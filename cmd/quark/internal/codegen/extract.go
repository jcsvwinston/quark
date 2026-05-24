// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package codegen implements `quark gen`: it parses a user's package with
// go/packages + go/types (not reflection, so the tool can be `go install`ed
// and driven from //go:generate) and emits, per package, a quark_gen.go
// that registers typed implementations with the runtime registry in package
// quark.
//
// F6-1 ships the pipeline and the registration contract only: the emitted
// scanner/binder are inert stubs (quark.StubScanner / quark.StubBinder) and
// the runtime hot paths do not yet consult the registry. The typed fast
// path is F6-2 (scanning) and F6-3 (binding). See ADR-0014.
package codegen

import (
	"errors"
	"fmt"
	"go/types"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/internal/schema"
	"golang.org/x/tools/go/packages"
)

const loadMode = packages.NeedName | packages.NeedFiles | packages.NeedTypes |
	packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports | packages.NeedDeps

// ModelDef is a model struct discovered in a package: a struct with at
// least one exported field carrying a `db` tag.
type ModelDef struct {
	Name   string
	Fields []quark.ModelField
	Hash   string
	// ColTypes holds, parallel to Fields, the Go type of each field rendered
	// for use *inside the model's own package* — i.e. with the local package
	// qualifier stripped ("Status", not "sample.Status"). This is what the
	// generated typed-column accessors (F6-4) parameterise over. It is kept
	// separate from quark.ModelField.GoType, which stays qualified for every
	// package so it matches reflect.Type.String() in the conformance hash.
	ColTypes []string
}

// PackageModels groups the models found in one package, with the on-disk
// directory where its quark_gen.go should be written.
type PackageModels struct {
	PkgPath string
	PkgName string
	Dir     string
	Models  []ModelDef
	// Imports maps import path -> package name for every non-local package
	// referenced by a field's ColType (e.g. "time" -> "time"). The emitter
	// merges these with the always-needed imports so the generated
	// typed-column declarations compile.
	Imports map[string]string
}

// qualifier renders types with the package's name (e.g. "quark.JSON[string]",
// "time.Time"), matching how reflect.Type.String() qualifies types, so the
// AST-derived GoType lines up with the reflection-derived one after
// quark.CanonicalType.
func qualifier(p *types.Package) string { return p.Name() }

// Load type-checks the packages matching patterns and returns those that
// contain at least one model. Patterns use go/packages syntax (e.g.
// "./...", an import path, or a directory).
func Load(patterns ...string) ([]PackageModels, error) {
	pkgs, err := packages.Load(&packages.Config{Mode: loadMode}, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	var loadErr error
	var out []PackageModels
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			for _, e := range p.Errors {
				loadErr = errors.Join(loadErr, fmt.Errorf("%s: %s", p.PkgPath, e))
			}
			continue
		}
		if pm := extractPackage(p); len(pm.Models) > 0 {
			out = append(out, pm)
		}
	}
	if loadErr != nil {
		return nil, loadErr
	}
	return out, nil
}

func extractPackage(p *packages.Package) PackageModels {
	pm := PackageModels{PkgPath: p.PkgPath, PkgName: p.Name, Imports: map[string]string{}}
	if len(p.GoFiles) > 0 {
		pm.Dir = filepath.Dir(p.GoFiles[0])
	}
	scope := p.Types.Scope()
	for _, name := range scope.Names() {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		if fields, colTypes := extractFields(st, p.Types, pm.Imports); len(fields) > 0 {
			pm.Models = append(pm.Models, ModelDef{
				Name:     name,
				Fields:   fields,
				Hash:     quark.HashModelFields(fields),
				ColTypes: colTypes,
			})
		}
	}
	// Deterministic order so regenerated output is stable.
	sort.Slice(pm.Models, func(i, j int) bool { return pm.Models[i].Name < pm.Models[j].Name })
	return pm
}

// colTypeQualifier renders a field type for use inside local's own package:
// the local package qualifier is dropped (so a locally-defined type prints
// unqualified, as it must to compile in the generated file), and every other
// referenced package is recorded in imports (path -> name) so the emitter can
// import it. It is deliberately distinct from the GoType qualifier, which
// qualifies the local package too in order to match reflect.Type.String().
func colTypeQualifier(local *types.Package, imports map[string]string) types.Qualifier {
	return func(p *types.Package) string {
		if p == nil || p == local {
			return ""
		}
		imports[p.Path()] = p.Name()
		return p.Name()
	}
}

// extractFields returns the persisted fields of st: exported fields with a
// `db` tag. Column parsing reuses internal/schema (the same code the runtime
// uses), so column names cannot drift between generator and runtime; the
// only independently-derived attribute is GoType, which the conformance test
// validates against reflection.
//
// It returns, parallel to the fields, each field's ColType (the same Go type
// rendered for use inside local's own package) and records any packages those
// types reference into imports. local is the package being generated for.
func extractFields(st *types.Struct, local *types.Package, imports map[string]string) ([]quark.ModelField, []string) {
	colQual := colTypeQualifier(local, imports)
	var fields []quark.ModelField
	var colTypes []string
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		if !f.Exported() {
			continue
		}
		tag := reflect.StructTag(st.Tag(i))
		dbTag, ok := tag.Lookup("db")
		if !ok {
			continue
		}
		// Match buildInsert/scanRow: a field tagged db:"-" (or db:"") is not
		// persisted, so it must not appear in the generated scanner/binder or
		// the hash. Skipping it here keeps the generated code and ModelHash in
		// step with the runtime.
		col := schema.ColumnFromDBTag(dbTag)
		if col == "" || col == "-" {
			continue
		}
		// Unalias so an alias type (e.g. quark.Nullable[T], an alias of
		// sql.Null[T]) renders as its target, matching reflect.Type.String,
		// which resolves aliases. Resolves the outermost alias; a type
		// alias nested under a pointer/slice is a known gap the
		// conformance test would surface as drift.
		unaliased := types.Unalias(f.Type())
		fields = append(fields, quark.ModelField{
			Name:   f.Name(),
			Column: col,
			GoType: types.TypeString(unaliased, qualifier),
			IsPK:   strings.EqualFold(tag.Get("pk"), "true"),
		})
		// ColType: same type, but rendered for the local package (its own
		// qualifier stripped) and recording every other referenced package
		// into imports, so the generated Column[ColType] declarations compile.
		colTypes = append(colTypes, types.TypeString(unaliased, colQual))
	}
	return fields, colTypes
}
