// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package oracle links the Oracle driver into a Quark
// application and teaches Quark to recognise its errors.
//
//	import _ "github.com/jcsvwinston/quark/drivers/oracle"
//
// Without it, Quark still runs against this engine — it uses whatever
// *sql.DB it is handed — but it cannot tell a duplicate key from a deadlock
// from a dropped connection, because those distinctions live in the driver's
// error types. None of those predicates FAILS when it does not recognise an
// error: it answers false, and a false from the deadlock predicate is a
// transaction that quietly stops being retried.
package oracle

import (
	_ "github.com/sijms/go-ora/v2"

	"github.com/jcsvwinston/quark/internal/driverclassify"
	"github.com/jcsvwinston/quark/quarkdriver"
)

func init() {
	quarkdriver.MustRegister("oracle", quarkdriver.Classifier{
		UniqueViolation: driverclassify.OracleUniqueViolation,
		Deadlock:        driverclassify.OracleDeadlock,
		TransientConn:   driverclassify.OracleTransientConn,
	})
}
