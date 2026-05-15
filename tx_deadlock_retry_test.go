// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// pgDeadlock returns the pgconn error that isDeadlock recognises as a
// PG 40P01 deadlock. The retry tests fabricate it directly instead of
// trying to provoke a real cross-engine deadlock — that is left to
// the integration suite and is genuinely hard to make deterministic.
func pgDeadlock() error { return &pgconn.PgError{Code: "40P01"} }

// newDeadlockClient returns a SQLite client wired with the requested
// retry budget. The dialect is irrelevant: the retry loop runs against
// whatever transaction closure the caller supplies, and the closure in
// these tests returns errors directly without ever issuing SQL.
func newDeadlockClient(t *testing.T, maxAttempts int) *quark.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	opts := []any{}
	if maxAttempts > 0 {
		opts = append(opts, quark.WithDeadlockRetry(maxAttempts))
	}
	c, err := quark.New("sqlite", dsn, opts...)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestTx_NoRetryByDefault pins the historical contract: without
// WithDeadlockRetry, a deadlock from the closure propagates immediately
// on the first attempt. The retry mechanism must be fully opt-in.
func TestTx_NoRetryByDefault(t *testing.T) {
	c := newDeadlockClient(t, 0)

	var attempts int
	err := c.Tx(context.Background(), func(tx *quark.Tx) error {
		attempts++
		return pgDeadlock()
	})
	if err == nil {
		t.Fatal("expected the deadlock to propagate, got nil")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry without WithDeadlockRetry)", attempts)
	}
}

// TestTx_RetryEventuallyCommits is the F4-7 headline: with retry
// configured, a closure that deadlocks once then succeeds commits on
// the second attempt.
func TestTx_RetryEventuallyCommits(t *testing.T) {
	c := newDeadlockClient(t, 3)

	var attempts int
	err := c.Tx(context.Background(), func(tx *quark.Tx) error {
		attempts++
		if attempts < 3 {
			return pgDeadlock()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected commit after retries, got: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (two retries then success)", attempts)
	}
}

// TestTx_RetryExhausted surfaces a wrapped error after maxAttempts
// hits of deadlock — the caller still sees the original (last) tx
// error via errors.Is on the unwrap chain.
func TestTx_RetryExhausted(t *testing.T) {
	c := newDeadlockClient(t, 2)

	var attempts int
	err := c.Tx(context.Background(), func(tx *quark.Tx) error {
		attempts++
		return pgDeadlock()
	})
	if err == nil {
		t.Fatal("expected wrapped deadlock-exhausted error, got nil")
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (maxAttempts)", attempts)
	}
	// The wrapped pg error should still be reachable via Unwrap.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Errorf("expected unwrap to reach pg 40P01, got: %v", err)
	}
}

// TestTx_NonDeadlockErrorPropagatesImmediately: only deadlocks trigger
// a retry. Any other error short-circuits and propagates on the first
// attempt — the retry budget is irrelevant for non-deadlock failures.
func TestTx_NonDeadlockErrorPropagatesImmediately(t *testing.T) {
	c := newDeadlockClient(t, 5)

	plain := errors.New("not a deadlock")
	var attempts int
	err := c.Tx(context.Background(), func(tx *quark.Tx) error {
		attempts++
		return plain
	})
	if !errors.Is(err, plain) {
		t.Errorf("err = %v, want %v wrapped (or equal)", err, plain)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on non-deadlock)", attempts)
	}
}

// TestTx_CancelledContextAbortsBackoff: a context cancelled during the
// backoff between attempts surfaces as an error and stops further
// retries. The caller stays in control of the retry budget.
func TestTx_CancelledContextAbortsBackoff(t *testing.T) {
	c := newDeadlockClient(t, 5)

	// Cancel almost immediately so the first backoff window (10ms ±
	// jitter) is interrupted.
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	var attempts int
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		attempts++
		return pgDeadlock()
	})
	if err == nil {
		t.Fatal("expected an error after cancellation, got nil")
	}
	// At least one attempt happens before the first backoff. After the
	// first backoff window is hit by the cancel, no more attempts.
	if attempts < 1 || attempts > 2 {
		t.Errorf("attempts = %d, want 1 or 2 (cancel during backoff)", attempts)
	}
}
