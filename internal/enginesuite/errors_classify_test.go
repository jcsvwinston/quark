// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

// The cross-engine classification tests. They fabricate the error value each
// driver returns and check that Quark's exported predicates recognise it, so
// they need every driver's error types — which is why they run from this
// module rather than from the library's (ADR-0024). The library keeps the
// same assertions against the lib/pq-shaped stand-in, which needs no driver.

import (
	"errors"
	"fmt"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
	goora "github.com/sijms/go-ora/v2/network"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
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
		{"mysql", &gomysql.MySQLError{Number: 1062}},
		{"mssql/unique-constraint", mssql.Error{Number: 2627}},
		{"mssql/unique-index", mssql.Error{Number: 2601}},
		{"oracle", &goora.OracleError{ErrCode: 1}},
	}
	for _, c := range violations {
		t.Run(c.engine, func(t *testing.T) {
			if !quark.IsUniqueViolation(c.err) {
				t.Errorf("IsUniqueViolation(%s) = false, want true", c.engine)
			}
			if !quark.IsUniqueViolation(fmt.Errorf("create failed: %w", c.err)) {
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
			if quark.IsUniqueViolation(c.err) {
				t.Errorf("IsUniqueViolation(%s) = true, want false", c.engine)
			}
		})
	}
}

func TestIsDeadlock_AcrossDrivers(t *testing.T) {
	deadlocks := []engineError{
		{"postgres/pgx", &pgconn.PgError{Code: "40P01"}},
		{"mysql", &gomysql.MySQLError{Number: 1213}},
		{"mssql", mssql.Error{Number: 1205}},
		{"oracle", &goora.OracleError{ErrCode: 60}},
	}
	for _, c := range deadlocks {
		t.Run(c.engine, func(t *testing.T) {
			if !quark.IsDeadlock(c.err) {
				t.Errorf("IsDeadlock(%s) = false, want true", c.engine)
			}
			if !quark.IsDeadlock(fmt.Errorf("tx failed: %w", c.err)) {
				t.Errorf("IsDeadlock(wrapped %s) = false, want true", c.engine)
			}
		})
	}

	for _, c := range []engineError{
		{"nil", nil},
		{"unique violation", &pgconn.PgError{Code: "23505"}},
	} {
		t.Run("not/"+c.engine, func(t *testing.T) {
			if quark.IsDeadlock(c.err) {
				t.Errorf("IsDeadlock(%s) = true, want false", c.engine)
			}
		})
	}
}

// The engine names Quark's WARN looks a classifier up by are the names the
// driver modules actually register under — the library's own test asserts the
// mapping, and this asserts the other side of it. The pairing is easy to get
// wrong in one place and invisible when it is: SQL Server's dialect is
// "mssql" and its module registers "sqlserver"; MariaDB shares MySQL's
// driver and therefore MySQL's classifier; PostgreSQL registers none on
// purpose.
func TestDriverModulesRegisterTheEnginesTheMappingNames(t *testing.T) {
	for _, engine := range []string{"mysql", "sqlite", "sqlserver", "oracle"} {
		if !quarkdriver.HasEngine(engine) {
			t.Errorf("no driver module registers a classifier for %q; the library's WARN names it", engine)
		}
	}
}
