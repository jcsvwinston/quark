// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// TestSavepointDialect is the BB-9 regression: savepoint DML must be
// dialect-correct. The ANSI form (SAVEPOINT / ROLLBACK TO SAVEPOINT /
// RELEASE SAVEPOINT) works on PostgreSQL/MySQL/MariaDB/SQLite, but SQL Server
// uses SAVE TRANSACTION / ROLLBACK TRANSACTION and has no release statement,
// and Oracle has no RELEASE SAVEPOINT — so nested transactions (tx.Tx) failed
// on those two engines until the dialects overrode the statements via
// SavepointDialect.
func TestSavepointDialect(t *testing.T) {
	// ANSI dialects must NOT implement SavepointDialect — they take the
	// default statements in the Tx layer.
	for _, d := range []quark.Dialect{quark.PostgreSQL(), quark.MySQL(), quark.SQLite()} {
		if _, ok := d.(quark.SavepointDialect); ok {
			t.Errorf("%s should use the ANSI savepoint default, but implements SavepointDialect", d.Name())
		}
	}

	t.Run("MSSQL", func(t *testing.T) {
		sd, ok := any(quark.MSSQL()).(quark.SavepointDialect)
		if !ok {
			t.Fatal("MSSQL dialect must implement SavepointDialect")
		}
		if got := sd.SavepointStmt("sp_1"); got != "SAVE TRANSACTION sp_1" {
			t.Errorf("SavepointStmt = %q, want SAVE TRANSACTION sp_1", got)
		}
		if got := sd.RollbackToSavepointStmt("sp_1"); got != "ROLLBACK TRANSACTION sp_1" {
			t.Errorf("RollbackToSavepointStmt = %q, want ROLLBACK TRANSACTION sp_1", got)
		}
		if got := sd.ReleaseSavepointStmt("sp_1"); got != "" {
			t.Errorf("ReleaseSavepointStmt = %q, want \"\" (SQL Server has no release statement)", got)
		}
	})

	t.Run("Oracle", func(t *testing.T) {
		sd, ok := any(quark.Oracle()).(quark.SavepointDialect)
		if !ok {
			t.Fatal("Oracle dialect must implement SavepointDialect")
		}
		// Oracle's SAVEPOINT / ROLLBACK TO SAVEPOINT are ANSI; only RELEASE is
		// unsupported (the bug). The exact quoting is the dialect's choice — we
		// assert the keyword and the empty release.
		if got := sd.SavepointStmt("sp_1"); got == "" {
			t.Error("Oracle SavepointStmt must be non-empty")
		}
		if got := sd.RollbackToSavepointStmt("sp_1"); !strings.HasPrefix(got, "ROLLBACK TO SAVEPOINT") {
			t.Errorf("RollbackToSavepointStmt = %q, want prefix \"ROLLBACK TO SAVEPOINT\"", got)
		}
		if got := sd.ReleaseSavepointStmt("sp_1"); got != "" {
			t.Errorf("ReleaseSavepointStmt = %q, want \"\" (Oracle has no RELEASE SAVEPOINT)", got)
		}
	})
}
