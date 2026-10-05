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

// kitMutation is a dialect that is wrong on purpose: a patch to the fixture
// driver's dialect source and the kit subtest that must fail for it, by
// name. The patch replaces one line the fixture is known to have; a fixture
// that no longer has it stops the probe instead of measuring nothing.
type kitMutation struct {
	name   string // what is wrong
	find   string // a line of dialect/dialect.go
	patch  string // what it becomes
	caught string // the kit subtest that must fail
}

var kitMutations = []kitMutation{
	{
		"placeholders that all bind the first value",
		`func (Dialect) Placeholder(int) string     { return "?" }`,
		`func (Dialect) Placeholder(int) string     { return "?1" }`,
		"engine/Placeholder",
	},
	{
		"quoting that does not escape the quote character",
		"return `\"` + strings.ReplaceAll(identifier, `\"`, `\"\"`) + `\"`",
		"return `\"` + identifier + `\"`",
		"engine/Quote",
	},
	{
		"an upsert that ignores the columns to update",
		`return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s", conflict, strings.Join(sets, ", "))`,
		`return fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", conflict)`,
		"engine/UpsertSQL",
	},
	{
		"LIMIT and OFFSET swapped",
		`return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)`,
		`return fmt.Sprintf("LIMIT %d OFFSET %d", offset, limit)`,
		"engine/LimitOffset",
	},
	{
		"a SavepointDialect whose rollback releases instead",
		`func (Dialect) CurrentTimestamp() string   { return "CURRENT_TIMESTAMP" }`,
		`func (Dialect) CurrentTimestamp() string   { return "CURRENT_TIMESTAMP" }
func (Dialect) SavepointStmt(n string) string           { return "SAVEPOINT " + n }
func (Dialect) RollbackToSavepointStmt(n string) string { return "RELEASE SAVEPOINT " + n }
func (Dialect) ReleaseSavepointStmt(n string) string    { return "RELEASE SAVEPOINT " + n }`,
		"engine/SavepointDialect",
	},
	{
		"an AutoIncrementer whose key the engine does not number",
		`return "INTEGER PRIMARY KEY AUTOINCREMENT", "INTEGER"`,
		`return "BIGINT PRIMARY KEY", "INTEGER"`,
		"engine/AutoIncrementer",
	},
}

// kitRun runs the fixture's dialect kit test in dir and returns the leaf
// subtests that failed and whether the engine half ran.
func kitRun(t *testing.T, dir string) (failed []string, engineRan bool, out string) {
	t.Helper()
	out, _ = goRun(t, dir, standalone, "test", "-count=1", "-json", "-run", "^TestDialectConformance$", ".")
	run := parseTestJSON(out)
	if len(run.action) == 0 {
		t.Fatalf("the fixture's dialect kit test did not run:\n%s", out)
	}
	for test, action := range run.action {
		if test == "TestDialectConformance/engine/Placeholder" && action == "pass" {
			engineRan = true
		}
		if action != "fail" {
			continue
		}
		leaf := true
		for other, a := range run.action {
			if a == "fail" && strings.HasPrefix(other, test+"/") {
				leaf = false
			}
		}
		if leaf {
			failed = append(failed, strings.TrimPrefix(test, "TestDialectConformance/"))
		}
	}
	sort.Strings(failed)
	return failed, engineRan, out
}

func probeKitChecksDialect(t *testing.T, e *env) verdict {
	childProbe(t)
	// The kit takes a dialect: a field of DialectCase holds one.
	api := e.loadAPI(t)
	caseType, ok := api.lookup(drivertestPkg, "DialectCase").(*types.TypeName)
	if !ok {
		t.Logf("drivertest has no DialectCase: the kit takes no dialect")
		return absent
	}
	dialect := api.lookup(rootPkg, "Dialect").Type()
	takes := false
	st := caseType.Type().Underlying().(*types.Struct)
	for i := 0; i < st.NumFields(); i++ {
		if types.Identical(st.Field(i).Type(), dialect) {
			takes = true
		}
	}
	if !takes {
		t.Logf("drivertest.DialectCase has no field of type Dialect")
		return absent
	}

	// The right dialect passes, with the engine half run.
	failed, engineRan, out := kitRun(t, extdriverModule(t, e))
	t.Logf("the fixture's own dialect: kit failures %v, engine half ran: %v", failed, engineRan)
	if len(failed) > 0 || !engineRan {
		t.Logf("output:\n%s", out)
		return absent
	}

	// Each wrong dialect fails, and the kit names the method.
	src := filepath.Join(e.root, "internal", "extbench", "testdata", "extdriver", "dialect", "dialect.go")
	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		m      kitMutation
		failed []string
	}
	results := make([]result, len(kitMutations))
	var wg sync.WaitGroup
	for i, m := range kitMutations {
		if !strings.Contains(string(orig), m.find) {
			t.Fatalf("mutation %q: the fixture's dialect no longer has the line it patches:\n%s", m.name, m.find)
		}
		dir := fixtureModule(t, e, "extdriver")
		patched := strings.Replace(string(orig), m.find, m.patch, 1)
		if err := os.WriteFile(filepath.Join(dir, "dialect", "dialect.go"), []byte(patched), 0o644); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(i int, m kitMutation, dir string) {
			defer wg.Done()
			f, _, _ := kitRun(t, dir)
			results[i] = result{m, f}
		}(i, m, dir)
	}
	wg.Wait()
	caught := 0
	for _, r := range results {
		hit := false
		for _, f := range r.failed {
			if f == r.m.caught || strings.HasPrefix(f, r.m.caught+"/") {
				hit = true
			}
		}
		t.Logf("wrong on purpose — %s: caught by %s: %v (%d failing subtests)", r.m.name, r.m.caught, hit, len(r.failed))
		if hit {
			caught++
		}
	}
	switch caught {
	case len(kitMutations):
		return present
	case 0:
		return absent
	default:
		return partial
	}
}

// --- DRV-06 ------------------------------------------------------------------------

// inRepoDrivers are the driver modules this repository publishes.
var inRepoDrivers = []string{"mssql", "mysql", "oracle", "postgres", "sqlite"}

// kitSubtest is a subtest drivertest.Verify always runs; its presence in a
// module's test output means the classifier kit ran there.
const kitSubtest = "recognises_its_own_unique_violation"

// dialectKitSubtest is a subtest drivertest.VerifyDialect always runs, with
// or without a database.
const dialectKitSubtest = "contract/Placeholder"

func probeEveryDriverRunsKit(t *testing.T, e *env) verdict {
	childProbe(t)
	var full, partialRun []string
	for _, d := range inRepoDrivers {
		dir := filepath.Join(e.root, "drivers", d)
		work := linkWorkspace(t, e, dir)
		out, err := goRun(t, dir, []string{"GOWORK=" + work}, "test", "-count=1", "-json", "./...")
		run := parseTestJSON(out)
		if len(run.action) == 0 {
			t.Fatalf("drivers/%s: no test ran (%v):\n%s", d, err, out)
		}
		classifier := quarkdriverHas(t, run, kitSubtest)
		dialectKit, engineRan, engineSkipped := false, 0, 0
		for test, action := range run.action {
			if strings.HasSuffix(test, "/"+dialectKitSubtest) && action == "pass" {
				dialectKit = true
			}
			if strings.HasSuffix(test, "/engine") || test == "TestDialectConformance/engine" {
				switch action {
				case "pass":
					engineRan++
				case "skip":
					engineSkipped++
				}
			}
		}
		// postgres registers no classifier by design (CON-07): its kit is
		// the dialect's.
		wantClassifier := d != "postgres"
		t.Logf("drivers/%s: classifier kit ran: %v (expected: %v); dialect kit ran: %v; its engine half ran against %d database(s) and was skipped for want of one %d time(s)",
			d, classifier, wantClassifier, dialectKit, engineRan, engineSkipped)
		if dialectKit && (classifier || !wantClassifier) {
			full = append(full, d)
		} else if dialectKit || classifier {
			partialRun = append(partialRun, d)
		}
	}
	t.Logf("driver modules that run the kit — the classifier half where they register a classifier, the dialect half always: %v; that run part of it: %v", full, partialRun)
	switch {
	case len(full) == len(inRepoDrivers):
		return present
	case len(full)+len(partialRun) > 0:
		return partial
	default:
		return absent
	}
}

// quarkdriverHas reports whether a subtest with the given suffix passed.
func quarkdriverHas(t *testing.T, run testRun, suffix string) bool {
	t.Helper()
	for test, action := range run.action {
		if strings.HasSuffix(test, "/"+suffix) && action == "pass" {
			return true
		}
	}
	return false
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

const suitePkg = "github.com/jcsvwinston/quark/quarkdriver/drivertest/suite"

// heavy are import paths the public suite must not bring into a driver
// module's graph: a container library, an engine's driver, a cache or
// tracing backend.
var heavy = []string{"testcontainers", "jackc/pgx", "go-sql-driver", "go-mssqldb", "go-ora", "mattn/go-sqlite3", "modernc.org/sqlite", "redis", "opentelemetry"}

// suiteSubtests returns the direct subtests of parent that passed.
func suiteSubtests(run testRun, parent string) (passed, other []string) {
	for test, action := range run.action {
		rest, ok := strings.CutPrefix(test, parent+"/")
		if !ok || strings.Contains(rest, "/") {
			continue
		}
		if action == "pass" {
			passed = append(passed, rest)
		} else {
			other = append(other, rest+":"+action)
		}
	}
	sort.Strings(passed)
	sort.Strings(other)
	return passed, other
}

func probeEngineSuiteReachable(t *testing.T, e *env) verdict {
	childProbe(t)
	dir := extdriverModule(t, e)

	// 1. A module outside the repository imports the suite, and what it
	// brings is Quark and the standard library.
	out, err := goRun(t, dir, standalone, "list", "-deps", suitePkg)
	if err != nil {
		t.Logf("go list -deps %s from example.com/extdriver: %v\n%s", suitePkg, err, out)
		return absent
	}
	graph := strings.Fields(out)
	var dragged []string
	for _, p := range graph {
		for _, h := range heavy {
			if strings.Contains(p, h) {
				dragged = append(dragged, p)
			}
		}
	}
	lib := deps(t, dir, standalone, rootPkg)
	t.Logf("%s from a module outside the repository (GOWORK=off): %d packages (the library alone: %d); container, driver, cache or tracing packages among them: %v",
		suitePkg, len(graph), len(lib), dragged)

	// 2. The fixture driver runs it, standalone.
	out, _ = goRun(t, dir, standalone, "test", "-count=1", "-json", "-run", "^TestEngineSuite$", ".")
	fixture, fixtureOther := suiteSubtests(parseTestJSON(out), "TestEngineSuite")
	t.Logf("example.com/extdriver ran the suite: %d subtests passed, others %v", len(fixture), fixtureOther)

	// 3. The in-repo engines run the same suite: internal/enginesuite's
	// SQLite lane, the one this bench can run without a server.
	suiteDir := filepath.Join(e.root, "internal", "enginesuite")
	out, _ = goRun(t, suiteDir, []string{"GOWORK=off"}, "test", "-count=1", "-json", "-run", "^TestSuiteSQLite$", ".")
	inRepoRun := parseTestJSON(out)
	inRepo, inRepoOther := suiteSubtests(inRepoRun, "TestSuiteSQLite/EngineSuite")
	shared, _ := suiteSubtests(inRepoRun, "TestSuiteSQLite")
	internalOnly := 0
	for _, s := range shared {
		if s != "EngineSuite" && s != "DialectKit" {
			internalOnly++
		}
	}
	same := strings.Join(fixture, ",") == strings.Join(inRepo, ",")
	t.Logf("internal/enginesuite's TestSuiteSQLite ran the same suite: %d subtests passed (others %v), identical to the fixture's: %v; %d subtests of the shared suite stay internal (engine-specific)",
		len(inRepo), inRepoOther, same, internalOnly)

	switch {
	case len(dragged) == 0 && len(fixture) > 0 && len(fixtureOther) == 0 && same:
		return present
	case len(fixture) > 0 || len(inRepo) > 0:
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
