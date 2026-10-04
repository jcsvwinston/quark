// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// Probes for the "integrations" family: Quark served from chi, Echo, Gin,
// gRPC and Nucleus as TESTED FIXTURES — modules of this repository that the
// CI compiles against the framework and runs (the owner's decision of
// 2026-10-04: examples/ is not coming back; integrations live where a build
// checks them).
//
// An integration exists, for this bench, when a module of the repository
// requires the framework directly, the packages of it that import the
// framework pass their tests standalone, and one of those tests holds the
// frameworks guide's section to that code. Nothing else compiles against a
// framework: a code block in a guide is text until a test compares it with
// code that builds, and a module that requires the framework only
// indirectly never imports it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

type framework struct {
	name    string // as the guide's heading spells it
	module  string // the module path a fixture would require
	initArg string // the `quark init --with` value that would write it
}

// Echo is measured at v5, its current major (v5.0.0, January 2026); v4 still
// receives releases, and the guide says what changes on it.
var frameworks = map[string]framework{
	"chi":     {name: "chi", module: "github.com/go-chi/chi/v5", initArg: "chi"},
	"echo":    {name: "Echo", module: "github.com/labstack/echo/v5", initArg: "echo"},
	"gin":     {name: "Gin", module: "github.com/gin-gonic/gin", initArg: "gin"},
	"grpc":    {name: "gRPC", module: "google.golang.org/grpc", initArg: "grpc"},
	"nucleus": {name: "Nucleus", module: "github.com/jcsvwinston/nucleus", initArg: "nucleus"},
}

// directRequirers returns the modules of the repository whose go.mod
// requires module directly (not `// indirect`).
func directRequirers(t *testing.T, e *env, module string) []string {
	t.Helper()
	direct, _ := requirers(t, e, module)
	return direct
}

// requirers returns the modules of the repository whose go.mod requires
// module, split into direct and indirect requirements.
func requirers(t *testing.T, e *env, module string) (direct, indirect []string) {
	t.Helper()
	for _, m := range repoModules(t, e) {
		raw, err := os.ReadFile(filepath.Join(e.root, m, "go.mod"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "require "))
			if len(f) >= 2 && (f[0] == module || strings.HasPrefix(f[0], module+"/")) {
				if strings.Contains(line, "// indirect") {
					indirect = append(indirect, m)
				} else {
					direct = append(direct, m)
				}
				break
			}
		}
	}
	return direct, indirect
}

// theExample is how the guide's prose points the reader at a runnable
// example — "The example wires…", across a line break as often as not.
var theExample = regexp.MustCompile(`The\s+example\b`)

// guideSection returns the Go code the frameworks guide shows under the
// framework's heading, or "" when it has no section.
func guideSection(t *testing.T, e *env, fw framework) (code string, describesExample bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.root, "website", "docs", "guides", "frameworks.mdx"))
	if err != nil {
		t.Fatalf("read the frameworks guide: %v", err)
	}
	sections := regexp.MustCompile(`(?m)^## `).Split(string(raw), -1)
	for _, s := range sections {
		if !strings.HasPrefix(s, fw.name+"\n") {
			continue
		}
		var b strings.Builder
		for _, block := range regexp.MustCompile("(?s)```go\n(.*?)```").FindAllStringSubmatch(s, -1) {
			b.WriteString(block[1])
		}
		return b.String(), theExample.MatchString(s)
	}
	return "", false
}

// guideTest is the test a fixture package runs to hold its section of the
// frameworks guide to its code (internal/integrations/guide). The bench asks
// for it by name, the way DRV-08 asks for the kit's subtest: a fixture whose
// tests pass while nothing compares the page with the code would leave the
// page free to drift again.
const guideTest = "TestGuideMatchesFixture"

// importers returns the packages of the module in dir that import module
// (or a package under it) from their code or their tests.
func importers(t *testing.T, dir, module string) ([]string, error) {
	t.Helper()
	out, err := goRun(t, dir, []string{"GOWORK=off"}, "list", "-f",
		"{{.ImportPath}}\t{{join .Imports \" \"}} {{join .TestImports \" \"}} {{join .XTestImports \" \"}}", "./...")
	if err != nil {
		return nil, &goError{"go list", out, err}
	}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		pkg, imports, _ := strings.Cut(line, "\t")
		for _, imp := range strings.Fields(imports) {
			if imp == module || strings.HasPrefix(imp, module+"/") {
				pkgs = append(pkgs, pkg)
				break
			}
		}
	}
	return pkgs, nil
}

func integrationProbe(key string) func(t *testing.T, e *env) verdict {
	return func(t *testing.T, e *env) verdict {
		fw := frameworks[key]
		mods, indirect := requirers(t, e, fw.module)
		code, describesExample := guideSection(t, e, fw)
		lines := 0
		if code != "" {
			lines = strings.Count(code, "\n")
		}
		t.Logf("%s: modules of the repository requiring %s directly: %v; only indirectly (they never import it): %v", fw.name, fw.module, mods, indirect)
		t.Logf("%s: the frameworks guide shows %d lines of Go for it; its prose points the reader at an example: %v", fw.name, lines, describesExample)
		if len(mods) == 0 {
			return absent
		}
		childProbe(t)
		for _, m := range mods {
			dir := filepath.Join(e.root, m)
			pkgs, err := importers(t, dir, fw.module)
			if err != nil {
				t.Logf("%s: %v", m, err)
				continue
			}
			if len(pkgs) == 0 {
				t.Logf("%s requires %s and no package of it imports it", m, fw.module)
				continue
			}
			out, err := goRun(t, dir, []string{"GOWORK=off"}, append([]string{"test", "-count=1", "-json"}, pkgs...)...)
			run := parseTestJSON(out)
			held := run.action[guideTest] == "pass"
			t.Logf("%s standalone, the packages importing %s %v: err=%v; %s passed: %v", m, fw.module, pkgs, err, guideTest, held)
			if err != nil {
				t.Logf("%s", out)
				continue
			}
			if held {
				return present
			}
		}
		return partial
	}
}

// initWithTargets reads the --with targets `quark init` accepts. The CLI is a
// module of its own (ADR-0024) that this bench cannot import; the value is
// read from its source as the type checker would see it — the slice literal
// the flag's validation consults — not grepped.
func initWithTargets(t *testing.T, e *env) []string {
	t.Helper()
	path := filepath.Join(e.root, "cmd", "quark", "commands", "init.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var targets []string
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range vs.Names {
			if name.Name != "initWithTargets" || i >= len(vs.Values) {
				continue
			}
			lit, ok := vs.Values[i].(*ast.CompositeLit)
			if !ok {
				continue
			}
			found = true
			for _, elt := range lit.Elts {
				if bl, ok := elt.(*ast.BasicLit); ok {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						targets = append(targets, s)
					}
				}
			}
		}
		return true
	})
	if !found {
		t.Fatalf("%s no longer declares initWithTargets: read the flag's validation again before recording a verdict", path)
	}
	return targets
}

func probeInitWith(t *testing.T, e *env) verdict {
	accepted := map[string]bool{}
	for _, target := range initWithTargets(t, e) {
		accepted[target] = true
	}
	var yes, no, uncompiled []string
	for _, key := range []string{"chi", "echo", "gin", "grpc", "nucleus"} {
		if !accepted[frameworks[key].initArg] {
			no = append(no, key)
			continue
		}
		yes = append(yes, key)
		// What --with writes is source text for a framework this module
		// does not require; it is checked only where a module compiles it.
		if len(directRequirers(t, e, frameworks[key].module)) == 0 {
			uncompiled = append(uncompiled, key)
		}
	}
	t.Logf("quark init --with accepts %v and refuses %v; accepted targets whose output no module of the repository compiles: %v", yes, no, uncompiled)
	switch {
	case len(no) == 0 && len(uncompiled) == 0:
		return present
	case len(yes) > 0:
		return partial
	default:
		return absent
	}
}

// One named probe per framework, so the catalogue reads as the others do.
func probeIntegrationChi(t *testing.T, e *env) verdict     { return integrationProbe("chi")(t, e) }
func probeIntegrationEcho(t *testing.T, e *env) verdict    { return integrationProbe("echo")(t, e) }
func probeIntegrationGin(t *testing.T, e *env) verdict     { return integrationProbe("gin")(t, e) }
func probeIntegrationGRPC(t *testing.T, e *env) verdict    { return integrationProbe("grpc")(t, e) }
func probeIntegrationNucleus(t *testing.T, e *env) verdict { return integrationProbe("nucleus")(t, e) }
