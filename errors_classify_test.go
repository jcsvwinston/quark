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

// engineError names a driver error shape so failures say which engine broke.
// modernc.org/sqlite.Error has only unexported fields and cannot be
// fabricated here; the SQLite path is covered by the live-engine suite.
type engineError struct {
	engine string
	err    error
}

func TestIsUniqueViolation_AcrossDrivers(t *testing.T) {
	violations := []engineError{
		{"postgres/pgx", &pgconn.PgError{Code: "23505"}},
		{"postgres/lib-pq", &libpqError{Code: "23505"}},
		{"mysql", &gomysql.MySQLError{Number: 1062}},
		{"mssql/unique-constraint", mssql.Error{Number: 2627}},
		{"mssql/unique-index", mssql.Error{Number: 2601}},
		{"oracle", &goora.OracleError{ErrCode: 1}},
	}
	for _, c := range violations {
		t.Run(c.engine, func(t *testing.T) {
			if !IsUniqueViolation(c.err) {
				t.Errorf("IsUniqueViolation(%s) = false, want true", c.engine)
			}
			if !IsUniqueViolation(fmt.Errorf("create failed: %w", c.err)) {
				t.Errorf("IsUniqueViolation(wrapped %s) = false, want true", c.engine)
			}
		})
	}

	others := []engineError{
		{"nil", nil},
		{"plain error", errors.New("connection refused")},
		{"postgres deadlock", &pgconn.PgError{Code: "40P01"}},
		{"postgres foreign key", &pgconn.PgError{Code: "23503"}},
		{"mysql deadlock", &gomysql.MySQLError{Number: 1213}},
	}
	for _, c := range others {
		t.Run("not/"+c.engine, func(t *testing.T) {
			if IsUniqueViolation(c.err) {
				t.Errorf("IsUniqueViolation(%s) = true, want false", c.engine)
			}
		})
	}
}

func TestIsDeadlock_AcrossDrivers(t *testing.T) {
	deadlocks := []engineError{
		{"postgres/pgx", &pgconn.PgError{Code: "40P01"}},
		{"postgres/lib-pq", &libpqError{Code: "40P01"}},
		{"mysql", &gomysql.MySQLError{Number: 1213}},
		{"mssql", mssql.Error{Number: 1205}},
		{"oracle", &goora.OracleError{ErrCode: 60}},
	}
	for _, c := range deadlocks {
		t.Run(c.engine, func(t *testing.T) {
			if !IsDeadlock(c.err) {
				t.Errorf("IsDeadlock(%s) = false, want true", c.engine)
			}
			if !IsDeadlock(fmt.Errorf("tx failed: %w", c.err)) {
				t.Errorf("IsDeadlock(wrapped %s) = false, want true", c.engine)
			}
		})
	}

	for _, c := range []engineError{
		{"nil", nil},
		{"unique violation", &pgconn.PgError{Code: "23505"}},
	} {
		t.Run("not/"+c.engine, func(t *testing.T) {
			if IsDeadlock(c.err) {
				t.Errorf("IsDeadlock(%s) = true, want false", c.engine)
			}
		})
	}
}

// TestWrapDBError_UniqueViolationIsLocaleIndependent pins that
// ErrConstraintViolation is reached by the driver's error CODE and not by the
// wording of its message.
//
// The message below is what a PostgreSQL server configured with a Spanish
// lc_messages actually returns for SQLSTATE 23505. It contains none of the
// English substrings wrapDBError matches on, so before the code check was
// added this error fell through unclassified: the same duplicate insert was
// a constraint violation on an English server and an unrecognised error on a
// Spanish one.
func TestWrapDBError_UniqueViolationIsLocaleIndependent(t *testing.T) {
	translated := &pgconn.PgError{
		Code:     "23505",
		Severity: "ERROR",
		Message:  "llave duplicada viola restricción de unicidad «users_email_key»",
	}

	got := wrapDBError(translated)
	if !errors.Is(got, ErrConstraintViolation) {
		t.Fatalf("wrapDBError(23505 with translated message) did not yield ErrConstraintViolation: %v", got)
	}
	// The original error must stay reachable for callers that need detail.
	if !errors.Is(got, error(translated)) {
		t.Error("wrapDBError dropped the underlying driver error")
	}
	// And the precise predicate must agree with the coarse sentinel.
	if !IsUniqueViolation(got) {
		t.Error("IsUniqueViolation disagrees with ErrConstraintViolation on the same error")
	}
}
