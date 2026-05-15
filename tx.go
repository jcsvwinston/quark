// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"time"
)

// Executor is the common interface for *sql.DB and *sql.Tx.
// It allows Query[T] to execute against either a raw connection or a transaction.
type Executor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Tx wraps *sql.Tx and provides transactional query execution.
// It shares dialect, guard, observers, and limits from the parent Client.
type Tx struct {
	tx     *sql.Tx
	client *Client
}

// BeginTx starts a new database transaction with the given options.
//
// Example:
//
//	tx, err := client.BeginTx(ctx, nil)
//	if err != nil { log.Fatal(err) }
//	defer tx.Rollback()
//
//	quark.ForTx[User](ctx, tx).Create(&user)
//	tx.Commit()
func (c *Client) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	sqlTx, err := c.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	return &Tx{tx: sqlTx, client: c}, nil
}

// Tx executes fn within a transaction. If fn returns nil, the transaction
// is committed. If fn returns an error or panics, the transaction is rolled back.
//
// Example:
//
//	err := client.Tx(ctx, func(tx *quark.Tx) error {
//	    quark.ForTx[User](ctx, tx).Create(&user)
//	    quark.ForTx[Order](ctx, tx).Create(&order)
//	    return nil // auto-commit
//	})
//
// When WithDeadlockRetry(maxAttempts) is configured (F4-7), a deadlock
// raised from inside fn triggers a fresh-transaction retry with
// exponential backoff + jitter, up to maxAttempts total attempts.
// Non-deadlock errors propagate immediately. Disabled by default —
// callers explicitly opt in to retry semantics.
func (c *Client) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	maxAttempts := c.deadlockRetries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			if err := waitDeadlockBackoff(ctx, attempt-1); err != nil {
				return fmt.Errorf("deadlock retry aborted: %w (last tx error: %v)", err, lastErr)
			}
			if c.logger != nil {
				c.logger.Warn("transaction retry after deadlock",
					"attempt", attempt, "max_attempts", maxAttempts)
			}
		}

		err := c.runTxOnce(ctx, fn)
		if err == nil {
			return nil
		}
		if !isDeadlock(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("deadlock retry exhausted after %d attempts: %w", maxAttempts, lastErr)
}

// runTxOnce executes fn inside a fresh transaction exactly once,
// committing on success and rolling back on error or panic. This is
// the historical Client.Tx behaviour, lifted into its own function so
// the retry loop above can re-invoke it.
func (c *Client) runTxOnce(ctx context.Context, fn func(tx *Tx) error) error {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p) // re-panic after rollback
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("rollback failed: %v (original error: %w)", rbErr, err)
		}
		return err
	}

	return tx.Commit()
}

// waitDeadlockBackoff sleeps with exponential backoff and ±50% jitter
// between deadlock-retry attempts. Capped at 1s so an unbounded retry
// loop with high maxAttempts can't stall a request indefinitely.
// Returns the context error if the context is cancelled while waiting.
//
// attemptIdx is the 1-based index of the gap (1 means before attempt 2,
// 2 before attempt 3, etc.): the base wait doubles every retry
// (10ms → 20ms → 40ms → 80ms → 160ms → 320ms → 640ms → 1s cap),
// each shifted by uniform jitter into [base/2, 3·base/2). Beyond the
// seventh gap (`shift == 7` → 1.28s pre-cap) the 1s cap engages and
// stays there.
func waitDeadlockBackoff(ctx context.Context, attemptIdx int) error {
	if attemptIdx < 1 {
		attemptIdx = 1
	}
	shift := attemptIdx - 1
	if shift > 7 {
		// One step past the value that engages the 1s cap below — any
		// higher would only make the shift loop overflow without
		// changing the (capped) result.
		shift = 7
	}
	base := 10 * time.Millisecond * (1 << shift)
	if base > time.Second {
		base = time.Second
	}
	// math/rand/v2 is goroutine-safe; no locking required.
	jitter := time.Duration(rand.Float64() * float64(base))
	wait := base/2 + jitter
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// Commit commits the transaction.
func (t *Tx) Commit() error {
	return t.tx.Commit()
}

// Rollback aborts the transaction.
func (t *Tx) Rollback() error {
	return t.tx.Rollback()
}

// Savepoint creates a savepoint with the given name.
func (t *Tx) Savepoint(name string) error {
	if err := t.client.guard.ValidateIdentifier(name); err != nil {
		return err
	}
	_, err := t.tx.Exec("SAVEPOINT " + t.client.dialect.Quote(name))
	return err
}

// RollbackTo rolls back to the named savepoint.
func (t *Tx) RollbackTo(name string) error {
	if err := t.client.guard.ValidateIdentifier(name); err != nil {
		return err
	}
	_, err := t.tx.Exec("ROLLBACK TO SAVEPOINT " + t.client.dialect.Quote(name))
	return err
}

// ReleaseSavepoint releases the named savepoint.
func (t *Tx) ReleaseSavepoint(name string) error {
	if err := t.client.guard.ValidateIdentifier(name); err != nil {
		return err
	}
	_, err := t.tx.Exec("RELEASE SAVEPOINT " + t.client.dialect.Quote(name))
	return err
}

func (t *Tx) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	spName := fmt.Sprintf("sp_%d", time.Now().UnixNano())
	if err := t.Savepoint(spName); err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = t.RollbackTo(spName)
			panic(p)
		}
	}()

	if err := fn(t); err != nil {
		if rbErr := t.RollbackTo(spName); rbErr != nil {
			return fmt.Errorf("rollback to savepoint failed: %v (original error: %w)", rbErr, err)
		}
		return err
	}

	return t.ReleaseSavepoint(spName)
}

// ForTx creates a Query builder for the given model type bound to a transaction.
// This is the transactional counterpart of For[T]().
//
// Example:
//
//	err := client.Tx(ctx, func(tx *quark.Tx) error {
//	    return quark.ForTx[User](ctx, tx).Create(&user)
//	})
func ForTx[T any](ctx context.Context, tx *Tx) *Query[T] {
	meta := GetModelMeta[T]()

	return &Query[T]{
		BaseQuery: BaseQuery{
			ctx:     ctx,
			client:  tx.client,
			dialect: tx.client.dialect,
			guard:   tx.client.guard,
			table:   meta.Table,
			pk:      meta.PK,
			exec:    tx.tx,
			meta:    meta,
		},
	}
}
