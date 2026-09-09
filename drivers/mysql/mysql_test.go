// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package mysql

import (
	"database/sql"
	"slices"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/jcsvwinston/quark/quarkdriver"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

func TestConformance(t *testing.T) {
	drivertest.Verify(t, drivertest.Case{
		Engine: "mysql",
		Classifier: quarkdriver.Classifier{
			UniqueViolation: uniqueViolation,
			Deadlock:        deadlock,
			TransientConn:   transientConn,
		},
		Unique:   &gomysql.MySQLError{Number: 1062},
		Deadlock: &gomysql.MySQLError{Number: 1213},
		Neither: []error{
			&gomysql.MySQLError{Number: 1452}, // foreign key
			&gomysql.MySQLError{Number: 1048}, // not null
			&gomysql.MySQLError{Number: 1064}, // syntax
		},
	})
}

func TestRecognisesConnectionLoss(t *testing.T) {
	for _, n := range []uint16{2002, 2003, 2006, 2013} {
		if !transientConn(&gomysql.MySQLError{Number: n}) {
			t.Errorf("%d must classify as a transient connection failure: a false keeps sending reads to a replica that is down", n)
		}
	}
	if transientConn(&gomysql.MySQLError{Number: 1062}) {
		t.Error("a duplicate key is not a connection failure")
	}
}

// The point of the module is the side effect.
func TestRegistersTheSQLDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "mysql") {
		t.Errorf("importing this module must register the \"mysql\" driver; registered: %v", sql.Drivers())
	}
}

// Quark reads a PostgreSQL SQLSTATE through the `SQLState() string` method
// that every PostgreSQL driver exposes, and it consults that method BEFORE
// any registered classifier. If this driver's error type ever grew one, its
// errors would be answered by the PostgreSQL branch and this engine would
// silently stop classifying — every violation reported as false, with nothing
// on screen to say so. A driver upgrade is the only thing that could change
// this, and this test is the warning.
func TestErrorDoesNotExposeSQLState(t *testing.T) {
	var err error = &gomysql.MySQLError{Number: 1062}
	if _, ok := err.(interface{ SQLState() string }); ok {
		t.Error("the MySQL error type now exposes SQLState(): Quark's PostgreSQL branch will shadow this engine's classifier")
	}
}
