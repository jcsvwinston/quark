// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for cases A4/A5 of the DX audit (2026-08-16) — the two
// exit-0-sin-efecto cases no backlog item covered:
//
//   - A4: quark.New(driver, dsn, opts ...any) applied PoolOption/Option
//     values and silently discarded everything else, so a string, a number,
//     or an option constructor passed uncalled all "worked" while doing
//     nothing.
//   - A5: an unknown driver name without an explicit WithDialect logged a
//     WARN and silently fell back to the PostgreSQL dialect, emitting SQL
//     for the wrong engine from then on.
package quark_test

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	sqlite "modernc.org/sqlite"
)

// A4: every variadic option must be a known quark option kind; anything else
// must fail naming the offending value, not vanish in silence.
func TestNewRejectsInvalidOptions(t *testing.T) {
	_, err := quark.New("sqlite", ":memory:",
		"WithMaxOpenConns(25)", // a string that LOOKS like an option
		42,                     // a number
		quark.WithMaxOpenConns, // the constructor itself, not its result
	)
	if err == nil {
		t.Fatal("New accepted three invalid options in silence")
	}
	for _, frag := range []string{"string", "int", "func("} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("error should name the offending option type %q, got: %v", frag, err)
		}
	}
}

// Valid options must keep working exactly as before.
func TestNewAcceptsValidOptions(t *testing.T) {
	c, err := quark.New("sqlite", ":memory:",
		quark.WithMaxOpenConns(5),
		quark.WithDialect(quark.SQLite()),
	)
	if err != nil {
		t.Fatalf("valid options must not error: %v", err)
	}
	defer c.Close()
}

// A5: unknown driver without WithDialect must be an error, not a silent
// PostgreSQL fallback.
func TestNewRejectsUnknownDriverWithoutDialect(t *testing.T) {
	// A registered database/sql driver whose name quark does not know —
	// the situation of any third-party driver (clickhouse, duckdb, ...).
	sql.Register("weirddb", &sqlite.Driver{})

	_, err := quark.New("weirddb", ":memory:")
	if err == nil {
		t.Fatal("unknown driver without WithDialect must error, not default to PostgreSQL")
	}
	if !errors.Is(err, quark.ErrDialectNotSupported) {
		t.Errorf("want ErrDialectNotSupported, got: %v", err)
	}
	if !strings.Contains(err.Error(), "WithDialect") {
		t.Errorf("error should point at quark.WithDialect as the fix, got: %v", err)
	}

	// The escape hatch keeps working: an explicit dialect makes the same
	// driver usable.
	c, err := quark.New("weirddb", ":memory:", quark.WithDialect(quark.SQLite()))
	if err != nil {
		t.Fatalf("unknown driver WITH WithDialect must work: %v", err)
	}
	defer c.Close()
}
