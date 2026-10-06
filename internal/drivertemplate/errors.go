// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertemplate

import (
	"errors"

	"github.com/jcsvwinston/quark/quarkdriver"

	"modernc.org/sqlite"
)

// Classifier is what the driver knows about its engine's errors. Quark
// consults it for quark.IsUniqueViolation and quark.IsDeadlock, to retry a
// transaction the engine chose as a deadlock victim, and to fail a read
// replica over. Each predicate matches the error's code through errors.As —
// never its message, which an engine may translate — and answers false for
// an error this driver did not produce: Quark consults every registered
// classifier in turn.
var Classifier = quarkdriver.Classifier{
	UniqueViolation: uniqueViolation,
	Deadlock:        deadlock,
	TransientConn:   transientConn,
}

// uniqueViolation matches SQLite's extended result codes 2067
// (SQLITE_CONSTRAINT_UNIQUE) and 1555 (SQLITE_CONSTRAINT_PRIMARYKEY), and no
// other constraint: a NOT NULL, CHECK or foreign-key failure is not "already
// taken".
func uniqueViolation(err error) bool {
	var e *sqlite.Error
	if errors.As(err, &e) {
		return e.Code() == 2067 || e.Code() == 1555
	}
	return false
}

// deadlock answers false: SQLite has one writer and never picks a victim. An
// engine that does returns true for the victim's code, and Quark retries the
// transaction.
func deadlock(error) bool { return false }

// transientConn answers false: the engine runs inside the process, so there is
// no connection to lose. An engine behind a network returns true for a
// connection that dropped or was refused — never for a query error, and never
// for the caller's own cancellation.
func transientConn(error) bool { return false }
