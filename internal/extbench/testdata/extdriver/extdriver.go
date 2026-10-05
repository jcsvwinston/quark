// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package extdriver is a driver module for an engine Quark does not ship,
// written the way a third party would write one: in a module whose path is
// not this repository's, built with no workspace, against nothing but the
// public API. internal/extbench copies it into a temporary module and runs
// it; it is not compiled as part of the library.
//
// The engine is SQLite under another name, so the fixture needs no database
// server: modernc's driver registered a second time, a classifier (package
// errs) and a dialect (package dialect) of its own. All three registrations
// go through quarkdriver, and no package of the driver imports package quark
// (ADR-0026); the bench checks that on the build graph (DRV-02). Only the
// fixture's test, which plays the application, imports package quark.
//
//	import _ "example.com/extdriver"
package extdriver

import (
	"database/sql"

	"github.com/jcsvwinston/quark/quarkdriver"

	"example.com/extdriver/dialect"
	"example.com/extdriver/errs"

	moderncsqlite "modernc.org/sqlite"
)

func init() {
	sql.Register(errs.Engine, &moderncsqlite.Driver{})
	quarkdriver.RegisterDialect(errs.Engine, dialect.New(errs.Engine))
}
