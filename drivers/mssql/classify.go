// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package mssql

// The SQL Server error predicates. They live in the module that owns the
// driver, so that the Quark library module requires no engine (ADR-0024).
//
// Each matches on the error NUMBER rather than on message text: the same
// rejection is worded in the server's own language.

import (
	"errors"

	mssqldb "github.com/microsoft/go-mssqldb"
)

// number targets the VALUE type: mssqldb.Error has a value receiver on
// Error(), so a pointer target never matches.
func number(err error) (int32, bool) {
	var e mssqldb.Error
	if errors.As(err, &e) {
		return e.Number, true
	}
	return 0, false
}

// uniqueViolation matches 2627 (unique/primary-key CONSTRAINT) and 2601
// (duplicate row in a unique INDEX). The engine picks between them by how
// uniqueness was declared, which the caller never sees.
func uniqueViolation(err error) bool {
	n, ok := number(err)
	return ok && (n == 2627 || n == 2601)
}

// deadlock matches 1205: chosen as the deadlock victim.
func deadlock(err error) bool {
	n, ok := number(err)
	return ok && n == 1205
}

// transientConn matches the transport-level failures.
func transientConn(err error) bool {
	n, ok := number(err)
	if !ok {
		return false
	}
	switch n {
	case 233, 10053, 10054, 10060:
		return true
	}
	return false
}
