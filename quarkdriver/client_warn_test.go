// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver_test

// This file imports package quark, so it lives in the external test package:
// quarkdriver itself stays a leaf. It is here rather than in package quark
// because quark's own test binary registers every classifier
// (zz_drivers_for_test.go), and the point of this test is a binary in which
// none is — the shape of an application that imported the bare driver.

import (
	"bytes"
	"database/sql"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// quark.New with a driver registered under the caller's name (lib/pq's
// "postgres", mattn's "sqlite3") must reach sql.Open — the v1.10.0 regression
// refused it before opening — and, with no classifier registered for the
// engine, must say so on the caller's logger instead of failing silently.
func TestNewOpensAliasedDriverAndWarnsWithoutClassifier(t *testing.T) {
	// The fakes are registered by package quarkdriver's own tests, which
	// share this binary and initialise first.
	for _, n := range []string{"postgres", "sqlite3"} {
		if !quarkdriver.IsRegistered(n) {
			t.Fatalf("test binary has no fake %q driver (linked: %v)", n, sql.Drivers())
		}
	}
	if quarkdriver.HasEngine("sqlite") {
		t.Fatal("this binary must not register a sqlite classifier — the test models an application that imported the bare driver")
	}

	t.Run("sqlite3 opens and warns", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		client, err := quark.New("sqlite3", "fake", quark.WithLogger(logger))
		if err != nil {
			t.Fatalf("quark.New(\"sqlite3\") with a linked sqlite3 driver: %v", err)
		}
		defer client.Close()
		out := buf.String()
		for _, want := range []string{"level=WARN", "quark.driver.no_classifier", "dialect=sqlite", "drivers/sqlite"} {
			if !strings.Contains(out, want) {
				t.Errorf("no-classifier WARN is missing %q; log:\n%s", want, out)
			}
		}
	})

	t.Run("postgres opens and stays quiet", func(t *testing.T) {
		// PostgreSQL needs no classifier: its SQLSTATE is read through a
		// method every PostgreSQL driver exposes, so warning here would be
		// noise on every lib/pq deployment.
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		client, err := quark.New("postgres", "fake", quark.WithLogger(logger))
		if err != nil {
			t.Fatalf("quark.New(\"postgres\") with a linked postgres driver: %v", err)
		}
		defer client.Close()
		if out := buf.String(); strings.Contains(out, "quark.driver.no_classifier") {
			t.Errorf("PostgreSQL must not warn about a missing classifier; log:\n%s", out)
		}
	})
}
