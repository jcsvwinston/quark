// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeConsumerModule writes a go.mod and go.sum for a throwaway project that
// embeds this CLI's command tree, pointed at the tree under test.
//
// It takes seven replace directives rather than one because the CLI is its
// own module since ADR-0024, and so is each driver it links. A real project
// needs none of them — it requires the published modules — but a test has to
// build against the tree it is testing, and every requirement that resolves
// from the proxy instead would compile the PREVIOUS release into the binary
// this test then runs.
func writeConsumerModule(t *testing.T, dir, moduleName string) {
	t.Helper()

	// Derived from this file's own path, not from the working directory:
	// several tests in this package chdir into a scaffolded project and the
	// helper has to answer the same thing whoever calls it.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	cliRoot := filepath.Dir(filepath.Dir(thisFile))
	repoRoot := filepath.Dir(filepath.Dir(cliRoot))

	var b strings.Builder
	fmt.Fprintf(&b, "module %s\n\ngo 1.25.7\n\n", moduleName)
	fmt.Fprintf(&b, "require (\n\tgithub.com/jcsvwinston/quark v0.0.0\n\tgithub.com/jcsvwinston/quark/cmd/quark v0.0.0\n)\n\n")
	fmt.Fprintf(&b, "replace github.com/jcsvwinston/quark => %s\n\n", repoRoot)
	fmt.Fprintf(&b, "replace github.com/jcsvwinston/quark/cmd/quark => %s\n\n", cliRoot)
	for _, engine := range []string{"mssql", "mysql", "oracle", "postgres", "sqlite"} {
		fmt.Fprintf(&b, "replace github.com/jcsvwinston/quark/drivers/%s => %s\n\n",
			engine, filepath.Join(repoRoot, "drivers", engine))
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Both sums: the library's for what it requires, the CLI's for cobra and
	// the engine libraries the drivers pull in. Duplicate lines are harmless
	// — go.sum is a set.
	var sums []byte
	for _, p := range []string{filepath.Join(repoRoot, "go.sum"), filepath.Join(cliRoot, "go.sum")} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, b...)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sums, 0o644); err != nil {
		t.Fatal(err)
	}
}
