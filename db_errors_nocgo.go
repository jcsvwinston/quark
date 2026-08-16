// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build !cgo

package quark

// isMattnUniqueViolation is a no-op without cgo: the mattn/go-sqlite3 driver
// cannot be linked into a CGO_ENABLED=0 binary, so its errors can never
// occur there. SQLite classification in pure-Go builds is covered by the
// modernc.org/sqlite branch in db_errors.go (same numeric extended codes).
func isMattnUniqueViolation(error) bool { return false }
