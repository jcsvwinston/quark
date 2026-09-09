// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package mssql

import (
	"database/sql"
	"slices"
	"testing"

	mssqldb "github.com/microsoft/go-mssqldb"

	"github.com/jcsvwinston/quark/quarkdriver"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

func TestConformance(t *testing.T) {
	drivertest.Verify(t, drivertest.Case{
		Engine: "sqlserver",
		Classifier: quarkdriver.Classifier{
			UniqueViolation: uniqueViolation,
			Deadlock:        deadlock,
			TransientConn:   transientConn,
		},
		Unique:   mssqldb.Error{Number: 2627},
		Deadlock: mssqldb.Error{Number: 1205},
		Neither: []error{
			mssqldb.Error{Number: 547}, // foreign key / check
			mssqldb.Error{Number: 515}, // not null
		},
	})
	// 2601 is the second unique form — a duplicate row in a unique INDEX
	// rather than a CONSTRAINT. Case carries one Unique, so this is checked
	// on its own; missing it would leave half the engine's duplicates
	// reported as internal errors.
	if !uniqueViolation(mssqldb.Error{Number: 2601}) {
		t.Error("2601 must classify as a unique violation")
	}
}

func TestRegistersTheSQLDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "sqlserver") {
		t.Errorf("importing this module must register the \"sqlserver\" driver; registered: %v", sql.Drivers())
	}
}

// See the note on the same test in the MySQL module: Quark consults
// `SQLState() string` before any registered classifier, so an error type that
// grows one stops being classified by this module. SQL Server comes closest
// of the three — it has SQLErrorState(), which differs in both name and
// return type.
func TestErrorDoesNotExposeSQLState(t *testing.T) {
	var err error = mssqldb.Error{Number: 2627}
	if _, ok := err.(interface{ SQLState() string }); ok {
		t.Error("the SQL Server error type now exposes SQLState(): Quark's PostgreSQL branch will shadow this engine's classifier")
	}
}
