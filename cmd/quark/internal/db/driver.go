// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package db

// DriverName maps the dialect-style names accepted in .quark.yml (and by
// `quark init --dialect`) to the database/sql driver names actually
// registered in this binary. `quark init` writes `driver: postgresql`, but
// the registered PostgreSQL driver is pgx, so passing the config value
// straight to sql.Open fails with `unknown driver "postgresql"`. Unknown
// names pass through untouched so custom drivers keep working.
func DriverName(name string) string {
	switch name {
	case "postgresql", "postgres":
		return "pgx"
	case "mariadb":
		// MariaDB speaks the MySQL wire protocol through go-sql-driver/mysql;
		// quark.New probes the server and upgrades the dialect on its own.
		return "mysql"
	default:
		return name
	}
}
