// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package drivertemplate is a Quark driver for one engine, written the way a
// driver module outside Quark's repository writes one: against package
// quarkdriver alone, never package quark. It is the code the guide "Writing a
// driver" (website/docs/guides/writing-a-driver.mdx) walks through, and a
// test of this module fails when a Go block of that page stops being code of
// this module.
//
// The engine is SQLite, through modernc.org/sqlite, registered under a name of
// its own (Name): it needs no server, so the kit's engine checks run wherever
// the tests do, and the name does not collide with the "sqlite" that Quark's
// own driver module registers. A driver for another engine replaces the
// engine and the name and keeps the shape: one name, under which the module
// registers three things — the database/sql driver, the error classifier and
// the dialect — and tests that run the conformance kit and the engine suite.
//
// An application imports the module for its side effect and opens the engine
// by name:
//
//	import _ "example.com/drivertemplate"
//
//	client, err := quark.New("templite", dsn)
//
// The module is never published. Its path is not this repository's, so the
// Go toolchain refuses it the internal packages of Quark exactly as it would a
// third party's; its go.mod points Quark at this tree with a replace
// directive, so it is built and tested against the code under review. A
// driver started from it drops the replace and requires a published Quark.
package drivertemplate

import (
	"database/sql"

	"github.com/jcsvwinston/quark/quarkdriver"

	"modernc.org/sqlite"
)

// Name is the name the driver registers under in all three registries:
// database/sql's drivers, Quark's error classifiers and Quark's dialects.
// quark.New(Name, dsn) finds all three by it.
const Name = "templite"

func init() {
	sql.Register(Name, &sqlite.Driver{})
	quarkdriver.MustRegister(Name, Classifier)
	quarkdriver.RegisterDialect(Name, Dialect{})
}
