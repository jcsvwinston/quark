// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package oracle

// The Oracle error predicates. They live in the module that owns the driver,
// so that the Quark library module requires no engine (ADR-0024).

import (
	"errors"

	goora "github.com/sijms/go-ora/v2/network"
)

// code walks the Unwrap chain: go-ora/v2 may return *network.OracleError
// directly or wrapped inside a *network.SessionError, and errors.As covers
// both shapes — do not "simplify" this into a type switch.
func code(err error) (int, bool) {
	var e *goora.OracleError
	if errors.As(err, &e) {
		return e.ErrCode, true
	}
	return 0, false
}

// uniqueViolation matches ORA-00001.
func uniqueViolation(err error) bool {
	c, ok := code(err)
	return ok && c == 1
}

// deadlock matches ORA-00060.
func deadlock(err error) bool {
	c, ok := code(err)
	return ok && c == 60
}

// transientConn always reports false. The Oracle driver surfaces connection
// loss through the network layer, which Quark classifies via net.Error before
// consulting any driver. Reporting false is the honest answer, not a gap:
// claiming an error this driver cannot identify would answer for another
// engine.
func transientConn(error) bool { return false }
