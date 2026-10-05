// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// The kit's dialect half is newer than the quark this module pins; the
// standalone lane builds the module against that floor and leaves this file
// out (-tags pinnedquark) until the release train raises it.

//go:build !pinnedquark

package sqlite

import (
	"database/sql"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
)

// TestDialectConformance runs the dialect kit against Quark's SQLite dialect
// through this module's driver, on an in-memory database — the one engine
// whose half of the kit runs on every machine. Foreign keys are enforced on
// the connection, so the kit's foreign-key checks are judged by the engine.
func TestDialectConformance(t *testing.T) {
	db, err := sql.Open("sqlite", "file:dialect_kit?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drivertest.VerifyDialect(t, drivertest.DialectCase{Dialect: quark.SQLite(), DB: db, DriverName: "sqlite"})
}
