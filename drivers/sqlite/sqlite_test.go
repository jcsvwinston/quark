// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/jcsvwinston/quark/internal/driverclassify"
	"github.com/jcsvwinston/quark/quarkdriver"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

// The classifier is verified against errors the driver itself produced.
// modernc.org/sqlite's Error type has unexported fields and cannot be
// fabricated, and a hand-rolled stand-in would only prove the test agrees
// with itself — so the test provokes the real thing, which an in-memory
// database makes free.
func TestConformance(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE u (email TEXT NOT NULL UNIQUE, note TEXT)",
		"INSERT INTO u (email, note) VALUES ('a@b.c', 'first')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	dup := mustFail(t, db, "INSERT INTO u (email, note) VALUES ('a@b.c', 'second')")
	// A NOT NULL failure is a constraint error on the same table and must NOT
	// be reported as unique, or the caller blames the wrong field. This is
	// what fails if the predicate is widened to "any SQLITE_CONSTRAINT_*".
	notNull := mustFail(t, db, "INSERT INTO u (email, note) VALUES (NULL, 'third')")

	drivertest.Verify(t, drivertest.Case{
		Engine: "sqlite",
		Classifier: quarkdriver.Classifier{
			UniqueViolation: driverclassify.SQLiteUniqueViolation,
			Deadlock:        driverclassify.SQLiteDeadlock,
			TransientConn:   driverclassify.SQLiteTransientConn,
		},
		Unique: dup,
		// Deadlock is nil on purpose: SQLite is single-writer and never picks
		// a victim. SQLITE_BUSY looks like a deadlock and is deliberately not
		// classified as one — it is lock contention, and retrying a
		// transaction that was never a victim just repeats the contention.
		Neither: []error{notNull},
	})
}

func mustFail(t *testing.T, db *sql.DB, stmt string) error {
	t.Helper()
	_, err := db.Exec(stmt)
	if err == nil {
		t.Fatalf("%s must fail", stmt)
	}
	return err
}

func TestRegistersTheSQLDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "sqlite") {
		t.Errorf("importing this module must register the \"sqlite\" driver; registered: %v", sql.Drivers())
	}
}
