// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"
)

// The hint's whole value is that it can be followed literally, so every piece
// a person would copy has to be there.
func TestMissingDriverHintCarriesTheWholeRecipe(t *testing.T) {
	for _, name := range []string{"pgx", "postgres", "postgresql", "mariadb", "mssql", "sqlite3", "SQLite"} {
		hint := MissingDriverHint(name)
		if hint == "" {
			t.Errorf("%q produced no hint; a name Quark's dialect layer accepts must resolve", name)
			continue
		}
		for _, want := range []string{"go get github.com/jcsvwinston/quark/drivers/", "import _ \"github.com/jcsvwinston/quark/drivers/"} {
			if !strings.Contains(hint, want) {
				t.Errorf("%q: hint is missing %q:\n%s", name, want, hint)
			}
		}
	}
}

// Inventing a `go get` for a driver nobody publishes would send the reader
// somewhere that does not exist — worse than the plain error it replaces.
func TestMissingDriverHintStaysSilentForForeignDrivers(t *testing.T) {
	for _, name := range []string{"clickhouse", "mongodb", "", "sqlit"} {
		if hint := MissingDriverHint(name); hint != "" {
			t.Errorf("%q must produce no hint, got:\n%s", name, hint)
		}
	}
}

// The aliases exist because nobody types the database/sql name: `quark init`
// writes "postgresql", the docs say "postgres", and the driver is "pgx".
func TestAliasesResolveToTheSameModule(t *testing.T) {
	want := "drivers/postgres"
	for _, name := range []string{"pgx", "postgres", "postgresql", "pq", "pgx/v5"} {
		if !strings.Contains(MissingDriverHint(name), want) {
			t.Errorf("%q did not resolve to %s", name, want)
		}
	}
	if !strings.Contains(MissingDriverHint("mariadb"), "drivers/mysql") {
		t.Error("mariadb speaks the MySQL wire protocol and must resolve to the mysql module")
	}
}

// fakeDriver stands in for lib/pq and mattn/go-sqlite3: drivers that register
// under the name people pass to quark.New ("postgres", "sqlite3") rather
// than under the name the modules this project publishes use ("pgx",
// "sqlite"). Its connections do nothing, which is all sql.Open and Ping need.
type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return fakeConn{}, nil }

type fakeConn struct{}

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake driver") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake driver") }

// registerFakeDrivers links the fakes once per test binary. sql.Register
// panics on a duplicate name, and the external test package shares this
// binary, so the guard is what keeps the two from colliding.
func registerFakeDrivers(names ...string) {
	for _, n := range names {
		if !slices.Contains(sql.Drivers(), n) {
			sql.Register(n, fakeDriver{})
		}
	}
}

func init() { registerFakeDrivers("postgres", "sqlite3") }

// A driver linked under the name the caller passes must count as
// registered, whatever name the module this project publishes would use:
// lib/pq's "postgres" and mattn's "sqlite3" opened fine in v1.9.0 and were
// refused in v1.10.0 because only the alias ("pgx", "sqlite") was checked.
func TestIsRegisteredAcceptsTheExactNameAndItsAlias(t *testing.T) {
	for _, name := range []string{"postgres", "sqlite3"} {
		if !IsRegistered(name) {
			t.Errorf("IsRegistered(%q) = false with a driver registered under that exact name (linked: %v)", name, sql.Drivers())
		}
	}
	// "pq" resolves to "pgx", which is NOT linked here — but the alias table
	// is only one of the two names checked, and "pq" itself is not linked
	// either, so this must stay false: sql.Open("pq") would fail.
	if IsRegistered("pq") {
		t.Errorf("IsRegistered(%q) = true although neither %q nor its alias %q is linked (linked: %v)", "pq", "pq", "pgx", sql.Drivers())
	}
	for _, name := range []string{"pgx", "sqlite", "mysql", "clickhouse"} {
		if IsRegistered(name) {
			t.Errorf("IsRegistered(%q) = true with nothing linked under that name (linked: %v)", name, sql.Drivers())
		}
	}
}

// When a driver for the same engine IS linked under another name, the hint
// has to say so: the reader's fix is then quark.New with that name (or the
// module import), not a `go get` for something already in the build.
func TestMissingDriverHintNamesTheDriverLinkedUnderAnotherName(t *testing.T) {
	// "sqlite" is not linked; mattn's "sqlite3" is (the fake above).
	hint := MissingDriverHint("sqlite")
	for _, want := range []string{`registered as "sqlite3"`, `quark.New("sqlite3", …)`, "linked right now: postgres, sqlite3"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint for %q is missing %q:\n%s", "sqlite", want, hint)
		}
	}
	// "pgx" is not linked; lib/pq's "postgres" is.
	if hint := MissingDriverHint("pgx"); !strings.Contains(hint, `registered as "postgres"`) {
		t.Errorf("hint for %q does not name the linked postgres driver:\n%s", "pgx", hint)
	}
	// Nothing for this engine is linked at all: no same-engine paragraph.
	if hint := MissingDriverHint("mysql"); strings.Contains(hint, "IS linked") {
		t.Errorf("hint for %q claims a same-engine driver is linked:\n%s", "mysql", hint)
	}
	// The requested name IS linked (the module is what is missing): the
	// paragraph would send the reader in a circle, so it stays out.
	if hint := MissingDriverHint("sqlite3"); strings.Contains(hint, "IS linked") {
		t.Errorf("hint for %q (linked) must not suggest another name:\n%s", "sqlite3", hint)
	}
}
