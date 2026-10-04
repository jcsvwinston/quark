// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package extdriver is a driver module for an engine Quark does not ship,
// written the way a third party would write one: in a module whose path is
// not this repository's, built with no workspace, against nothing but the
// public API. internal/extbench copies it into a temporary module and runs
// it; it is not compiled as part of the library.
//
// The engine is SQLite under another name, so the fixture needs no database
// server: modernc's driver registered a second time, and SQLite's dialect
// with its name changed.
//
//	import _ "example.com/extdriver"
package extdriver

import (
	"context"
	"database/sql"

	"github.com/jcsvwinston/quark"

	"example.com/extdriver/errs"

	moderncsqlite "modernc.org/sqlite"
)

// Dialect is SQLite's dialect under the fixture's engine name.
type Dialect struct{ quark.Dialect }

// Name answers the engine name the driver registers under.
func (Dialect) Name() string { return errs.Engine }

// IntrospectSchema forwards the optional interface SQLite's dialect has.
func (d Dialect) IntrospectSchema(ctx context.Context, exec quark.Executor) (quark.Schema, error) {
	return d.Dialect.(quark.SchemaIntrospector).IntrospectSchema(ctx, exec)
}

func init() {
	sql.Register(errs.Engine, &moderncsqlite.Driver{})
	quark.RegisterDialect(errs.Engine, Dialect{quark.SQLite()})
}
