// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package sqlite links the SQLite (pure Go: no cgo, so it cross-compiles) driver into a Quark
// application and teaches Quark to recognise its errors.
//
//	import _ "github.com/jcsvwinston/quark/drivers/sqlite"
//
// Without it, Quark still runs against this engine — it uses whatever
// *sql.DB it is handed — but it cannot tell a duplicate key from a deadlock
// from a dropped connection, because those distinctions live in the driver's
// error types. None of those predicates FAILS when it does not recognise an
// error: it answers false, and a false from the deadlock predicate is a
// transaction that quietly stops being retried.
package sqlite

import (
	_ "modernc.org/sqlite"

	"github.com/jcsvwinston/quark/internal/driverclassify"
	"github.com/jcsvwinston/quark/quarkdriver"
)

func init() {
	quarkdriver.MustRegister("sqlite", quarkdriver.Classifier{
		UniqueViolation: driverclassify.SQLiteUniqueViolation,
		Deadlock:        driverclassify.SQLiteDeadlock,
		TransientConn:   driverclassify.SQLiteTransientConn,
	})
}
