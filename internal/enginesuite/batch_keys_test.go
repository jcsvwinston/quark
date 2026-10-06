// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jcsvwinston/quark"
)

// CreateBatch with a generated key, on every engine (QK-36, A12 Q3): every
// entity comes back with the key of ITS row, inside and outside a
// transaction, while other connections insert into the same table; and when a
// row fails, the caller sees on MySQL and SQL Server what the per-row form
// left — the rows before it inserted with their keys, the rest not — and on
// the engines with RETURNING what the single statement leaves: nothing.
//
// MySQL runs twice: on the server's default innodb_autoinc_lock_mode (2 on
// MySQL 8), where the keys of a multi-row INSERT are not documented to be
// consecutive and CreateBatch keeps one INSERT per row; and on a server
// started with innodb_autoinc_lock_mode=1, where they are, and a chunk goes
// in one INSERT. The statement count tells which form ran.

type batchKeyUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name,size=120" quark:"unique"`
	Age  int    `db:"age"`
}

func (batchKeyUser) TableName() string { return "qk36_batch_users" }

// insertCounter counts the INSERT statements into the batch table the engine
// received, leaving out the ones the concurrent writers send — the round
// trips CreateBatch made.
type insertCounter struct{ n atomic.Int64 }

func (c *insertCounter) ObserveQuery(e quark.QueryEvent) {
	up := strings.ToUpper(e.SQL)
	if e.Error != nil || !strings.Contains(up, "QK36_BATCH_USERS") || !(strings.Contains(up, "INSERT INTO") || strings.Contains(up, "MERGE INTO")) {
		return
	}
	if len(e.Args) > 0 {
		if s, ok := e.Args[0].(string); ok && strings.HasPrefix(s, "noise-") {
			return
		}
	}
	c.n.Add(1)
}

// chunks is how many statements a batch of n rows takes at rowsPerChunk rows
// each.
func chunks(n, rowsPerChunk int) int64 { return int64((n + rowsPerChunk - 1) / rowsPerChunk) }

// The batch model binds two columns (name, age), so a chunk holds the
// dialect's parameter budget over two: 32500 rows on PostgreSQL and MySQL,
// 16000 on SQLite, 1000 on SQL Server.
func TestCreateBatchKeysAllEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("engine test")
	}
	type leg struct {
		name    string
		env     string
		resolve func(*testing.T) string
		drv     string
		// perRowOnFailure: a failed row leaves the rows before it (MySQL and
		// SQL Server, as the per-row form always did); otherwise the chunk is
		// one statement and leaves nothing.
		perRowOnFailure bool
		// statements is how many INSERTs a batch of n rows takes.
		statements func(n int) int64
	}
	perRow := func(n int) int64 { return int64(n) }
	legs := []leg{
		{"SQLite", "", func(*testing.T) string { return "file:qk36?mode=memory&cache=shared" }, "sqlite", false, func(n int) int64 { return chunks(n, 16000) }},
		{"Postgres", "QUARK_TEST_POSTGRES_DSN", resolvePostgresDSN, "pgx", false, func(n int) int64 { return chunks(n, 32500) }},
		// The server's default lock mode: 2 on MySQL 8, one INSERT per row.
		{"MySQL", "QUARK_TEST_MYSQL_DSN", resolveMySQLDSN, "mysql", true, perRow},
		{"MariaDB", "QUARK_TEST_MARIADB_DSN", resolveMariaDBDSN, "mysql", false, func(n int) int64 { return chunks(n, 32500) }},
		{"MSSQL", "QUARK_TEST_MSSQL_DSN", resolveMSSQLDSN, "sqlserver", true, func(n int) int64 { return chunks(n, 1000) }},
		{"Oracle", "QUARK_TEST_ORACLE_DSN", resolveOracleDSN, "oracle", true, perRow},
	}
	for _, lg := range legs {
		t.Run(lg.name, func(t *testing.T) {
			dsn := lg.resolve(t)
			if dsn == "" {
				t.Skipf("%s not set (rebuild with -tags=integration to spin up a container); %s leg skipped", lg.env, lg.name)
			}
			testCreateBatchKeys(t, lg.drv, dsn, lg.perRowOnFailure, lg.statements)
			if lg.name == "MySQL" {
				t.Run("innodb_autoinc_lock_mode=1", func(t *testing.T) {
					dsn := resolveMySQLConsecutiveDSN(t)
					if dsn == "" {
						t.Skip("QUARK_TEST_MYSQL_AUTOINC1_DSN not set (rebuild with -tags=integration to spin up a container)")
					}
					// Chunks of five rows or more in one INSERT; smaller ones
					// row by row, where that is fewer round trips.
					testCreateBatchKeys(t, lg.drv, dsn, true, func(n int) int64 {
						if n < 5 {
							return int64(n)
						}
						return chunks(n, 32500)
					})
				})
			}
		})
	}
}

// testCreateBatchKeys runs the checks on one server.
func testCreateBatchKeys(t *testing.T, drv, dsn string, perRowOnFailure bool, statements func(n int) int64) {
	ctx := context.Background()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	counter := &insertCounter{}
	c, err := quark.New(drv, dsn, quark.WithLimits(limits), quark.WithQueryObserver(counter), quark.WithMaxOpenConns(8))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.Exec(ctx, "DROP TABLE qk36_batch_users")
	if err := c.Migrate(ctx, &batchKeyUser{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Exec(context.Background(), "DROP TABLE qk36_batch_users") })

	// The keys come back right while other connections insert into the same
	// table: under innodb_autoinc_lock_mode=2 this is where a run of keys
	// would interleave.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var noise atomic.Int64
	writers := 3
	if drv == "sqlite" {
		writers = 0 // one writer at a time: SQLite would answer SQLITE_BUSY
	}
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				u := batchKeyUser{Name: fmt.Sprintf("noise-%d-%d", w, i), Age: -1}
				if err := quark.For[batchKeyUser](ctx, c).Create(&u); err != nil {
					t.Errorf("concurrent Create: %v", err)
					return
				}
				noise.Add(1)
			}
		}(w)
	}

	sizes := []int{1, 4, 5, 37, 2500}
	seen := map[int64]string{}
	for _, n := range sizes {
		users := make([]*batchKeyUser, n)
		for i := range users {
			users[i] = &batchKeyUser{Name: fmt.Sprintf("b%d-%04d", n, i), Age: i % 90}
		}
		before := counter.n.Load()
		if err := quark.For[batchKeyUser](ctx, c).CreateBatch(users); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("CreateBatch of %d: %v", n, err)
		}
		inserts := counter.n.Load() - before
		for _, u := range users {
			if u.ID == 0 {
				t.Fatalf("batch of %d: %s came back without a key", n, u.Name)
			}
			if prev, dup := seen[u.ID]; dup {
				t.Fatalf("batch of %d: key %d handed to %s and to %s", n, u.ID, prev, u.Name)
			}
			seen[u.ID] = u.Name
		}
		if want := statements(n); inserts != want {
			t.Errorf("batch of %d took %d INSERT statements, want %d", n, inserts, want)
		}
	}
	close(stop)
	wg.Wait()
	// Every key reads back the row it was handed for.
	for id, name := range seen {
		got, err := quark.For[batchKeyUser](ctx, c).Find(id)
		if err != nil {
			t.Fatalf("Find(%d): %v", id, err)
		}
		if got.Name != name {
			t.Fatalf("key %d was handed to %s, but its row is %s", id, name, got.Name)
		}
	}
	t.Logf("%d keys checked against their rows, %d concurrent inserts", len(seen), noise.Load())

	// In a transaction: rolled back, the rows are gone; committed, they are
	// there with their keys.
	for _, commit := range []bool{false, true} {
		users := make([]*batchKeyUser, 12)
		for i := range users {
			users[i] = &batchKeyUser{Name: fmt.Sprintf("tx-%v-%02d", commit, i), Age: i}
		}
		err := c.Tx(ctx, func(tx *quark.Tx) error {
			if err := quark.ForTx[batchKeyUser](ctx, tx).CreateBatch(users); err != nil {
				return err
			}
			for _, u := range users {
				got, err := quark.ForTx[batchKeyUser](ctx, tx).Find(u.ID)
				if err != nil || got.Name != u.Name {
					return fmt.Errorf("inside the transaction key %d reads %+v (%v), want %s", u.ID, got, err, u.Name)
				}
			}
			if !commit {
				return errRollbackOnPurpose
			}
			return nil
		})
		if commit && err != nil {
			t.Fatal(err)
		}
		if !commit && err != errRollbackOnPurpose {
			t.Fatalf("rolled-back transaction: %v", err)
		}
		n, err := quark.For[batchKeyUser](ctx, c).Where("name", "LIKE", fmt.Sprintf("tx-%v-%%", commit)).Count()
		if err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int64{false: 0, true: 12}[commit]; n != want {
			t.Fatalf("after the transaction (commit=%v) %d rows are left, want %d", commit, n, want)
		}
	}

	// A row that fails: the sixth duplicates a name already taken.
	failing := func(prefix string) []*batchKeyUser {
		users := make([]*batchKeyUser, 10)
		for i := range users {
			users[i] = &batchKeyUser{Name: fmt.Sprintf("%s-%02d", prefix, i), Age: i}
		}
		users[5].Name = "b1-0000"
		return users
	}
	// wantFailure checks the error of a batch whose sixth row is rejected.
	// On SQL Server the per-row form reports it as a failed scan of a NULL
	// SCOPE_IDENTITY() rather than as the engine's error — single Create was
	// fixed for this (it scans a NullInt64), CreateBatch's per-row form was
	// not. That predates A12 Q3 and is reported, not changed, here: the
	// rows and keys it leaves are what this test pins.
	wantFailure := func(err error) error {
		switch {
		case err == nil:
			return fmt.Errorf("a batch with a duplicate name succeeded")
		case drv == "sqlserver":
			return nil
		case !quark.IsUniqueViolation(err):
			return fmt.Errorf("a batch with a duplicate name: %v, want a unique violation", err)
		}
		return nil
	}
	users := failing("fail")
	if err := wantFailure(quark.For[batchKeyUser](ctx, c).CreateBatch(users)); err != nil {
		t.Fatal(err)
	}
	left, err := quark.For[batchKeyUser](ctx, c).Where("name", "LIKE", "fail-%").Count()
	if err != nil {
		t.Fatal(err)
	}
	if perRowOnFailure {
		if left != 5 {
			t.Fatalf("after the failed batch %d of its rows are in the table; the per-row form leaves the 5 before the failing one", left)
		}
		for i, u := range users {
			if (i < 5) != (u.ID != 0) {
				t.Fatalf("after the failed batch row %d has key %d; the rows before the failing one carry theirs, the rest none", i, u.ID)
			}
		}
	} else if left != 0 {
		t.Fatalf("after the failed batch %d of its rows are in the table; one statement leaves none", left)
	}

	// The same failure inside a transaction leaves the transaction usable on
	// MySQL and SQL Server, with the rows before the failing one in it.
	users = failing("txfail")
	err = c.Tx(ctx, func(tx *quark.Tx) error {
		if err := wantFailure(quark.ForTx[batchKeyUser](ctx, tx).CreateBatch(users)); err != nil {
			return fmt.Errorf("inside a transaction: %w", err)
		}
		if !perRowOnFailure {
			return errRollbackOnPurpose
		}
		n, err := quark.ForTx[batchKeyUser](ctx, tx).Where("name", "LIKE", "txfail-%").Count()
		if err != nil {
			return err
		}
		if n != 5 {
			return fmt.Errorf("inside the transaction %d rows of the failed batch are visible, want 5", n)
		}
		return errRollbackOnPurpose
	})
	if err != errRollbackOnPurpose {
		t.Fatal(err)
	}
}

var errRollbackOnPurpose = fmt.Errorf("rolled back on purpose")
