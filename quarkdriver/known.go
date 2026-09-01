// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarkdriver

import (
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Known describes a driver module this project publishes.
//
// The table is data, not code: Quark carries the NAME of each satellite module
// and none of its code. A name belongs here only because this project
// publishes it, since the promise the error makes — `go get` this and it
// works — is one that has to be kept.
type Known struct {
	// Name is what a person calls the engine.
	Name string

	// Module is the `go get` target.
	Module string
}

// knownDrivers is keyed by the database/sql driver name, because that is the
// name sql.Open fails on, and the failure is where the guidance has to appear.
// The aliases below map what people write to what the driver registers as.
var knownDrivers = map[string]Known{
	"pgx":       {Name: "postgres", Module: "github.com/jcsvwinston/quark/drivers/postgres"},
	"mysql":     {Name: "mysql", Module: "github.com/jcsvwinston/quark/drivers/mysql"},
	"sqlite":    {Name: "sqlite", Module: "github.com/jcsvwinston/quark/drivers/sqlite"},
	"sqlserver": {Name: "sqlserver", Module: "github.com/jcsvwinston/quark/drivers/mssql"},
	"oracle":    {Name: "oracle", Module: "github.com/jcsvwinston/quark/drivers/oracle"},
}

// driverAliases maps the names Quark's dialect layer accepts to the
// database/sql name the driver registers under. `quark init` writes
// "postgresql", the CLI accepts "postgres", and the driver is "pgx" — a person
// hitting this error should not have to know that.
var driverAliases = map[string]string{
	"postgres": "pgx", "postgresql": "pgx", "pq": "pgx", "pgx/v5": "pgx",
	"mariadb": "mysql",
	"mssql":   "sqlserver",
	"sqlite3": "sqlite",
}

// MissingDriverHint returns a message naming the module to import when
// driverName is one this project publishes, and "" when it is not.
//
// It answers "" for an unknown name on purpose: inventing a `go get` for a
// driver nobody publishes would send the reader somewhere that does not exist,
// which is worse than the plain error it replaced.
func MissingDriverHint(driverName string) string {
	// Lowered before the lookup: the hint exists for a person reading an
	// error, and people write "SQLite" and "Postgres".
	name := strings.ToLower(strings.TrimSpace(driverName))
	if alias, ok := driverAliases[name]; ok {
		name = alias
	}
	k, ours := knownDrivers[name]
	if !ours {
		return ""
	}

	linked := "none"
	if reg := sql.Drivers(); len(reg) > 0 {
		sort.Strings(reg)
		linked = strings.Join(reg, ", ")
	}

	return fmt.Sprintf("the %s driver ships as its own module and is not imported yet.\n\n"+
		"\tAdd it to your build:\n\n"+
		"\t\tgo get %s\n\n"+
		"\tand import it for its side effect, the way database/sql drivers are wired:\n\n"+
		"\t\timport _ %q\n\n"+
		"\t(linked right now: %s)",
		k.Name, k.Module, k.Module, linked)
}

// IsRegistered reports whether driverName is registered with database/sql,
// resolving the aliases Quark's dialect layer accepts.
func IsRegistered(driverName string) bool {
	// NOT lowered: this answers whether database/sql will find the driver,
	// and there the name is case-sensitive. Only the alias table is
	// consulted case-insensitively, because those are names Quark's own
	// dialect layer accepts.
	name := driverName
	if alias, ok := driverAliases[strings.ToLower(driverName)]; ok {
		name = alias
	}
	return slices.Contains(sql.Drivers(), name)
}
