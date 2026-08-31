// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package mssql

import (
	"database/sql"
	"slices"
	"testing"

	mssqldb "github.com/microsoft/go-mssqldb"

	"github.com/jcsvwinston/quark/internal/driverclassify"
	"github.com/jcsvwinston/quark/quarkdriver"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

func TestConformance(t *testing.T) {
	drivertest.Verify(t, drivertest.Case{
		Engine: "sqlserver",
		Classifier: quarkdriver.Classifier{
			UniqueViolation: driverclassify.MSSQLUniqueViolation,
			Deadlock:        driverclassify.MSSQLDeadlock,
			TransientConn:   driverclassify.MSSQLTransientConn,
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
	if !driverclassify.MSSQLUniqueViolation(mssqldb.Error{Number: 2601}) {
		t.Error("2601 must classify as a unique violation")
	}
}

func TestRegistersTheSQLDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "sqlserver") {
		t.Errorf("importing this module must register the \"sqlserver\" driver; registered: %v", sql.Drivers())
	}
}
