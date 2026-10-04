// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"

	// The driver module an application imports for MySQL; it brings in
	// go-sql-driver/mysql, which registers "mysql".
	_ "github.com/jcsvwinston/quark/drivers/mysql"
)

// MySQL is measured on ONE operation, FindByPK, because that is where the
// protocol shows: go-sql-driver/mysql, given arguments and no
// interpolateParams, prepares the statement on the server, executes it and
// closes it — three packets and two round trips for every query. A program
// that prepares once and reuses the *sql.Stmt pays one round trip. Quark
// prepares nothing, so every Find pays the first price.
//
// The fourth arm sets interpolateParams=true on quark's DSN: the driver then
// writes the arguments into the SQL text itself and sends one query. It is a
// DSN setting an application can choose today, so the bench shows what it
// buys; it is not a baseline, and no verdict is judged against it.

const (
	armSQLStmt          = "database/sql, statement reused"
	armQuarkInterpolate = "quark, interpolateParams=true"
)

const mySchema = `
DROP TABLE IF EXISTS bench_users;
CREATE TABLE bench_users (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, email VARCHAR(128) NOT NULL, age INT NOT NULL, active BOOLEAN NOT NULL);
INSERT INTO bench_users (name, email, age, active)
  WITH RECURSIVE g(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM g WHERE n < 999)
  SELECT CONCAT('user', LPAD(n, 7, '0')), CONCAT('user', LPAD(n, 7, '0'), '@example.com'), 18 + n % 50, n % 2 = 0 FROM g;
ANALYZE TABLE bench_users;
`

const myFindSQL = `SELECT id, name, email, age, active FROM bench_users WHERE id = ?`

// withParam appends a DSN parameter, whether or not the DSN already has a
// query string.
func withParam(dsn, kv string) string {
	if strings.Contains(dsn, "?") {
		return dsn + "&" + kv
	}
	return dsn + "?" + kv
}

func setupMySQL(tb testing.TB, dsn string) {
	tb.Helper()
	db := mustOpen(tb, "mysql", withParam(dsn, "multiStatements=true"))
	defer db.Close()
	waitReady(tb, db, "mysql")
	if _, err := db.Exec(mySchema); err != nil {
		tb.Fatalf("mysql schema: %v", err)
	}
}

func openMySQL(tb testing.TB, dsn string) *sql.DB {
	db := mustOpen(tb, "mysql", dsn)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	tb.Cleanup(func() { _ = db.Close() })
	return db
}

func myOperations() []*operation {
	return []*operation{
		{
			engine: "mysql", name: "FindByPK",
			what: "select one row by primary key",
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openMySQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						var u benchUser
						return db.QueryRowContext(ctx, myFindSQL, int64(i%seedUsers)+1).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active)
					}
				}},
				{armSQLStmt, func(tb testing.TB, dsn string) stepFunc {
					db := openMySQL(tb, dsn)
					st, err := db.PrepareContext(context.Background(), myFindSQL)
					if err != nil {
						tb.Fatalf("prepare: %v", err)
					}
					tb.Cleanup(func() { _ = st.Close() })
					return func(ctx context.Context, i int) error {
						var u benchUser
						return st.QueryRowContext(ctx, int64(i%seedUsers)+1).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "mysql", dsn)
					return func(ctx context.Context, i int) error {
						_, err := quark.For[benchUser](ctx, c).Find(int64(i%seedUsers) + 1)
						return err
					}
				}},
				{armQuarkInterpolate, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "mysql", withParam(dsn, "interpolateParams=true"))
					return func(ctx context.Context, i int) error {
						_, err := quark.For[benchUser](ctx, c).Find(int64(i%seedUsers) + 1)
						return err
					}
				}},
			},
		},
	}
}
