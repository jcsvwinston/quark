// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build cgo

package quark

import (
	"errors"

	mattnsqlite "github.com/mattn/go-sqlite3"
)

// isMattnUniqueViolation classifies mattn/go-sqlite3 unique/PK violations.
// ExtendedCode 2067 = SQLITE_CONSTRAINT_UNIQUE, 1555 =
// SQLITE_CONSTRAINT_PRIMARYKEY. Compiled only with cgo — the driver itself
// needs it; see the !cgo stub in db_errors_nocgo.go.
func isMattnUniqueViolation(err error) bool {
	var mattnErr mattnsqlite.Error
	if errors.As(err, &mattnErr) {
		switch mattnErr.ExtendedCode {
		case mattnsqlite.ErrConstraintUnique, mattnsqlite.ErrConstraintPrimaryKey:
			return true
		}
	}
	return false
}
