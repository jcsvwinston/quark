// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

import (
	"bytes"
	"context"
	"encoding/json"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// quiet keeps the bench's output to its own verdicts.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// recorder captures the statements a client ran, as a QueryObserver sees
// them. Several contract controls compare what an observer saw with what a
// middleware saw: the two are the extension points a third party uses to
// watch Quark, and a statement one of them misses is a statement a tracer or
// a feed built on it never reports.
type recorder struct {
	mu     sync.Mutex
	events []quark.QueryEvent
}

func (r *recorder) ObserveQuery(ev quark.QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

func (r *recorder) sql() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.SQL)
	}
	return out
}

// env is what every probe shares: a context, the repository root the child
// processes run in, and the type-checked API that the contract probes read.
//
// There is no shared client. Nearly every probe here needs a client built
// with something of its own — a dialect, a middleware, a cache store — and
// those are fixed at New, so each probe opens the database it measures.
type env struct {
	tb   testing.TB
	ctx  context.Context
	root string // the repository root (the library module)

	apiOnce sync.Once
	api     *apiTypes
	apiErr  error
}

func newEnv(tb testing.TB) *env {
	tb.Helper()
	wd, err := os.Getwd()
	if err != nil {
		tb.Fatalf("working directory: %v", err)
	}
	root, err := filepath.Abs(filepath.Join(wd, "..", ".."))
	if err != nil {
		tb.Fatalf("repository root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		tb.Fatalf("%s is not the library module: %v", root, err)
	}
	return &env{tb: tb, ctx: context.Background(), root: root}
}

// open builds a client on a private in-memory SQLite database. name keeps
// probes from sharing a database by accident; opts are the extension points
// the probe is about.
func (e *env) open(t *testing.T, driverName, name string, opts ...any) (*quark.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	opts = append([]any{quark.WithLogger(quiet), quark.WithQueryObserver(rec)}, opts...)
	c, err := quark.New(driverName, "file:"+name+"?mode=memory&cache=shared", opts...)
	if err != nil {
		t.Fatalf("open %s on %s: %v", name, driverName, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec
}

// --- the go tool ------------------------------------------------------------
//
// The driver and integration controls are about BUILDS: what a module outside
// this repository has to import, whether it compiles with no workspace,
// whether a registry is race-free under the detector. Those questions are
// answered by the go command, so the probes run it.

// childProbe marks a probe that runs the go command. Under -short (the race
// lane) it is skipped: compiling a module per probe is the cost the full lane
// pays once, and the verdict is asserted there.
func childProbe(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go command in a child process; measured in the full (non -short) lane")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("the go command is not on PATH, and this probe measures a build: %v", err)
	}
}

// goRun runs the go command in dir with extra environment, and returns its
// combined output and error. GOFLAGS is cleared so a developer's setting does
// not change what is measured; GOWORK is set by the caller when it matters.
func goRun(t *testing.T, dir string, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	cmd.Env = append(cmd.Env, extraEnv...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// testEvent is one line of `go test -json`.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// testRun is what `go test -json` reported, per test: its final action and
// everything it printed.
type testRun struct {
	action map[string]string // test name → "pass" | "fail" | "skip"
	output map[string]string // test name → output
	raw    string
}

func parseTestJSON(raw string) testRun {
	r := testRun{action: map[string]string{}, output: map[string]string{}, raw: raw}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev testEvent
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Test == "" {
			continue
		}
		switch ev.Action {
		case "pass", "fail", "skip":
			r.action[ev.Test] = ev.Action
		case "output":
			r.output[ev.Test] += ev.Output
		}
	}
	return r
}

// --- the type-checked API -----------------------------------------------------
//
// The contract controls ask questions about TYPES: which interfaces a third
// party can implement, what packages a method signature names, whether an
// interface with a given method set is exported. go/types answers them from
// the compiler's own export data, located with `go list -export` — the same
// facts the compiler uses, not a parse of the source.

const (
	rootPkg       = "github.com/jcsvwinston/quark"
	driverPkg     = "github.com/jcsvwinston/quark/quarkdriver"
	drivertestPkg = "github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

type apiTypes struct {
	pkgs map[string]*types.Package
}

func (e *env) loadAPI(t *testing.T) *apiTypes {
	t.Helper()
	childProbe(t) // `go list -export` is a child go command too
	e.apiOnce.Do(func() {
		out, err := goRun(t, e.root, []string{"GOWORK=off"},
			"list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}",
			rootPkg, driverPkg, drivertestPkg)
		if err != nil {
			e.apiErr = &goError{"go list -export", out, err}
			return
		}
		exports := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if path, file, ok := strings.Cut(line, "="); ok {
				exports[path] = file
			}
		}
		imp := importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
			return os.Open(exports[path])
		})
		api := &apiTypes{pkgs: map[string]*types.Package{}}
		for _, p := range []string{rootPkg, driverPkg, drivertestPkg} {
			pkg, err := imp.Import(p)
			if err != nil {
				e.apiErr = err
				return
			}
			api.pkgs[p] = pkg
		}
		e.api = api
	})
	if e.apiErr != nil {
		t.Fatalf("type-check the public API: %v", e.apiErr)
	}
	return e.api
}

// lookup returns the named object of a package, or nil.
func (a *apiTypes) lookup(pkg, name string) types.Object {
	p := a.pkgs[pkg]
	if p == nil {
		return nil
	}
	return p.Scope().Lookup(name)
}

type goError struct {
	what string
	out  string
	err  error
}

func (g *goError) Error() string { return g.what + ": " + g.err.Error() + "\n" + g.out }
