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
}

// PackageModels groups the models found in one package, with the on-disk
// directory where its quark_gen.go should be written.
type PackageModels struct {
	PkgPath string
	PkgName string
	Dir     string
	Models  []ModelDef
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
	pm := PackageModels{PkgPath: p.PkgPath, PkgName: p.Name}
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
		if fields := extractFields(st); len(fields) > 0 {
			pm.Models = append(pm.Models, ModelDef{
				Name:   name,
				Fields: fields,
				Hash:   quark.HashModelFields(fields),
			})
		}
	}
	// Deterministic order so regenerated output is stable.
	sort.Slice(pm.Models, func(i, j int) bool { return pm.Models[i].Name < pm.Models[j].Name })
	return pm
}

// extractFields returns the persisted fields of st: exported fields with a
// `db` tag. Column parsing reuses internal/schema (the same code the runtime
// uses), so column names cannot drift between generator and runtime; the
// only independently-derived attribute is GoType, which the conformance test
// validates against reflection.
func extractFields(st *types.Struct) []quark.ModelField {
	var fields []quark.ModelField
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
		fields = append(fields, quark.ModelField{
			Name:   f.Name(),
			Column: col,
			// Unalias so an alias type (e.g. quark.Nullable[T], an alias of
			// sql.Null[T]) renders as its target, matching reflect.Type.String,
			// which resolves aliases. Resolves the outermost alias; a type
			// alias nested under a pointer/slice is a known gap the
			// conformance test would surface as drift.
			GoType: types.TypeString(types.Unalias(f.Type()), qualifier),
			IsPK:   strings.EqualFold(tag.Get("pk"), "true"),
		})
	}
	return fields
}
