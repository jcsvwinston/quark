// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// Probes for the "drivers" family: what it takes to ship a database engine
// for Quark from OUTSIDE this repository — register it, prove it conforms,
// and have it behave like the engines Quark ships.
//
// The fixture driver (testdata/extdriver) is copied into a temporary module
// whose path is example.com/extdriver, pointed at this tree with a replace,
// and built with GOWORK=off: the module graph a third party's driver has.
// The fixture carries no go.mod of its own on purpose — the CI step that
// checks every go.mod has a Dependabot entry would count a fixture as a
// module to keep updated.

import (
	"errors"
	"fmt"
	"go/build"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark/quarkdriver"

	moderncsqlite "modernc.org/sqlite"
)

// quarkdriverRegister registers, for engine, the classifier a modernc-backed
// driver module registers: the extended result codes 2067
// (SQLITE_CONSTRAINT_UNIQUE) and 1555 (SQLITE_CONSTRAINT_PRIMARYKEY).
func quarkdriverRegister(engine string) error {
	return quarkdriver.Register(engine, quarkdriver.Classifier{
		UniqueViolation: func(err error) bool {
			var e *moderncsqlite.Error
			if errors.As(err, &e) {
				return e.Code() == 2067 || e.Code() == 1555
			}
			return false
		},
		Deadlock:      func(error) bool { return false },
		TransientConn: func(error) bool { return false },
	})
}

// --- the fixture module ---------------------------------------------------------

// rootRequire returns the version the library's go.mod requires for path, and
// the go directive.
func rootRequire(t *testing.T, e *env, path string) (version, goVersion string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "go" {
			goVersion = f[1]
		}
		if len(f) >= 2 && f[0] == path {
			version = f[1]
		}
		if len(f) >= 3 && f[0] == "require" && f[1] == path {
			version = f[2]
		}
	}
	if version == "" || goVersion == "" {
		t.Fatalf("the library's go.mod names no %s requirement or no go directive", path)
	}
	return version, goVersion
}

// fixtureModule copies testdata/<name> into a temporary module example.com/<name>
// that requires this tree through a replace — the module a third party
// would have — and returns its directory. go.sum is seeded from the
// library's, so the build needs no module the library does not already pin.
func fixtureModule(t *testing.T, e *env, name string) string {
	t.Helper()
	src := filepath.Join(e.root, "internal", "extbench", "testdata", name)
	// The bench's own temporary directory, not the probe's: the module is
	// shared by several probes and must outlive the first one.
	dst := filepath.Join(e.tb.TempDir(), name)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatalf("copy the %s fixture: %v", name, err)
	}
	sqliteVersion, goVersion := rootRequire(t, e, "modernc.org/sqlite")
	gomod := fmt.Sprintf(`module example.com/%s

go %s

require (
	github.com/jcsvwinston/quark v0.0.0-00010101000000-000000000000
	modernc.org/sqlite %s
)

replace github.com/jcsvwinston/quark => %s
`, name, goVersion, sqliteVersion, e.root)
	if err := os.WriteFile(filepath.Join(dst, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(e.root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

// standalone is the environment of a module built the way a third party
// builds it: no workspace, and the module's own requirements completed.
var standalone = []string{"GOWORK=off", "GOFLAGS=-mod=mod"}

var (
	extModOnce sync.Once
	extModDir  string
)

// extdriverModule materialises the fixture driver module once per run.
func extdriverModule(t *testing.T, e *env) string {
	t.Helper()
	extModOnce.Do(func() { extModDir = fixtureModule(t, e, "extdriver") })
	if extModDir == "" {
		t.Fatal("the extdriver fixture module could not be materialised")
	}
	return extModDir
}

// deps returns the packages pkg transitively imports, as the go command
// resolves them in dir.
func deps(t *testing.T, dir string, env []string, pkg string) map[string]bool {
	t.Helper()
	out, err := goRun(t, dir, env, "list", "-deps", "-f", "{{.ImportPath}}", pkg)
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, out)
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		set[strings.TrimSpace(line)] = true
	}
	return set
}

// --- DRV-01, CON-08: the registries under the race detector ----------------------

var (
	raceOnce sync.Once
	raceRuns map[string]testRun
	raceErr  error
)

// registryRaces runs testdata/registryrace under -race: the dialect registry
// in a process of its own (an unsynchronised map can also kill the process
// with "concurrent map writes", which would take the other registries'
// verdicts with it), the other four together.
func registryRaces(t *testing.T, e *env) map[string]testRun {
	t.Helper()
	raceOnce.Do(func() {
		raceRuns = map[string]testRun{}
		for _, run := range []string{"^TestDialectRegistry$",
			"^Test(Classifier|ListenerFactory|TypeMapper|Codegen)Registry$"} {
			out, _ := goRun(t, e.root, []string{"GOWORK=off"},
				"test", "-race", "-count=1", "-json", "-run", run, "./internal/extbench/testdata/registryrace")
			parsed := parseTestJSON(out)
			if len(parsed.action) == 0 {
				raceErr = fmt.Errorf("go test -race -run %s reported no test: is the race detector available (cgo)?\n%s", run, out)
				return
			}
			raceRuns[run] = parsed
		}
	})
	if raceErr != nil {
		t.Fatal(raceErr)
	}
	return raceRuns
}

// raceVerdict reads one registry test: "clean" (passed under -race), "race"
// (failed with a data race report), or the failure is something else and
// the probe stops rather than call it a race.
func raceVerdict(t *testing.T, runs map[string]testRun, test string) string {
	t.Helper()
	for _, r := range runs {
		action, ok := r.action[test]
		if !ok {
			continue
		}
		switch {
		case action == "pass":
			return "clean"
		case action == "fail" && strings.Contains(r.output[test], "DATA RACE"):
			return "race"
		default:
			t.Fatalf("%s: %s without a race report — the fixture is broken, not measured:\n%s", test, action, r.output[test])
		}
	}
	t.Fatalf("%s did not run", test)
	return ""
}

func probeDialectRegistryRace(t *testing.T, e *env) verdict {
	childProbe(t)
	got := raceVerdict(t, registryRaces(t, e), "TestDialectRegistry")
	t.Logf("RegisterDialect alongside DetectDialect/DetectDialectByName under -race: %s", got)
	if got == "clean" {
		return present
	}
	return absent
}

func probeOtherRegistriesRace(t *testing.T, e *env) verdict {
	childProbe(t)
	runs := registryRaces(t, e)
	var raced []string
	tests := []string{"TestClassifierRegistry", "TestListenerFactoryRegistry", "TestTypeMapperRegistry", "TestCodegenRegistry"}
	for _, test := range tests {
		if raceVerdict(t, runs, test) == "race" {
			raced = append(raced, test)
		}
	}
	t.Logf("registries other than the dialect's that race under -race: %v (of %d)", raced, len(tests))
	switch len(raced) {
	case 0:
		return present
	case len(tests):
		return absent
	default:
		return partial
	}
}

// --- DRV-02 ------------------------------------------------------------------------

// The fixture's packages: the module root registers the engine with
// database/sql and its dialect with quarkdriver; errs registers the
// classifier; dialect writes the SQL.
const (
	extRootPkg       = "example.com/extdriver"
	extClassifierPkg = "example.com/extdriver/errs"
	extDialectPkg    = "example.com/extdriver/dialect"
)

func probeRegistrationWithoutRoot(t *testing.T, e *env) verdict {
	childProbe(t)
	dir := extdriverModule(t, e)
	classifierHalf := deps(t, dir, standalone, extClassifierPkg)
	dialectHalf := deps(t, dir, standalone, extDialectPkg)
	wholeDriver := deps(t, dir, standalone, extRootPkg)
	// Each half is measured only if the driver actually uses it: a dialect
	// package nothing imports would be root-free and prove nothing.
	if !wholeDriver[extClassifierPkg] || !wholeDriver[extDialectPkg] {
		t.Fatalf("the fixture driver does not import both of its halves (%s: %v, %s: %v): the probe would measure a package the driver does not register",
			extClassifierPkg, wholeDriver[extClassifierPkg], extDialectPkg, wholeDriver[extDialectPkg])
	}
	classifierFree := !classifierHalf[rootPkg] && classifierHalf[driverPkg]
	dialectFree := !dialectHalf[rootPkg] && dialectHalf[driverPkg]
	driverFree := !wholeDriver[rootPkg]

	// Where the contract a dialect implements is declared, and which types
	// of package quark its methods name: a type outside the package cannot
	// implement an interface whose methods name a type it cannot import.
	api := e.loadAPI(t)
	declaredIn := func(name string) string {
		obj := api.lookup(rootPkg, name)
		if n, ok := types.Unalias(obj.Type()).(*types.Named); ok && n.Obj().Pkg() != nil {
			return n.Obj().Pkg().Path()
		}
		return obj.Pkg().Path()
	}
	dialect := api.lookup(rootPkg, "Dialect").Type().Underlying().(*types.Interface)
	named := map[string]bool{}
	for i := 0; i < dialect.NumMethods(); i++ {
		sig := dialect.Method(i).Type().(*types.Signature)
		for _, tuple := range []*types.Tuple{sig.Params(), sig.Results()} {
			for j := 0; j < tuple.Len(); j++ {
				if n, ok := types.Unalias(tuple.At(j).Type()).(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == rootPkg {
					named["quark."+n.Obj().Name()] = true
				}
			}
		}
	}
	var rootTypes []string
	for n := range named {
		rootTypes = append(rootTypes, n)
	}
	sort.Strings(rootTypes)
	_, registryInLeaf := api.pkgs[driverPkg].Scope().Lookup("RegisterDialect").(*types.Func)
	t.Logf("the classifier half (%s) imports quarkdriver and not package quark: %v (%d packages)", extClassifierPkg, classifierFree, len(classifierHalf))
	t.Logf("the dialect half (%s) imports quarkdriver and not package quark: %v (%d packages)", extDialectPkg, dialectFree, len(dialectHalf))
	t.Logf("the whole driver (%s, both registered) is free of package quark: %v (%d packages)", extRootPkg, driverFree, len(wholeDriver))
	t.Logf("quark.Dialect is declared in %s; types of package quark its methods name: %v; quarkdriver.RegisterDialect exists: %v",
		declaredIn("Dialect"), rootTypes, registryInLeaf)

	switch {
	case classifierFree && dialectFree && driverFree:
		return present
	case classifierFree:
		return partial
	default:
		return absent
	}
}

// --- DRV-03 ------------------------------------------------------------------------

func probeExternalDriverEndToEnd(t *testing.T, e *env) verdict {
	childProbe(t)
	dir := extdriverModule(t, e)
	out, err := goRun(t, dir, standalone, "test", "-count=1", "-json", "./...")
	run := parseTestJSON(out)
	conformance, endToEnd := run.action["TestConformance"], run.action["TestEndToEnd"]
	kitRan := false
	for test, action := range run.action {
		if strings.HasPrefix(test, "TestConformance/") && action == "pass" {
			kitRan = true
		}
	}
	t.Logf("standalone (GOWORK=off) build and test of example.com/extdriver: err=%v; TestConformance=%s (kit subtests ran: %v); TestEndToEnd=%s",
		err, conformance, kitRan, endToEnd)
	if conformance == "" && endToEnd == "" {
		t.Fatalf("the fixture module did not build:\n%s", out)
	}
	switch {
	case conformance == "pass" && kitRan && endToEnd == "pass":
		return present
	case conformance == "pass" || endToEnd == "pass":
		return partial
	default:
		t.Logf("output:\n%s", out)
		return absent
	}
}

// --- DRV-04 ------------------------------------------------------------------------

// participantRecorded is the set of battery steps DRV-04 records as diverging.
// The probe compares the measured set with it exactly: a step that starts or
// stops diverging is a change to the note, not only to the count.
// Empty since A11 Q2: no step depends on the name. A step that starts to is
// a regression, and the probe reports it as partial.
var participantRecorded = []string{}

func probeFullParticipant(t *testing.T, e *env) verdict {
	diverged := participantDivergences(t, e)
	steps := len(partBattery())
	t.Logf("steps whose outcome depends on the dialect's name: %d of %d", len(diverged), steps)
	if len(diverged) == 0 {
		return present
	}
	if strings.Join(diverged, "\n") != strings.Join(participantRecorded, "\n") {
		t.Errorf("the diverging steps moved — measured:\n  %s\nrecorded:\n  %s\nupdate participantRecorded and DRV-04's note together",
			strings.Join(diverged, "\n  "), strings.Join(participantRecorded, "\n  "))
	}
	if len(diverged) == steps {
		return absent
	}
	return partial
}

// --- DRV-05 ------------------------------------------------------------------------

func probeKitChecksDialect(t *testing.T, e *env) verdict {
	api := e.loadAPI(t)
	dialect := api.lookup(rootPkg, "Dialect").Type()
	kit := api.pkgs[drivertestPkg]
	var takes []string
	for _, name := range kit.Scope().Names() {
		obj := kit.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}
		switch o := obj.(type) {
		case *types.TypeName:
			if st, ok := o.Type().Underlying().(*types.Struct); ok {
				for i := 0; i < st.NumFields(); i++ {
					if types.Implements(st.Field(i).Type(), dialect.Underlying().(*types.Interface)) || types.Identical(st.Field(i).Type(), dialect) {
						takes = append(takes, name+"."+st.Field(i).Name())
					}
				}
			}
		case *types.Func:
			sig := o.Type().(*types.Signature)
			for i := 0; i < sig.Params().Len(); i++ {
				if types.Identical(sig.Params().At(i).Type(), dialect) {
					takes = append(takes, name)
				}
			}
		}
	}
	t.Logf("places the conformance kit (%s) can be handed a Dialect: %v", drivertestPkg, takes)
	if len(takes) > 0 {
		t.Fatalf("the kit now takes a dialect (%v). Before this control can move, extend this probe to run the kit against a dialect that is wrong on purpose — placeholders, quoting, upsert, limit, savepoint — and record whether the kit catches each", takes)
	}
	return absent
}

// --- DRV-06 ------------------------------------------------------------------------

// inRepoDrivers are the driver modules this repository publishes.
var inRepoDrivers = []string{"mssql", "mysql", "oracle", "postgres", "sqlite"}

// kitSubtest is a subtest drivertest.Verify always runs; its presence in a
// module's test output means the kit ran there.
const kitSubtest = "recognises_its_own_unique_violation"

func probeEveryDriverRunsKit(t *testing.T, e *env) verdict {
	childProbe(t)
	var ran, didNot []string
	for _, d := range inRepoDrivers {
		dir := filepath.Join(e.root, "drivers", d)
		work := linkWorkspace(t, e, dir)
		out, err := goRun(t, dir, []string{"GOWORK=" + work}, "test", "-count=1", "-json", "./...")
		run := parseTestJSON(out)
		if len(run.action) == 0 {
			t.Fatalf("drivers/%s: no test ran (%v):\n%s", d, err, out)
		}
		kit := false
		for test, action := range run.action {
			if strings.HasSuffix(test, "/"+kitSubtest) && action == "pass" {
				kit = true
			}
		}
		if kit {
			ran = append(ran, d)
		} else {
			didNot = append(didNot, d)
		}
	}
	t.Logf("driver modules whose tests run drivertest.Verify: %v; whose tests do not: %v", ran, didNot)
	switch {
	case len(didNot) == 0:
		return present
	case len(ran) > 0:
		return partial
	default:
		return absent
	}
}

// linkWorkspace writes a go.work that builds the module in dir against THIS
// tree — what the CI driver lane does with scripts/ci/link_workspace.sh —
// and returns its path. The driver's requirement on the library names a
// published version, so the workspace replaces that exact version with the
// tree; without it the kit measured would be the released one.
func linkWorkspace(t *testing.T, e *env, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	version := ""
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == rootPkg {
				version = f[i+1]
			}
		}
	}
	if version == "" {
		t.Fatalf("%s/go.mod does not require %s", dir, rootPkg)
	}
	_, goVersion := rootRequire(t, e, "modernc.org/sqlite")
	work := filepath.Join(t.TempDir(), "go.work")
	content := fmt.Sprintf("go %s\n\nuse (\n\t%s\n\t%s\n)\n\nreplace %s %s => %s\n",
		goVersion, e.root, dir, rootPkg, version, e.root)
	if err := os.WriteFile(work, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return work
}

// --- DRV-07 ------------------------------------------------------------------------

func probeEngineSuiteReachable(t *testing.T, e *env) verdict {
	childProbe(t)
	// What an importer of the engine suite would get: the exported
	// declarations of its non-test files.
	suiteDir := filepath.Join(e.root, "internal", "enginesuite")
	pkg, err := build.ImportDir(suiteDir, 0)
	if err != nil {
		t.Fatalf("read the engine suite package: %v", err)
	}
	// Whether a module outside this repository may import an internal
	// package of quark at all: asked of the go command, from a copy of the
	// fixture of its own (the package it adds must not break DRV-03's build).
	dir := fixtureModule(t, e, "extdriver")
	probeDir := filepath.Join(dir, "reachinternal")
	if err := os.MkdirAll(probeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package reachinternal\n\nimport _ \"github.com/jcsvwinston/quark/internal/guard\"\n"
	if err := os.WriteFile(filepath.Join(probeDir, "reach.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, buildErr := goRun(t, dir, standalone, "build", "./reachinternal")
	refused := buildErr != nil && strings.Contains(out, "use of internal package")

	t.Logf("engine suite: import path %s; non-test files %v; test files %d (the suite lives in them)",
		"github.com/jcsvwinston/quark/internal/enginesuite", pkg.GoFiles, len(pkg.TestGoFiles))
	t.Logf("a module outside the repository importing an internal package of quark is refused by the go command: %v", refused)
	if !refused {
		t.Logf("go build output:\n%s", out)
	}
	exportsSuite := len(pkg.GoFiles) > 1 // doc.go alone exports nothing
	switch {
	case !refused && exportsSuite:
		return present
	case exportsSuite:
		return partial
	default:
		return absent
	}
}

// --- DRV-08 ------------------------------------------------------------------------

// knownModules are the modules of this repository that are not a driver
// template, by their directory.
var knownModules = map[string]bool{
	".": true, "acceptance": true, "benchmarks": true, "bugbash": true,
	"cmd/quark": true, "internal/enginesuite": true, "internal/integrations": true,
	"drivers/mssql": true, "drivers/mysql": true, "drivers/oracle": true,
	"drivers/postgres": true, "drivers/sqlite": true,
}

// repoModules returns the directory (relative to the root) of every go.mod in
// the repository, outside node_modules and testdata.
func repoModules(t *testing.T, e *env) []string {
	t.Helper()
	var mods []string
	err := filepath.WalkDir(e.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "go.mod" {
			rel, _ := filepath.Rel(e.root, filepath.Dir(path))
			mods = append(mods, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	sort.Strings(mods)
	return mods
}

func probeDriverTemplate(t *testing.T, e *env) verdict {
	childProbe(t)
	mods := repoModules(t, e)
	var candidates []string
	for _, m := range mods {
		if knownModules[m] {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(e.root, m, "go.mod"))
		if err == nil && strings.Contains(string(raw), rootPkg+" ") {
			candidates = append(candidates, m)
		}
	}
	t.Logf("modules in the repository: %d %v; modules requiring the library that are none of the known ones: %v", len(mods), mods, candidates)

	passing := 0
	for _, m := range candidates {
		dir := filepath.Join(e.root, m)
		out, err := goRun(t, dir, []string{"GOWORK=off"}, "test", "-count=1", "-json", "./...")
		run := parseTestJSON(out)
		kit := false
		for test, action := range run.action {
			if strings.HasSuffix(test, "/"+kitSubtest) && action == "pass" {
				kit = true
			}
		}
		t.Logf("%s standalone: err=%v, the kit ran: %v", m, err, kit)
		if err == nil && kit {
			passing++
		}
	}
	switch {
	case passing > 0:
		return present
	case len(candidates) > 0:
		return partial
	default:
		return absent
	}
}
