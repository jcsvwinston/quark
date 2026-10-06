// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"

	// go-sql-driver/mysql, registered as "mysql". The quark/drivers/mysql
	// module imports it and adds an error classifier, which quark consults
	// only when a statement fails — never on the path measured here. Named
	// directly for the reason given next to the pgx import.
	_ "github.com/go-sql-driver/mysql"
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
	// armQuarkNoCache is informational: quark as MY-01 measures it, next to
	// quark with WithStatementCache, so MY-02's table shows what the option
	// buys within one round.
	armQuarkNoCache = "quark, no statement cache"
	// armSQLPerRow is informational: one INSERT per row, which is what
	// CreateBatch sends where the keys of a multi-row INSERT are not
	// documented to be consecutive (innodb_autoinc_lock_mode=2).
	armSQLPerRow = "database/sql, one INSERT per row"
)

// myStmtCacheSize is the cache MY-02 measures quark with: room for every
// statement the operation sends, so what it measures is reuse, not eviction.
const myStmtCacheSize = 64

const mySchema = `
DROP TABLE IF EXISTS bench_users;
DROP TABLE IF EXISTS bench_users_w;
CREATE TABLE bench_users (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, email VARCHAR(128) NOT NULL, age INT NOT NULL, active BOOLEAN NOT NULL);
CREATE TABLE bench_users_w (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, email VARCHAR(128) NOT NULL, age INT NOT NULL, active BOOLEAN NOT NULL);
INSERT INTO bench_users (name, email, age, active)
  WITH RECURSIVE g(n) AS (SELECT 0 UNION ALL SELECT n + 1 FROM g WHERE n < 999)
  SELECT CONCAT('user', LPAD(n, 7, '0')), CONCAT('user', LPAD(n, 7, '0'), '@example.com'), 18 + n % 50, n % 2 = 0 FROM g;
ANALYZE TABLE bench_users;
`

const myFindSQL = `SELECT id, name, email, age, active FROM bench_users WHERE id = ?`

const (
	myTruncate  = `TRUNCATE bench_users_w`
	myInsertSQL = `INSERT INTO bench_users_w (name, email, age, active) VALUES (?, ?, ?, ?)`
)

// myBatchSQL is the multi-row INSERT a hand-written program sends for
// InsertBatch1000 on MySQL: one statement, 4000 parameters, the keys
// computed from LAST_INSERT_ID() — the shape quark sends where MySQL
// documents that they are consecutive.
var myBatchSQL = func() string {
	var b strings.Builder
	b.WriteString("INSERT INTO bench_users_w (name, email, age, active) VALUES ")
	for r := 0; r < batchN; r++ {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(?, ?, ?, ?)")
	}
	return b.String()
}()

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

// openQuarkMySQL is a quark client on MySQL with extra options.
func openQuarkMySQL(tb testing.TB, dsn string, opts ...any) *quark.Client {
	opts = append([]any{quark.WithLogger(quiet), quark.WithMaxOpenConns(4)}, opts...)
	c, err := quark.New("mysql", dsn, opts...)
	if err != nil {
		tb.Fatalf("quark.New(mysql): %v", err)
	}
	tb.Cleanup(func() { _ = c.Close() })
	return c
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
		{
			engine: "mysql", name: "FindByPKStmtCache",
			what: "select one row by primary key, quark with WithStatementCache",
			arms: []arm{
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
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openMySQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						var u benchUser
						return db.QueryRowContext(ctx, myFindSQL, int64(i%seedUsers)+1).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuarkMySQL(tb, dsn, quark.WithStatementCache(myStmtCacheSize))
					return func(ctx context.Context, i int) error {
						_, err := quark.For[benchUser](ctx, c).Find(int64(i%seedUsers) + 1)
						return err
					}
				}},
				// Not a baseline: quark without the option, so the table
				// shows what it buys in the same rounds.
				{armQuarkNoCache, func(tb testing.TB, dsn string) stepFunc {
					c := openQuarkMySQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						_, err := quark.For[benchUser](ctx, c).Find(int64(i%seedUsers) + 1)
						return err
					}
				}},
			},
		},
		{
			engine: "mysql", name: "InsertBatch1000",
			what:  "insert 1000 rows and read the 1000 generated ids back (innodb_autoinc_lock_mode=1)",
			reset: myTruncate,
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openMySQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						users, args := batchArgs(i)
						res, err := db.ExecContext(ctx, myBatchSQL, args...)
						if err != nil {
							return err
						}
						first, err := res.LastInsertId()
						if err != nil {
							return err
						}
						for k, u := range users {
							u.ID = first + int64(k)
						}
						return nil
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuarkMySQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						users, _ := batchArgs(i)
						if err := quark.For[benchUserW](ctx, c).CreateBatch(users); err != nil {
							return err
						}
						if users[0].ID == 0 || users[batchN-1].ID != users[0].ID+batchN-1 {
							return fmt.Errorf("CreateBatch keys: first %d, last %d", users[0].ID, users[batchN-1].ID)
						}
						return nil
					}
				}},
				// Not a baseline: one INSERT per row, which is what quark sends
				// under innodb_autoinc_lock_mode=2, MySQL 8's default.
				{armSQLPerRow, func(tb testing.TB, dsn string) stepFunc {
					db := openMySQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						users, _ := batchArgs(i)
						for _, u := range users {
							res, err := db.ExecContext(ctx, myInsertSQL, u.Name, u.Email, u.Age, u.Active)
							if err != nil {
								return err
							}
							if u.ID, err = res.LastInsertId(); err != nil {
								return err
							}
						}
						return nil
					}
				}},
			},
		},
	}
}
