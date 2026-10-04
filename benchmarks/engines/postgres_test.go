// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jcsvwinston/quark"

	// pgx's database/sql driver, registered as "pgx" — exactly what the
	// quark/drivers/postgres module imports, and all it imports. The bench
	// names the engine library rather than the driver module on purpose: a
	// driver module requires the library at the floor the release train raises,
	// so depending on one would leave this module's go.mod stale after every
	// raise and the smoke lane red until someone tidied it.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The data set: 1000 users with 5 posts each, read by FindByPK, List100 and
// Preload100; the write operations insert into a table of their own that is
// emptied before every sample.
const (
	seedUsers    = 1000
	postsPerUser = 5
	listN        = 100
	batchN       = 1000
	minAge       = 18
)

// quiet keeps the bench's output to its own table.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// benchUser is the read model: five columns, one of each kind a row usually
// carries (key, two strings, an integer, a boolean).
type benchUser struct {
	ID     int64  `db:"id" pk:"true"`
	Name   string `db:"name"`
	Email  string `db:"email"`
	Age    int    `db:"age"`
	Active bool   `db:"active"`
}

func (benchUser) TableName() string { return "bench_users" }

// benchUserW is the same row in the table the write operations insert into.
type benchUserW struct {
	ID     int64  `db:"id" pk:"true"`
	Name   string `db:"name"`
	Email  string `db:"email"`
	Age    int    `db:"age"`
	Active bool   `db:"active"`
}

func (benchUserW) TableName() string { return "bench_users_w" }

type benchPost struct {
	ID     int64  `db:"id" pk:"true"`
	UserID int64  `db:"user_id"`
	Title  string `db:"title"`
}

func (benchPost) TableName() string { return "bench_posts" }

// userWithPosts is the parent of Preload100: the user row and its has-many.
type userWithPosts struct {
	ID     int64       `db:"id" pk:"true"`
	Name   string      `db:"name"`
	Email  string      `db:"email"`
	Age    int         `db:"age"`
	Active bool        `db:"active"`
	Posts  []benchPost `rel:"has_many" join:"user_id"`
}

func (userWithPosts) TableName() string { return "bench_users" }

// userPosts is what the baselines assemble Preload100 into by hand.
type userPosts struct {
	benchUser
	Posts []benchPost
}

func mkUser(i int) benchUserW {
	return benchUserW{
		Name:   fmt.Sprintf("user%07d", i),
		Email:  fmt.Sprintf("user%07d@example.com", i),
		Age:    minAge + i%50,
		Active: i%2 == 0,
	}
}

const pgSchema = `
DROP TABLE IF EXISTS bench_posts;
DROP TABLE IF EXISTS bench_users;
DROP TABLE IF EXISTS bench_users_w;
CREATE TABLE bench_users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, age INTEGER NOT NULL, active BOOLEAN NOT NULL);
CREATE TABLE bench_users_w (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL, age INTEGER NOT NULL, active BOOLEAN NOT NULL);
CREATE TABLE bench_posts (id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES bench_users(id), title TEXT NOT NULL);
CREATE INDEX bench_posts_user ON bench_posts(user_id);
INSERT INTO bench_users (name, email, age, active)
  SELECT 'user' || lpad(g::text, 7, '0'), 'user' || lpad(g::text, 7, '0') || '@example.com', 18 + g % 50, g % 2 = 0
  FROM generate_series(0, 999) g;
INSERT INTO bench_posts (user_id, title) SELECT u.id, 'post ' || p FROM bench_users u, generate_series(1, 5) p;
ANALYZE;
`

// setupPostgres creates and seeds the data set. It runs once per test
// binary, before any operation is measured.
func setupPostgres(tb testing.TB, dsn string) {
	tb.Helper()
	db := mustOpen(tb, "pgx", dsn)
	defer db.Close()
	waitReady(tb, db, "postgres")
	if _, err := db.Exec(pgSchema); err != nil {
		tb.Fatalf("postgres schema: %v", err)
	}
}

const pgTruncate = `TRUNCATE bench_users_w RESTART IDENTITY`

// --- the three ways to open PostgreSQL ------------------------------------------

// openPgSQL is database/sql over pgx's stdlib driver: what a program that
// writes its SQL by hand, and wants the database/sql interface, gets.
func openPgSQL(tb testing.TB, dsn string) *sql.DB {
	db := mustOpen(tb, "pgx", dsn)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	tb.Cleanup(func() { _ = db.Close() })
	return db
}

// openPgx is pgx's own pool: no database/sql in between.
func openPgx(tb testing.TB, dsn string) *pgxpool.Pool {
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		tb.Fatalf("pgxpool: %v", err)
	}
	tb.Cleanup(p.Close)
	return p
}

// openQuark is a quark client as an application builds one: the default
// options, a logger that discards, and the same pool size as database/sql.
func openQuark(tb testing.TB, driver, dsn string) *quark.Client {
	c, err := quark.New(driver, dsn, quark.WithLogger(quiet), quark.WithMaxOpenConns(4))
	if err != nil {
		tb.Fatalf("quark.New(%s): %v", driver, err)
	}
	tb.Cleanup(func() { _ = c.Close() })
	return c
}

// --- the operations ---------------------------------------------------------------

const (
	armSQL   = "database/sql"
	armPgx   = "pgx"
	armQuark = "quark"
	// armSQLInList is informational: no control is judged against it.
	armSQLInList = "database/sql, quark's IN list"
)

const (
	pgInsertSQL = `INSERT INTO bench_users_w (name, email, age, active) VALUES ($1, $2, $3, $4) RETURNING id`
	pgFindSQL   = `SELECT id, name, email, age, active FROM bench_users WHERE id = $1`
	pgListSQL   = `SELECT id, name, email, age, active FROM bench_users WHERE age >= $1 ORDER BY id ASC LIMIT 100`
	pgPostsSQL  = `SELECT id, user_id, title FROM bench_posts WHERE user_id = ANY($1)`
)

// pgPostsInList is the children's query in the shape quark's preload sends
// on PostgreSQL: every column, and one placeholder per parent.
var pgPostsInList = func() string {
	var b strings.Builder
	b.WriteString(`SELECT * FROM "bench_posts" WHERE "user_id" IN (`)
	for k := 1; k <= listN; k++ {
		if k > 1 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "$%d", k)
	}
	b.WriteString(")")
	return b.String()
}()

// pgBatchSQL is the multi-row INSERT the baselines send for InsertBatch1000:
// one statement, 4000 parameters, the ids back through RETURNING — the same
// shape quark's CreateBatch sends on PostgreSQL.
var pgBatchSQL = func() string {
	var b strings.Builder
	b.WriteString("INSERT INTO bench_users_w (name, email, age, active) VALUES ")
	for r := 0; r < batchN; r++ {
		if r > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "($%d, $%d, $%d, $%d)", r*4+1, r*4+2, r*4+3, r*4+4)
	}
	b.WriteString(" RETURNING id")
	return b.String()
}()

// rowScanner is what both baselines' result sets share, so the hand-written
// scanning below is written once for both.
type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}

type sqlRows struct{ *sql.Rows }

func (r sqlRows) Close() { _ = r.Rows.Close() }

func scanUsers(rows rowScanner, out []userPosts, idx map[int64]int) ([]userPosts, []int64, error) {
	ids := make([]int64, 0, listN)
	for rows.Next() {
		var u userPosts
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active); err != nil {
			rows.Close()
			return nil, nil, err
		}
		idx[u.ID] = len(out)
		ids = append(ids, u.ID)
		out = append(out, u)
	}
	rows.Close()
	return out, ids, rows.Err()
}

func scanPosts(rows rowScanner, out []userPosts, idx map[int64]int) (int, error) {
	n := 0
	for rows.Next() {
		var p benchPost
		if err := rows.Scan(&p.ID, &p.UserID, &p.Title); err != nil {
			rows.Close()
			return 0, err
		}
		k := idx[p.UserID]
		out[k].Posts = append(out[k].Posts, p)
		n++
	}
	rows.Close()
	return n, rows.Err()
}

func scanList(rows rowScanner) (int, error) {
	out := make([]benchUser, 0, listN)
	for rows.Next() {
		var u benchUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active); err != nil {
			rows.Close()
			return 0, err
		}
		out = append(out, u)
	}
	rows.Close()
	return len(out), rows.Err()
}

func batchArgs(i int) ([]*benchUserW, []any) {
	users := make([]*benchUserW, batchN)
	args := make([]any, 0, batchN*4)
	for j := range users {
		u := mkUser(i*batchN + j)
		users[j] = &u
		args = append(args, u.Name, u.Email, u.Age, u.Active)
	}
	return users, args
}

func pgOperations() []*operation {
	return []*operation{
		{
			engine: "postgres", name: "InsertOne",
			what:  "insert one row and read its generated id back (RETURNING id)",
			reset: pgTruncate,
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openPgSQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						u := mkUser(i)
						return db.QueryRowContext(ctx, pgInsertSQL, u.Name, u.Email, u.Age, u.Active).Scan(&u.ID)
					}
				}},
				{armPgx, func(tb testing.TB, dsn string) stepFunc {
					p := openPgx(tb, dsn)
					return func(ctx context.Context, i int) error {
						u := mkUser(i)
						return p.QueryRow(ctx, pgInsertSQL, u.Name, u.Email, u.Age, u.Active).Scan(&u.ID)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "pgx", dsn)
					return func(ctx context.Context, i int) error {
						u := mkUser(i)
						if err := quark.For[benchUserW](ctx, c).Create(&u); err != nil {
							return err
						}
						if u.ID == 0 {
							return fmt.Errorf("Create left the id at zero")
						}
						return nil
					}
				}},
			},
		},
		{
			engine: "postgres", name: "FindByPK",
			what: "select one row by primary key",
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openPgSQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						var u benchUser
						return db.QueryRowContext(ctx, pgFindSQL, int64(i%seedUsers)+1).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active)
					}
				}},
				{armPgx, func(tb testing.TB, dsn string) stepFunc {
					p := openPgx(tb, dsn)
					return func(ctx context.Context, i int) error {
						var u benchUser
						return p.QueryRow(ctx, pgFindSQL, int64(i%seedUsers)+1).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "pgx", dsn)
					return func(ctx context.Context, i int) error {
						_, err := quark.For[benchUser](ctx, c).Find(int64(i%seedUsers) + 1)
						return err
					}
				}},
			},
		},
		{
			engine: "postgres", name: "List100",
			what: "select 100 rows with a WHERE, an ORDER BY and a LIMIT",
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openPgSQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						rows, err := db.QueryContext(ctx, pgListSQL, minAge)
						if err != nil {
							return err
						}
						return wantN(scanList(sqlRows{rows}))(listN)
					}
				}},
				{armPgx, func(tb testing.TB, dsn string) stepFunc {
					p := openPgx(tb, dsn)
					return func(ctx context.Context, i int) error {
						rows, err := p.Query(ctx, pgListSQL, minAge)
						if err != nil {
							return err
						}
						return wantN(scanList(rows))(listN)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "pgx", dsn)
					return func(ctx context.Context, i int) error {
						out, err := quark.For[benchUser](ctx, c).Where("age", ">=", minAge).OrderBy("id", "ASC").Limit(listN).List()
						return wantN(len(out), err)(listN)
					}
				}},
			},
		},
		{
			engine: "postgres", name: "Preload100",
			what: "select 100 parents and their 500 children, two queries, assembled into the parents",
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openPgSQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						rows, err := db.QueryContext(ctx, pgListSQL, minAge)
						if err != nil {
							return err
						}
						idx := make(map[int64]int, listN)
						out, ids, err := scanUsers(sqlRows{rows}, make([]userPosts, 0, listN), idx)
						if err != nil {
							return err
						}
						prows, err := db.QueryContext(ctx, pgPostsSQL, ids)
						if err != nil {
							return err
						}
						return wantN(scanPosts(sqlRows{prows}, out, idx))(listN * postsPerUser)
					}
				}},
				{armPgx, func(tb testing.TB, dsn string) stepFunc {
					p := openPgx(tb, dsn)
					return func(ctx context.Context, i int) error {
						rows, err := p.Query(ctx, pgListSQL, minAge)
						if err != nil {
							return err
						}
						idx := make(map[int64]int, listN)
						out, ids, err := scanUsers(rows, make([]userPosts, 0, listN), idx)
						if err != nil {
							return err
						}
						prows, err := p.Query(ctx, pgPostsSQL, ids)
						if err != nil {
							return err
						}
						return wantN(scanPosts(prows, out, idx))(listN * postsPerUser)
					}
				}},
				// Not a baseline: database/sql sending the children's query in
				// the shape quark sends it — SELECT * and an IN list of 100
				// placeholders — so the table separates what the query's shape
				// costs on the server from what quark's mapping costs in the
				// client.
				{armSQLInList, func(tb testing.TB, dsn string) stepFunc {
					db := openPgSQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						rows, err := db.QueryContext(ctx, pgListSQL, minAge)
						if err != nil {
							return err
						}
						idx := make(map[int64]int, listN)
						out, ids, err := scanUsers(sqlRows{rows}, make([]userPosts, 0, listN), idx)
						if err != nil {
							return err
						}
						args := make([]any, len(ids))
						for k, id := range ids {
							args[k] = id
						}
						prows, err := db.QueryContext(ctx, pgPostsInList, args...)
						if err != nil {
							return err
						}
						return wantN(scanPosts(sqlRows{prows}, out, idx))(listN * postsPerUser)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "pgx", dsn)
					return func(ctx context.Context, i int) error {
						out, err := quark.For[userWithPosts](ctx, c).Where("age", ">=", minAge).OrderBy("id", "ASC").Limit(listN).Preload("Posts").List()
						if err != nil {
							return err
						}
						n := 0
						for _, u := range out {
							n += len(u.Posts)
						}
						return wantN(n, nil)(listN * postsPerUser)
					}
				}},
			},
		},
		{
			engine: "postgres", name: "InsertBatch1000",
			what:  "insert 1000 rows in one statement and read the 1000 generated ids back",
			reset: pgTruncate,
			arms: []arm{
				{armSQL, func(tb testing.TB, dsn string) stepFunc {
					db := openPgSQL(tb, dsn)
					return func(ctx context.Context, i int) error {
						users, args := batchArgs(i)
						rows, err := db.QueryContext(ctx, pgBatchSQL, args...)
						if err != nil {
							return err
						}
						return scanIDs(sqlRows{rows}, users)
					}
				}},
				{armPgx, func(tb testing.TB, dsn string) stepFunc {
					p := openPgx(tb, dsn)
					return func(ctx context.Context, i int) error {
						users, args := batchArgs(i)
						rows, err := p.Query(ctx, pgBatchSQL, args...)
						if err != nil {
							return err
						}
						return scanIDs(rows, users)
					}
				}},
				{armQuark, func(tb testing.TB, dsn string) stepFunc {
					c := openQuark(tb, "pgx", dsn)
					return func(ctx context.Context, i int) error {
						users, _ := batchArgs(i)
						if err := quark.For[benchUserW](ctx, c).CreateBatch(users); err != nil {
							return err
						}
						if users[0].ID == 0 || users[batchN-1].ID == 0 {
							return fmt.Errorf("CreateBatch left an id at zero")
						}
						return nil
					}
				}},
			},
		},
	}
}

func scanIDs(rows rowScanner, users []*benchUserW) error {
	k := 0
	for rows.Next() {
		if k >= len(users) {
			rows.Close()
			return fmt.Errorf("more ids than rows")
		}
		if err := rows.Scan(&users[k].ID); err != nil {
			rows.Close()
			return err
		}
		k++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if k != len(users) {
		return fmt.Errorf("%d ids for %d rows", k, len(users))
	}
	return nil
}

// wantN turns "n results, err" into an error when the arm did not get the
// rows it was supposed to: an arm that silently returned less work would
// look faster than it is.
func wantN(n int, err error) func(want int) error {
	return func(want int) error {
		if err != nil {
			return err
		}
		if n != want {
			return fmt.Errorf("got %d rows, want %d", n, want)
		}
		return nil
	}
}
