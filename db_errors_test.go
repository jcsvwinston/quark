// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"
	"fmt"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
	goora "github.com/sijms/go-ora/v2/network"
)

// TestIsDeadlock_Detection pins the per-driver mapping documented in
// F4-7. We fabricate the canonical error type each driver returns on
// deadlock and assert that isDeadlock recognises it. This avoids
// needing a live DB just to exercise the classifier.
func TestIsDeadlock_Detection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a deadlock", nil, false},
		{"plain error is not a deadlock", fmt.Errorf("connection refused"), false},

		// PostgreSQL: SQLSTATE 40P01 = deadlock_detected.
		{"pg 40P01 deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"pg 23505 unique violation is NOT a deadlock", &pgconn.PgError{Code: "23505"}, false},

		// MySQL / MariaDB.
		{"mysql 1213 deadlock", &gomysql.MySQLError{Number: 1213}, true},
		{"mysql 1062 dup-entry is NOT a deadlock", &gomysql.MySQLError{Number: 1062}, false},

		// MSSQL.
		{"mssql 1205 deadlock victim", mssql.Error{Number: 1205}, true},
		{"mssql 2627 unique is NOT a deadlock", mssql.Error{Number: 2627}, false},

		// Oracle: ORA-00060.
		{"oracle ORA-00060 deadlock", &goora.OracleError{ErrCode: 60}, true},
		{"oracle ORA-00001 unique is NOT a deadlock", &goora.OracleError{ErrCode: 1}, false},

		// Wrapped — errors.As walks the Unwrap chain, so a wrapped
		// driver error still classifies correctly.
		{"wrapped pg deadlock", fmt.Errorf("transaction failed: %w", &pgconn.PgError{Code: "40P01"}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeadlock(tc.err); got != tc.want {
				t.Errorf("isDeadlock(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsDeadlock_DoesNotCollideWithUniqueViolation: the two
// classifiers must return mutually-exclusive results — a unique
// violation is not a deadlock and a deadlock is not a unique
// violation. This is the contract every retry caller relies on.
func TestIsDeadlock_DoesNotCollideWithUniqueViolation(t *testing.T) {
	pgUnique := &pgconn.PgError{Code: "23505"}
	pgDeadlock := &pgconn.PgError{Code: "40P01"}

	if isDeadlock(pgUnique) {
		t.Error("PG 23505 (unique) wrongly classified as deadlock")
	}
	if isUniqueViolation(pgDeadlock) {
		t.Error("PG 40P01 (deadlock) wrongly classified as unique violation")
	}
}

// fakeDeadlock returns an error that isDeadlock recognises — useful
// for exercising the retry loop without a live multi-writer DB.
func fakeDeadlock() error {
	return &pgconn.PgError{Code: "40P01"}
}

// TestIsDeadlock_FakeWorks sanity-checks the helper above: the
// retry-path tests in tx_test.go rely on `fakeDeadlock` being
// classified as a deadlock.
func TestIsDeadlock_FakeWorks(t *testing.T) {
	if !isDeadlock(fakeDeadlock()) {
		t.Fatal("fakeDeadlock() must satisfy isDeadlock — retry tests depend on it")
	}
	if !errors.Is(fakeDeadlock(), fakeDeadlock()) {
		// Sanity check only — pg errors aren't comparable via Is by
		// default. Skip if the assertion is meaningless.
		t.Log("note: pgconn.PgError instances aren't comparable; tests use isDeadlock directly")
	}
}
