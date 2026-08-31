// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// DX-18: `quark migrate create <name> --from-models <dir> --dialect <d>`
// renders domain DDL from the project's model structs — the number-one gap
// of the DX audit: three paths to a schema and none produced DDL usable on
// PostgreSQL, so reference apps wrote every CREATE TABLE by hand.
//
// The structs are loaded statically with go/packages (the CLI cannot
// compile user models into itself), and the SQL types come from the SAME
// mapping the runtime migrator uses (internal/migrate.SQLTypeWithOpts and
// PKColumnSQL), so the generated DDL matches what `client.Migrate` would
// have created.
package commands

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jcsvwinston/quark/internal/migrate"
	"github.com/jcsvwinston/quark/internal/schema"
	"golang.org/x/tools/go/packages"
)

type ddlField struct {
	Column    string
	GoType    string
	IsPK      bool
	NotNull   bool
	Unique    bool
	IsVersion bool
	Size      int
	Precision int
	Scale     int
}

type ddlModel struct {
	Name   string
	Table  string
	Fields []ddlField
	// FKs: column -> referenced table (from rel:"belongs_to" join:"col"
	// sibling fields; the referenced table derives from the field's type).
	FKs map[string]string
}

// loadModelsForDDL statically extracts every exported struct carrying db
// tags from the package at dir.
func loadModelsForDDL(dir string) ([]ddlModel, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Dir:  ".",
	}
	pkgs, err := packages.Load(cfg, dir)
	if err != nil {
		return nil, fmt.Errorf("loading models from %s: %w", dir, err)
	}
	// packages.Load returns zero packages (with no per-package error) when the
	// pattern resolves to nothing — most often because dir is not inside a Go
	// module. Report THAT, not a misleading "no model structs" (QC-4).
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no Go package found at %s — check the path exists and is inside a Go module (run 'go mod init' or 'quark init' first)", dir)
	}
	// Compile errors are the real cause when the package exists but does not
	// type-check; surfacing them beats claiming there are no models (QC-4).
	var loadErrs []string
	for _, p := range pkgs {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	}
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("the models package %s does not compile — fix these first:\n  %s", dir, strings.Join(loadErrs, "\n  "))
	}

	var models []ddlModel
	for _, p := range pkgs {
		tableNames := literalTableNames(p)
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			m, extracted := extractDDLModel(obj, tableNames)
			if extracted {
				models = append(models, m)
			}
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("%s compiled but defines no model structs (no exported struct carries `db` tags)", dir)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

// literalTableNames scans the package AST for `func (X) TableName() string
// { return "..." }` methods with a literal return and maps type name to it.
func literalTableNames(p *packages.Package) map[string]string {
	out := map[string]string{}
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name.Name != "TableName" || len(fd.Body.List) != 1 {
				continue
			}
			ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			lit, ok := ret.Results[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			recv := fd.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if ident, ok := recv.(*ast.Ident); ok {
				out[ident.Name] = val
			}
		}
	}
	return out
}

// buildDDLStatements renders CREATE/DROP statement lists for the dialect,
// parents before children (Kahn over the FK graph, declaration order as the
// stable tie-break).
func buildDDLStatements(models []ddlModel, dialect string) (up, down []string, err error) {
	ordered := topoOrderModels(models)

	for _, m := range ordered {
		var cols []string
		for _, f := range m.Fields {
			colSQL, cerr := ddlColumnSQL(f, dialect)
			if cerr != nil {
				return nil, nil, fmt.Errorf("model %s, column %s: %w", m.Name, f.Column, cerr)
			}
			cols = append(cols, "\t"+f.Column+" "+colSQL)
		}
		var fkCols []string
		for col := range m.FKs {
			fkCols = append(fkCols, col)
		}
		sort.Strings(fkCols)
		for _, col := range fkCols {
			cols = append(cols, fmt.Sprintf("\tFOREIGN KEY (%s) REFERENCES %s(id)", col, m.FKs[col]))
		}
		up = append(up, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n%s\n)", m.Table, strings.Join(cols, ",\n")))
		for _, col := range fkCols {
			up = append(up, fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_%s ON %s (%s)", m.Table, col, m.Table, col))
		}
	}

	for i := len(ordered) - 1; i >= 0; i-- {
		down = append(down, "DROP TABLE IF EXISTS "+ordered[i].Table)
	}
	return up, down, nil
}

// ddlColumnSQL maps one field to its dialect column fragment using the
// runtime migrator's own mapping.
func ddlColumnSQL(f ddlField, dialect string) (string, error) {
	opts := migrate.TypeOptions{Size: f.Size, Precision: f.Precision, Scale: f.Scale, IsPK: f.IsPK}

	if f.IsPK {
		rt, ok := staticGoType(f.GoType)
		if ok && isIntegerKind(rt.Kind()) {
			return migrate.PKColumnSQL(dialect, migrate.PKInteger, ""), nil
		}
		if f.GoType == "string" {
			return migrate.PKColumnSQL(dialect, migrate.PKString, ""), nil
		}
	}

	goType := f.GoType
	nullable := false
	if strings.HasPrefix(goType, "*") {
		goType, nullable = goType[1:], true
	}
	if inner, ok := strings.CutPrefix(goType, "quark.Nullable["); ok {
		goType, nullable = strings.TrimSuffix(inner, "]"), true
	}
	if strings.HasPrefix(goType, "quark.JSON[") || strings.HasPrefix(goType, "quark.Array[") || goType == "json.RawMessage" {
		goType = "json.RawMessage"
	}

	rt, ok := staticGoType(goType)
	if !ok {
		return "", fmt.Errorf("unsupported Go type %q for static DDL generation — supported: bool, ints, floats, string, time.Time, json.RawMessage, quark.Nullable/Array/JSON, pointers", f.GoType)
	}
	sqlType := migrate.SQLTypeWithOpts(dialect, rt, opts)

	var b strings.Builder
	b.WriteString(sqlType)
	if f.IsVersion {
		b.WriteString(" NOT NULL DEFAULT 0")
		return b.String(), nil
	}
	if f.NotNull && !nullable {
		b.WriteString(" NOT NULL")
	}
	if f.Unique {
		b.WriteString(" UNIQUE")
	}
	return b.String(), nil
}

var staticTypes = map[string]reflect.Type{
	"bool":            reflect.TypeOf(false),
	"int":             reflect.TypeOf(int(0)),
	"int8":            reflect.TypeOf(int8(0)),
	"int16":           reflect.TypeOf(int16(0)),
	"int32":           reflect.TypeOf(int32(0)),
	"int64":           reflect.TypeOf(int64(0)),
	"uint":            reflect.TypeOf(uint(0)),
	"uint32":          reflect.TypeOf(uint32(0)),
	"uint64":          reflect.TypeOf(uint64(0)),
	"float32":         reflect.TypeOf(float32(0)),
	"float64":         reflect.TypeOf(float64(0)),
	"string":          reflect.TypeOf(""),
	"time.Time":       reflect.TypeOf(time.Time{}),
	"json.RawMessage": reflect.TypeOf(json.RawMessage{}),
}

func staticGoType(goType string) (reflect.Type, bool) {
	t, ok := staticTypes[goType]
	return t, ok
}

func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

func topoOrderModels(models []ddlModel) []ddlModel {
	byTable := map[string]ddlModel{}
	for _, m := range models {
		byTable[m.Table] = m
	}
	placed := map[string]bool{}
	var out []ddlModel
	for len(out) < len(models) {
		progressed := false
		for _, m := range models {
			if placed[m.Table] {
				continue
			}
			ready := true
			for _, parent := range m.FKs {
				if parent == m.Table {
					continue // self-reference
				}
				if _, in := byTable[parent]; in && !placed[parent] {
					ready = false
					break
				}
			}
			if ready {
				out = append(out, m)
				placed[m.Table] = true
				progressed = true
			}
		}
		if !progressed { // FK cycle — declaration order for the rest
			for _, m := range models {
				if !placed[m.Table] {
					out = append(out, m)
					placed[m.Table] = true
				}
			}
		}
	}
	return out
}

// snakePluralTable mirrors the runtime's default table naming.
func snakePluralTable(name string) string {
	return schema.ToSnakeCase(schema.Pluralize(name))
}

// extractDDLModel converts one exported named struct into a ddlModel; ok is
// false when the object is not a struct carrying db tags.
func extractDDLModel(obj types.Object, tableNames map[string]string) (ddlModel, bool) {
	named, ok := obj.Type().(*types.Named)
	if !ok {
		return ddlModel{}, false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return ddlModel{}, false
	}

	m := ddlModel{Name: obj.Name(), FKs: map[string]string{}}
	if tn, ok := tableNames[obj.Name()]; ok {
		m.Table = tn
	} else {
		m.Table = snakePluralTable(obj.Name())
	}

	for i := 0; i < st.NumFields(); i++ {
		fld := st.Field(i)
		if !fld.Exported() {
			continue
		}
		tag := reflect.StructTag(st.Tag(i))

		// belongs_to relation pair: record the FK edge, skip the field.
		if rel := tag.Get("rel"); rel != "" {
			if rel == "belongs_to" {
				join := tag.Get("join")
				refType := fld.Type()
				if ptr, ok := refType.(*types.Pointer); ok {
					refType = ptr.Elem()
				}
				if refNamed, ok := refType.(*types.Named); ok && join != "" {
					m.FKs[join] = snakePluralTable(refNamed.Obj().Name())
				}
			}
			continue
		}

		dbTag := tag.Get("db")
		if dbTag == "" || dbTag == "-" {
			continue
		}
		parts := strings.Split(dbTag, ",")
		f := ddlField{Column: strings.TrimSpace(parts[0])}
		for _, opt := range parts[1:] {
			opt = strings.TrimSpace(opt)
			if eq := strings.IndexByte(opt, '='); eq > 0 {
				n, _ := strconv.Atoi(strings.TrimSpace(opt[eq+1:]))
				switch strings.ToLower(strings.TrimSpace(opt[:eq])) {
				case "size":
					f.Size = n
				case "precision":
					f.Precision = n
				case "scale":
					f.Scale = n
				}
			}
		}
		f.IsPK = strings.EqualFold(tag.Get("pk"), "true") && tag.Get("pk") != ""
		for _, token := range strings.Split(tag.Get("quark"), ",") {
			switch strings.TrimSpace(token) {
			case "not_null":
				f.NotNull = true
			case "unique":
				f.Unique = true
			case "version":
				f.IsVersion = true
			}
		}
		f.GoType = staticTypeName(fld.Type())
		m.Fields = append(m.Fields, f)
	}

	if len(m.Fields) == 0 {
		return ddlModel{}, false
	}
	// db:"id" fallback PK, mirroring the runtime.
	hasPK := false
	for _, f := range m.Fields {
		if f.IsPK {
			hasPK = true
			break
		}
	}
	if !hasPK {
		for i := range m.Fields {
			if m.Fields[i].Column == "id" {
				m.Fields[i].IsPK = true
				break
			}
		}
	}
	return m, true
}

// staticTypeName renders a field type the way the static tables expect:
// "int64", "time.Time", "json.RawMessage", "quark.Nullable[string]",
// "*string".
func staticTypeName(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}
