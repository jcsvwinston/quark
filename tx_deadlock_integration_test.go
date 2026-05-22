// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type deadlockRow struct {
	ID  int64 `db:"id" pk:"true"`
	Val int   `db:"val"`
}

// runDeadlockRetryRecovers provokes a *real* engine deadlock and asserts
// that WithDeadlockRetry lets the chosen victim recover. Two
// transactions take the same two row locks in opposite order; once both
// hold their first lock (enforced by a barrier) the second acquisition
// crosses and the engine aborts one transaction as the deadlock victim
// (PG 40P01 / MySQL+MariaDB 1213). The victim's whole closure retries
// and, with the winner now committed, succeeds — so both Tx calls
// return nil and the total attempt count exceeds two.
//
// This is the F4-7 follow-up the unit tests in tx_deadlock_retry_test.go
// faked with a fabricated pgconn error; here the deadlock SQLSTATE comes
// from the engine itself. SQLite is excluded — it is single-writer and
// cannot deadlock. MSSQL/Oracle deadlock determinism depends on lock
// hints / timeouts and is left out; db_errors_test.go already pins
// their classifier codes (1205 / ORA-00060) by table test.
func runDeadlockRetryRecovers(t *testing.T, driver, dsn string) {
	t.Helper()

	client, err := quark.New(driver, dsn, quark.WithDeadlockRetry(5))
	if err != nil {
		t.Fatalf("quark.New(%s): %v", driver, err)
	}
	defer client.Close()

	ctx := context.Background()
	dropTable(client, "deadlock_rows")
	if err := client.Migrate(ctx, &deadlockRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, "deadlock_rows")

	// Seed two rows and capture their engine-assigned PKs (avoids any
	// identity/auto-increment surprise from inserting explicit IDs).
	a, b := &deadlockRow{}, &deadlockRow{}
	if err := quark.For[deadlockRow](ctx, client).Create(a); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	if err := quark.For[deadlockRow](ctx, client).Create(b); err != nil {
		t.Fatalf("seed b: %v", err)
	}

	// Barrier: both workers grab their first row lock, then meet here so
	// the second acquisition is guaranteed to cross and deadlock. It
	// fires once per worker — on a retry the loser must not block again,
	// because the winner has already committed and released its locks.
	var firstLockReady sync.WaitGroup
	firstLockReady.Add(2)
	var barrier [2]sync.Once

	lockRow := func(ctx context.Context, tx *quark.Tx, id int64) error {
		_, err := quark.ForTx[deadlockRow](ctx, tx).Where("id", "=", id).ForUpdate().List()
		return err
	}

	var attempts [2]int32
	run := func(idx int, first, second int64) error {
		return client.Tx(ctx, func(tx *quark.Tx) error {
			atomic.AddInt32(&attempts[idx], 1)
			if err := lockRow(ctx, tx, first); err != nil {
				return err
			}
			barrier[idx].Do(func() {
				firstLockReady.Done()
				firstLockReady.Wait()
			})
			return lockRow(ctx, tx, second)
		})
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = run(0, a.ID, b.ID) }()
	go func() { defer wg.Done(); errs[1] = run(1, b.ID, a.ID) }()
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("worker %d: expected commit after deadlock retry, got %v", i, e)
		}
	}
	total := atomic.LoadInt32(&attempts[0]) + atomic.LoadInt32(&attempts[1])
	if total < 3 {
		t.Errorf("total attempts = %d, want >= 3 (one worker must have been the deadlock victim and retried)", total)
	}
}

func TestDeadlockRetryPostgres(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}
	runDeadlockRetryRecovers(t, "pgx", dsn)
}

func TestDeadlockRetryMySQL(t *testing.T) {
	dsn := resolveMySQLDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_MYSQL_DSN not set (rebuild with -tags=integration to spin up a container)")
	}
	runDeadlockRetryRecovers(t, "mysql", dsn)
}

func TestDeadlockRetryMariaDB(t *testing.T) {
	dsn := resolveMariaDBDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_MARIADB_DSN not set (rebuild with -tags=integration to spin up a container)")
	}
	runDeadlockRetryRecovers(t, "mysql", dsn)
}
