// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoStatementBypassesTheSeam is the static half of "every statement Quark
// sends passes the middleware chain and reaches the observers" (A11 Q7). The
// battery tests (observe_test.go, the extension bench's CON-04) measure what
// a recording driver receives on the paths they run; this one reads every
// package of the module, type-checked, and fails on a path they do not run:
//
//  1. a call to a statement method of database/sql — Exec, Query, QueryRow,
//     their Context forms, Prepare, Conn.Raw — on a *sql.DB, *sql.Tx,
//     *sql.Conn or *sql.Stmt;
//  2. a *sql.DB, *sql.Tx or *sql.Conn handed, as an interface that can run
//     statements (an Executor), to anything but the seam, which would run
//     them on the raw handle;
//  3. a statement method called on an Executor held in a struct field, the
//     way q.exec holds the query's executor: only the seam may call it;
//  4. a QueryEvent literal without a Kind.
//
// Each exception is listed below with the reason it is one. What it cannot
// see is an Executor value that already holds a raw handle being passed on;
// that is what the battery tests measure.
//
// It runs the go command (go list -export) to type-check, so it is skipped
// under -short, like the benches' child-process probes.
func TestNoStatementBypassesTheSeam(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list -export to type-check the module; measured in the full (non -short) lane")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("the go command is not on PATH: %v", err)
	}

	pkgs := listModulePackages(t)
	exports := map[string]string{}
	for _, p := range pkgs {
		for k, v := range p.exports {
			exports[k] = v
		}
	}
	imp := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok || f == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})

	var violations []string
	checked := 0
	for _, p := range pkgs {
		if why, skip := seamGuardSkipped[p.path]; skip {
			t.Logf("not checked: %s — %s", p.path, why)
			continue
		}
		fset := token.NewFileSet()
		var files []*ast.File
		for _, name := range p.files {
			f, err := parser.ParseFile(fset, filepath.Join(p.dir, name), nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			files = append(files, f)
		}
		info := &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
		}
		conf := types.Config{Importer: imp}
		if _, err := conf.Check(p.path, fset, files, info); err != nil {
			t.Fatalf("type-check %s: %v", p.path, err)
		}
		checked++
		for _, f := range files {
			violations = append(violations, seamViolations(p.path, fset, f, info)...)
		}
	}
	if checked == 0 {
		t.Fatal("no package checked")
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v)
	}
	t.Logf("%d packages checked, %d statements past the seam", checked, len(violations))
}

// seamGuardSkipped are the packages of the module the guard does not read.
var seamGuardSkipped = map[string]string{
	"github.com/jcsvwinston/quark/quarkdriver/drivertest":       "the conformance kit a driver runs in its own tests: what it sends on the *sql.DB the test hands it is the kit's, not a client's",
	"github.com/jcsvwinston/quark/quarkdriver/drivertest/suite": "the engine suite a driver runs in its own tests: its setup statements on client.Raw() are the suite's",
	"github.com/jcsvwinston/quark/internal/db":                  "the CLI's catalog reader, on a *sql.DB the CLI opens with no client and so no chain",
}

// seamAllowed are the functions that may send statements past the seam,
// keyed by package path and function (Type.Method for a method).
var seamAllowed = map[string]string{
	"github.com/jcsvwinston/quark nativeRLSExecutor.ExecContext":     "beneath the seam: the chain's innermost link calls this Executor, and the statement it runs is the one the chain wraps; the set_config before it travels with it",
	"github.com/jcsvwinston/quark nativeRLSExecutor.QueryContext":    "beneath the seam, as ExecContext",
	"github.com/jcsvwinston/quark nativeRLSExecutor.QueryRowContext": "beneath the seam, as ExecContext",
	"github.com/jcsvwinston/quark Client.execEngine":                 "the chain's innermost link: with WithStatementCache it runs the statement the chain wraps on a cached *sql.Stmt, with the same text and arguments (ADR-0027)",
	"github.com/jcsvwinston/quark Client.queryEngine":                "the chain's innermost link, as Client.execEngine",
	"github.com/jcsvwinston/quark Client.queryRowEngine":             "the chain's innermost link, as Client.execEngine",
	"github.com/jcsvwinston/quark stmtLRU.acquire":                   "prepares, for the innermost link, the statement the chain is wrapping; the prepare is not a statement of its own (ADR-0027)",
	"github.com/jcsvwinston/quark txStmts.get":                       "prepares on a transaction, for the innermost link, the statement the chain is wrapping (ADR-0027)",
	"github.com/jcsvwinston/quark valueRow":                          "an in-process driver that mints a *sql.Row from values already read; nothing reaches the engine",
	"github.com/jcsvwinston/quark errorRow":                          "an in-process driver that mints a *sql.Row carrying an error; nothing reaches the engine",
}

// seamCallees are what a raw handle may be handed to: the seam itself, and
// the hook that gives the migrate and quarktenant packages its Executor
// (migrate's ledger helper is a call to it).
var seamCallees = map[string]bool{
	"github.com/jcsvwinston/quark/migrate ledger":            true,
	"github.com/jcsvwinston/quark execStmt":                  true,
	"github.com/jcsvwinston/quark queryStmt":                 true,
	"github.com/jcsvwinston/quark queryRowStmt":              true,
	"github.com/jcsvwinston/quark observed":                  true,
	"github.com/jcsvwinston/quark schemaExec":                true,
	"github.com/jcsvwinston/quark/internal/observe Executor": true,
}

var statementMethods = map[string]bool{
	"Exec": true, "ExecContext": true,
	"Query": true, "QueryContext": true,
	"QueryRow": true, "QueryRowContext": true,
	"Prepare": true, "PrepareContext": true,
}

// rawHandle reports whether t is *sql.DB, *sql.Tx, *sql.Conn or *sql.Stmt,
// and which.
func rawHandle(t types.Type) (string, bool) {
	p, ok := t.(*types.Pointer)
	if !ok {
		return "", false
	}
	n, ok := p.Elem().(*types.Named)
	if !ok || n.Obj().Pkg() == nil || n.Obj().Pkg().Path() != "database/sql" {
		return "", false
	}
	switch name := n.Obj().Name(); name {
	case "DB", "Tx", "Conn", "Stmt":
		return name, true
	}
	return "", false
}

func seamViolations(pkg string, fset *token.FileSet, f *ast.File, info *types.Info) []string {
	var out []string
	report := func(pos token.Pos, fn, what string) {
		if _, ok := seamAllowed[pkg+" "+fn]; ok {
			return
		}
		p := fset.Position(pos)
		out = append(out, fmt.Sprintf("%s:%d in %s: %s", filepath.Base(p.Filename), p.Line, fn, what))
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fn := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			fn = recvName(fd.Recv.List[0].Type) + "." + fn
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				checkCall(pkg, fn, n, info, report)
			case *ast.CompositeLit:
				if pkg != "github.com/jcsvwinston/quark" {
					return true
				}
				tv, ok := info.Types[n]
				if !ok {
					return true
				}
				if nt, ok := tv.Type.(*types.Named); ok && nt.Obj().Name() == "QueryEvent" && nt.Obj().Pkg().Path() == pkg {
					hasKind := false
					for _, el := range n.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Kind" {
								hasKind = true
							}
						}
					}
					if !hasKind {
						report(n.Pos(), fn, "a QueryEvent with no Kind")
					}
				}
			}
			return true
		})
	}
	return out
}

func checkCall(pkg, fn string, call *ast.CallExpr, info *types.Info, report func(token.Pos, string, string)) {
	sel, isSel := call.Fun.(*ast.SelectorExpr)

	// 1 and 3: a statement method on a raw handle, or on an Executor held in
	// a struct field.
	if isSel && (statementMethods[sel.Sel.Name] || sel.Sel.Name == "Raw") {
		if tv, ok := info.Types[sel.X]; ok {
			if name, raw := rawHandle(tv.Type); raw && (sel.Sel.Name != "Raw" || name == "Conn") {
				report(call.Pos(), fn, fmt.Sprintf("%s on a *sql.%s, past the seam", sel.Sel.Name, name))
			}
			if isExecutor(tv.Type) && statementMethods[sel.Sel.Name] {
				if inner, ok := sel.X.(*ast.SelectorExpr); ok {
					if s, ok := info.Selections[inner]; ok && s.Kind() == types.FieldVal {
						report(call.Pos(), fn, fmt.Sprintf("%s on the Executor in field %s, past the seam", sel.Sel.Name, inner.Sel.Name))
					}
				}
			}
		}
	}

	// 2: a raw handle handed to an interface parameter of anything but the
	// seam.
	sig, ok := info.Types[call.Fun].Type.(*types.Signature)
	if !ok {
		return
	}
	callee := calleeKey(call.Fun, info)
	if seamCallees[callee] {
		return
	}
	params := sig.Params()
	for i, arg := range call.Args {
		tv, ok := info.Types[arg]
		if !ok {
			continue
		}
		name, raw := rawHandle(tv.Type)
		if !raw || name == "Stmt" {
			continue
		}
		var pt types.Type
		switch {
		case sig.Variadic() && i >= params.Len()-1:
			last := params.At(params.Len() - 1).Type()
			if s, ok := last.(*types.Slice); ok && !call.Ellipsis.IsValid() {
				pt = s.Elem()
			} else {
				pt = last
			}
		case i < params.Len():
			pt = params.At(i).Type()
		default:
			continue
		}
		if runsStatements(pt) {
			report(arg.Pos(), fn, fmt.Sprintf("a *sql.%s handed to %s as an interface that runs statements, past the seam", name, callee))
		}
	}
}

// isExecutor reports whether t is quarkdriver.Executor (quark.Executor is an
// alias of it).
func isExecutor(t types.Type) bool {
	n, ok := t.(*types.Named)
	return ok && n.Obj().Name() == "Executor" && n.Obj().Pkg() != nil &&
		n.Obj().Pkg().Path() == "github.com/jcsvwinston/quark/quarkdriver"
}

// runsStatements reports whether t is an interface with a statement method:
// a raw handle converted to it can be used to run statements.
func runsStatements(t types.Type) bool {
	iface, ok := t.Underlying().(*types.Interface)
	if !ok {
		return false
	}
	for i := 0; i < iface.NumMethods(); i++ {
		if statementMethods[iface.Method(i).Name()] {
			return true
		}
	}
	return false
}

// calleeKey names what a call calls as "package name", for a function, a
// method or a package-level func variable.
func calleeKey(fun ast.Expr, info *types.Info) string {
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return "(func value)"
	}
	obj := info.Uses[id]
	if obj == nil || obj.Pkg() == nil {
		return id.Name
	}
	return obj.Pkg().Path() + " " + obj.Name()
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

type modulePackage struct {
	path    string
	dir     string
	files   []string
	exports map[string]string
}

// listModulePackages lists the module's packages with their non-test Go
// files, and the export data of everything they import.
func listModulePackages(t *testing.T) []modulePackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-export", "-deps", "-json=ImportPath,Dir,GoFiles,Export,Module,Standard", "./...")
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	type listed struct {
		ImportPath string
		Dir        string
		GoFiles    []string
		Export     string
		Standard   bool
		Module     *struct{ Path string }
	}
	exports := map[string]string{}
	var mine []listed
	dec := json.NewDecoder(&stdout)
	for dec.More() {
		var p listed
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("go list output: %v", err)
		}
		exports[p.ImportPath] = p.Export
		if p.Module != nil && p.Module.Path == "github.com/jcsvwinston/quark" && len(p.GoFiles) > 0 {
			mine = append(mine, p)
		}
	}
	out := make([]modulePackage, 0, len(mine))
	for _, p := range mine {
		if strings.Contains(p.ImportPath, "/internal/extbench") || strings.Contains(p.ImportPath, "/internal/enterprisebench") {
			continue // test-only packages: no Go files outside _test.go
		}
		out = append(out, modulePackage{path: p.ImportPath, dir: p.Dir, files: p.GoFiles, exports: exports})
	}
	return out
}
