// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// `quark init --with <framework>` writes the code of a tested fixture of
// internal/integrations into a project. These tests hold the three things
// together: the copies the CLI embeds are the fixtures
// (TestInitWithTemplatesAreTheFixtures), the versions go.mod is given are
// ones the fixtures are tested at (TestInitWithPinsAreTheFixtures), and what
// init writes builds, vets and tests in a project of its own against this
// tree (TestInitWithBuilds, the test the extension bench runs for INT-06).
package commands

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// treeRoots returns the CLI module's directory and the repository root,
// from this file's path: several tests of this package chdir.
func treeRoots(t *testing.T) (cliRoot, repoRoot string) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	cliRoot = filepath.Dir(filepath.Dir(thisFile))
	return cliRoot, filepath.Dir(filepath.Dir(cliRoot))
}

// fixturesDir is internal/integrations, or a skip when this package is not
// tested from a checkout of the repository.
func fixturesDir(t *testing.T) string {
	t.Helper()
	_, repo := treeRoots(t)
	dir := filepath.Join(repo, "internal", "integrations")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Skipf("no internal/integrations beside this module (%v): these tests compare the CLI with the repository it is part of", err)
	}
	return dir
}

// fixtureSources lists the files of the fixtures init copies, relative to
// internal/integrations: everything under notes/ and under each target's
// package except tests.
func fixtureSources(t *testing.T, fixtures string) []string {
	t.Helper()
	var out []string
	dirs := []string{"notes"}
	for _, in := range integrations {
		dirs = append(dirs, in.fixture)
	}
	for _, d := range dirs {
		err := filepath.WalkDir(filepath.Join(fixtures, d), func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || strings.HasSuffix(p, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(fixtures, p)
			out = append(out, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(out)
	return out
}

func TestInitWithTargetsAreTheIntegrations(t *testing.T) {
	var targets []string
	for _, in := range integrations {
		targets = append(targets, in.target)
	}
	if !slices.Equal(targets, initWithTargets) {
		t.Fatalf("initWithTargets is %v and integrations lists %v: the flag's validation and the bench read the first, init writes the second", initWithTargets, targets)
	}
}

// The copies are the fixtures, byte for byte. With QUARK_SYNC_TEMPLATES set
// the test writes them instead (`make regen`).
func TestInitWithTemplatesAreTheFixtures(t *testing.T) {
	fixtures := fixturesDir(t)
	cliRoot, _ := treeRoots(t)
	want := fixtureSources(t, fixtures)

	if os.Getenv("QUARK_SYNC_TEMPLATES") != "" {
		dst := filepath.Join(cliRoot, "commands", filepath.FromSlash(fixtureRoot))
		if err := os.RemoveAll(dst); err != nil {
			t.Fatal(err)
		}
		for _, rel := range want {
			src, err := os.ReadFile(filepath.Join(fixtures, rel))
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dst, filepath.FromSlash(rel)+".tmpl")
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(out, src, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("wrote %d copies of internal/integrations under commands/%s", len(want), fixtureRoot)
		return
	}

	var got []string
	err := fs.WalkDir(integrationFixtures, fixtureRoot, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		got = append(got, strings.TrimSuffix(strings.TrimPrefix(p, fixtureRoot+"/"), ".tmpl"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const regen = "run `make regen` (or QUARK_SYNC_TEMPLATES=1 go test ./commands -run TestInitWithTemplatesAreTheFixtures in cmd/quark)"
	if !slices.Equal(got, want) {
		t.Fatalf("the CLI embeds copies of %v and the fixtures init copies are %v: %s", got, want, regen)
	}
	for _, rel := range want {
		fixture, err := os.ReadFile(filepath.Join(fixtures, rel))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := integrationFixtures.ReadFile(path.Join(fixtureRoot, rel+".tmpl"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(fixture, copied) {
			t.Errorf("commands/%s/%s.tmpl is not internal/integrations/%s: %s", fixtureRoot, rel, rel, regen)
		}
	}
}

// A framework version init writes into go.mod is one the fixtures require
// directly, at that version or a newer one: never a version CI has not
// tested. An older one is allowed so that a dependency update of the
// fixtures' module does not fail here; TestInitWithBuilds proves the
// scaffold builds at the pinned one.
func TestInitWithPinsAreTheFixtures(t *testing.T) {
	fixtures := fixturesDir(t)
	raw, err := os.ReadFile(filepath.Join(fixtures, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	direct := map[string]string{}
	for _, r := range f.Require {
		if !r.Indirect {
			direct[r.Mod.Path] = r.Mod.Version
		}
	}
	for _, in := range integrations {
		for _, pin := range in.requires {
			tested, ok := direct[pin.Path]
			switch {
			case !ok:
				t.Errorf("--with %s requires %s, which internal/integrations does not require directly: no fixture is tested against it", in.target, pin.Path)
			case semver.Compare(pin.Version, tested) > 0:
				t.Errorf("--with %s pins %s %s, newer than the %s the fixtures are tested at", in.target, pin.Path, pin.Version, tested)
			}
		}
	}
}

// scaffoldDialects gives each target a different dialect, so the five
// builds compile the driver rewrite for every engine module.
var scaffoldDialects = map[string]string{
	"chi": "postgresql", "echo": "mysql", "gin": "mssql", "grpc": "oracle", "nucleus": "sqlite",
}

// The versions TestInitWithBuilds stamps, as a release does (.goreleaser.yaml).
// The replaces below resolve any version from this tree; these let the test
// check that go.mod requires what the binary carries.
const (
	stampedCLIVersion     = "v1.1.2"
	stampedLibraryVersion = "v1.15.2"
)

// TestInitWithBuilds runs `quark init --with <target>` for every target into
// a directory of its own, points every Quark module the project requires at
// this tree with a replace, and runs what a user runs next — go mod tidy —
// then go build, go vet and go test over the whole project, with no
// workspace. It is what makes the scaffold a build CI checks rather than
// text: the extension bench runs it and counts a target only when its
// subtest passes (INT-06).
func TestInitWithBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a scaffolded project per framework with the go command; skipped with -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("the go command is not on PATH, and this test is a build: %v", err)
	}
	cliRoot, repo := treeRoots(t)
	oldVersion, oldLibrary := version, libraryVersion
	version, libraryVersion = stampedCLIVersion, stampedLibraryVersion
	t.Cleanup(func() { version, libraryVersion = oldVersion, oldLibrary })

	for _, in := range integrations {
		t.Run(in.target, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "shop")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			out, err := runInitWith(t, dir, in.target, scaffoldDialects[in.target])
			if err != nil {
				t.Fatalf("quark init --with %s: %v\n%s", in.target, err, out)
			}

			// init created go.mod and required the framework and the two
			// Quark modules at the versions the binary carries.
			gomod := readGoMod(t, dir)
			want := append([]module.Version{
				{Path: libraryModule, Version: stampedLibraryVersion},
				{Path: cliModule, Version: stampedCLIVersion},
			}, in.requires...)
			for _, m := range want {
				if got := gomod[m.Path]; got != m.Version {
					t.Errorf("go.mod requires %s at %q, want %s", m.Path, got, m.Version)
				}
			}

			pointAtTree(t, dir, cliRoot, repo)
			if in.target == "grpc" {
				writeDescriptorCheck(t, dir)
			}

			t.Parallel()
			for _, args := range [][]string{
				{"mod", "tidy"},
				{"build", "./..."},
				{"vet", "./..."},
				{"test", "-count=1", "./..."},
			} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s in the project `quark init --with %s` wrote: %v\n%s", strings.Join(args, " "), in.target, err, out)
				}
			}
		})
	}
}

// runInitWith runs init the way the command line does, without cobra: the
// subtests share the flag variables, and a string-slice flag parsed twice
// appends.
func runInitWith(t *testing.T, dir, target, dialect string) (string, error) {
	t.Helper()
	oldDir, oldDialect, oldModule, oldWith := initDir, initDialect, initModule, initWith
	defer func() { initDir, initDialect, initModule, initWith = oldDir, oldDialect, oldModule, oldWith }()
	initDir, initDialect, initModule, initWith = dir, dialect, "example.com/shop", []string{target}
	return captureStdout(t, runInit)
}

func readGoMod(t *testing.T, dir string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatalf("init did not create go.mod: %v", err)
	}
	f, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		t.Fatalf("init wrote a go.mod the go command cannot read: %v\n%s", err, raw)
	}
	out := map[string]string{}
	for _, r := range f.Require {
		out[r.Mod.Path] = r.Mod.Version
	}
	return out
}

// pointAtTree replaces every Quark module the project can reach — the
// library, this CLI (the runner embeds it) and the five drivers (the CLI
// links them all) — with this tree, and seeds go.sum with the sums the
// tree's modules already carry, so tidy does not ask the checksum database
// for what this repository has checked.
func pointAtTree(t *testing.T, dir, cliRoot, repo string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("replace " + libraryModule + " => " + repo + "\n")
	b.WriteString("replace " + cliModule + " => " + cliRoot + "\n")
	sums := []string{filepath.Join(repo, "go.sum"), filepath.Join(cliRoot, "go.sum"), filepath.Join(repo, "internal", "integrations", "go.sum")}
	for _, engine := range []string{"mssql", "mysql", "oracle", "postgres", "sqlite"} {
		d := filepath.Join(repo, "drivers", engine)
		b.WriteString("replace " + libraryModule + "/drivers/" + engine + " => " + d + "\n")
		sums = append(sums, filepath.Join(d, "go.sum"))
	}
	f, err := os.OpenFile(filepath.Join(dir, "go.mod"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	var all []byte
	for _, p := range sums {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, raw...)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), all, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeDescriptorCheck adds one test to the gRPC scaffold. init re-encodes
// the descriptor protoc-gen-go embeds (rewriteRawDescGoPackage), and a
// descriptor with a wrong length still compiles: only the protobuf runtime,
// at init, refuses it. This test makes `go test` load it and read the
// go_package back.
func writeDescriptorCheck(t *testing.T, dir string) {
	t.Helper()
	const check = `package notespb

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

func TestDescriptorNamesThisPackage(t *testing.T) {
	opts := File_notes_proto.Options().(*descriptorpb.FileOptions)
	if got, want := opts.GetGoPackage(), "example.com/shop/internal/shop/notespb"; got != want {
		t.Fatalf("the embedded descriptor's go_package is %q, want %q", got, want)
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "internal", "shop", "notespb", "descriptor_check_test.go"), []byte(check), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Re-encoding the descriptor for the path it already names gives back the
// file protoc-gen-go wrote, byte for byte — the constant is written the way
// protoc-gen-go writes it — and a path long enough to need two-byte lengths
// reads back whole, with every other field of the descriptor unchanged.
func TestRawDescGoPackage(t *testing.T) {
	src, err := integrationFixtures.ReadFile(path.Join(fixtureRoot, "withgrpc", "notespb", "notes.pb.go.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	same, err := rewriteRawDescGoPackage(src, fixtureModule+"/withgrpc/notespb")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(same, src) {
		t.Fatalf("re-encoding the descriptor with the go_package it has changed the file:\n%s", same)
	}

	long := "example.com/" + strings.Repeat("a", 150) + "/notespb"
	out, err := rewriteRawDescGoPackage(src, long)
	if err != nil {
		t.Fatal(err)
	}
	_, _, before, err := rawDesc(src)
	if err != nil {
		t.Fatal(err)
	}
	_, _, after, err := rawDesc(out)
	if err != nil {
		t.Fatalf("the rewritten constant does not decode: %v", err)
	}
	if got := goPackageOf(t, after); got != long {
		t.Fatalf("go_package reads back %q, want %q", got, long)
	}
	// Every field but the options is the same bytes, in the same order.
	strip := func(desc []byte) []byte {
		fields, err := wireFields(desc)
		if err != nil {
			t.Fatal(err)
		}
		var rest []byte
		for _, f := range fields {
			if f.num != fileOptionsField {
				rest = append(rest, f.raw...)
			}
		}
		return rest
	}
	if !bytes.Equal(strip(before), strip(after)) {
		t.Fatal("re-encoding go_package changed a field of the descriptor other than its options")
	}
}

func goPackageOf(t *testing.T, desc []byte) string {
	t.Helper()
	fields, err := wireFields(desc)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fields {
		if f.num != fileOptionsField {
			continue
		}
		opts, err := wireFields(f.payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range opts {
			if o.num == goPackageField {
				return string(o.payload)
			}
		}
	}
	return ""
}

// A go.mod that exists keeps every requirement it has, and init adds the
// ones it lacks. A development build has no release of the Quark modules to
// pin: it leaves them to `go mod tidy` and says so.
func TestInitWithKeepsRequirementsAndPinsOnlyReleases(t *testing.T) {
	dir := t.TempDir()
	gomod := "module example.com/shop\n\ngo 1.25\n\nrequire github.com/go-chi/chi/v5 v5.0.0\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldLibrary := version, libraryVersion
	version, libraryVersion = "devel", "devel"
	t.Cleanup(func() { version, libraryVersion = oldVersion, oldLibrary })

	out, err := runInitArgs(t, "--dir", dir, "--dialect", "sqlite", "--with", "chi")
	if err != nil {
		t.Fatalf("init --with chi: %v", err)
	}
	got := readGoMod(t, dir)
	if got["github.com/go-chi/chi/v5"] != "v5.0.0" {
		t.Errorf("init changed the chi requirement the project had: %v", got)
	}
	for _, m := range []string{libraryModule, cliModule} {
		if v, ok := got[m]; ok {
			t.Errorf("a development build pinned %s at %s", m, v)
		}
	}
	for _, want := range []string{
		"Kept in go.mod at the version it already required: github.com/go-chi/chi/v5 v5.0.0",
		"Left to go mod tidy: github.com/jcsvwinston/quark and github.com/jcsvwinston/quark/cmd/quark",
		"go mod tidy",
		"go run ./cmd/shop-server",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init's output misses %q:\n%s", want, out)
		}
	}

	for v, pin := range map[string]bool{
		"v1.1.2": true, "v1.2.0-rc.1": true,
		"devel": false, "(devel)": false, "v1.1.3-0.20261005120000-abcdefabcdef": false, "v1.1.2+dirty": false,
	} {
		if got := releaseVersion(module.Version{Path: cliModule, Version: v}); got != pin {
			t.Errorf("releaseVersion(%q) = %v, want %v", v, got, pin)
		}
	}
}

// internal/<app> takes a package name the scaffold's own files do not use.
func TestPackageIdentAvoidsTakenNames(t *testing.T) {
	for name, want := range map[string]string{
		"my-shop.api": "myshopapi",
		"quark":       "quarkapp",
		"notes":       "notesapp",
		"main":        "mainapp",
		"go":          "goapp",
		"http":        "httpapp",
		"9lives":      "lives",
		"---":         "app",
	} {
		if got := packageIdent(name); got != want {
			t.Errorf("packageIdent(%q) = %q, want %q", name, got, want)
		}
	}
}
