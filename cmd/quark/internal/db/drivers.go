// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package db

// The Quark library links no driver: each ships as its own module so an
// application pays only for the engine it uses (ADR-0023). The CLI is the one
// place where linking every engine is right — it is a tool people install
// once and point at whatever database they have, and asking them to rebuild
// it per engine would be absurd.
//
// It imports the driver MODULES, the way any application does. That was not
// possible while the CLI shared the library's module: the driver modules
// import Quark, so the requirement would have been circular, and the CLI
// linked the bare drivers and re-registered the predicates from a package
// shared with the library instead. Moving the CLI to its own module
// (ADR-0024) removed the cycle, and with it the shared package — which is
// what let the library's go.mod stop requiring every engine.
import (
	_ "github.com/jcsvwinston/quark/drivers/mssql"
	_ "github.com/jcsvwinston/quark/drivers/mysql"
	_ "github.com/jcsvwinston/quark/drivers/oracle"
	_ "github.com/jcsvwinston/quark/drivers/postgres"
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)
