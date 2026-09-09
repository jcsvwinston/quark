// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

// These suites connect to real databases, so the test binary links every
// engine — through the driver MODULES, the way an application does. Each one
// registers its database/sql driver AND the predicates that tell a duplicate
// key from a deadlock from a dropped connection (ADR-0023); linking the bare
// driver would register only the first half, and the predicates would answer
// false with nothing on screen to say so.
//
// The library's own module cannot import these: they import the library, and
// the requirement would be circular. This harness can, because it is a module
// of its own (ADR-0024) — which is the same reason the library's go.mod no
// longer lists an engine.
import (
	_ "github.com/jcsvwinston/quark/drivers/mssql"
	_ "github.com/jcsvwinston/quark/drivers/mysql"
	_ "github.com/jcsvwinston/quark/drivers/oracle"
	_ "github.com/jcsvwinston/quark/drivers/postgres"
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)
