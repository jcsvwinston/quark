// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package mysql links the MySQL and MariaDB driver into a Quark
// application and teaches Quark to recognise its errors.
//
//	import _ "github.com/jcsvwinston/quark/drivers/mysql"
//
// Without it, Quark still runs against this engine — it uses whatever
// *sql.DB it is handed — but it cannot tell a duplicate key from a deadlock
// from a dropped connection, because those distinctions live in the driver's
// error types. None of those predicates FAILS when it does not recognise an
// error: it answers false, and a false from the deadlock predicate is a
// transaction that quietly stops being retried.
package mysql

import (
	_ "github.com/go-sql-driver/mysql"

	"github.com/jcsvwinston/quark/internal/driverclassify"
	"github.com/jcsvwinston/quark/quarkdriver"
)

func init() {
	quarkdriver.MustRegister("mysql", quarkdriver.Classifier{
		UniqueViolation: driverclassify.MySQLUniqueViolation,
		Deadlock:        driverclassify.MySQLDeadlock,
		TransientConn:   driverclassify.MySQLTransientConn,
	})
}
