// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
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
	// ctx is the context the transaction was opened with (via
	// [Client.BeginTx] / [Client.Tx]). It is passed to the
	// [Tx.OnCommit] / [Tx.OnRollback] callbacks when they fire. If
	// the caller installed a deadline on it, the callbacks may
	// observe an expired context — they can derive a fresh one from
	// [context.Background] if needed.
	ctx context.Context

	// afterHooks is the FIFO queue of model `After*` hooks
	// ([AfterCreateHook], [AfterUpdateHook], [AfterDeleteHook]) that
	// were registered while this transaction was open. Commit drains
	// the queue once the underlying tx commit succeeds (F5-4);
	// Rollback discards it. This is what makes "After hooks fire
	// post-commit" semantically true without bleeding the database
	// state of a partially-applied tx into application code.
	//
	// onCommitHooks / onRollbackHooks are the F5-5 public side-effect
	// queues registered via [Tx.OnCommit] / [Tx.OnRollback]. On a
	// successful commit the drain order is: afterHooks (model-level,
	// ORM contract) first, then onCommitHooks (user-level
	// side-effects). On rollback, afterHooks and onCommitHooks are
	// discarded and onRollbackHooks fire. Unlike afterHooks (which
	// bake their own captured ctx), the OnCommit/OnRollback callbacks
	// receive Tx.ctx explicitly.
	//
	// Access goes through queueAfterHook / OnCommit / OnRollback and
	// the drain*/discard* helpers under hooksMu — Tx is intentionally
	// shareable across goroutines (the wrapped *sql.Tx is, modulo
	// the database/sql per-statement rules) and the queues must not
	// race when concurrent CRUD calls register hooks.
	afterHooks      []func() error
	onCommitHooks   []func(context.Context) error
	onRollbackHooks []func(context.Context) error
	// savepointMarks snapshots the hook-queue lengths captured when
	// each live savepoint was created. ROLLBACK TO a savepoint undoes
	// the SQL written since it was set; [Tx.RollbackTo] uses these
	// marks to symmetrically undo the After*/OnCommit/OnRollback hooks
	// queued in that same window, so a rolled-back savepoint scope can
	// never fire the side-effects of work it just discarded on the
	// eventual outer commit (ADR-0013 Regla 2 extended to savepoints).
	// Guarded by hooksMu.
	savepointMarks []savepointMark
	hooksMu        sync.Mutex

	// savepointSeq supplies unique savepoint names for the [Tx.Tx]
	// nested-transaction helper, so two nested scopes never collide on
	// a clock-derived name. Zero value is ready to use.
	savepointSeq atomic.Uint64
}

// savepointMark records the FIFO hook-queue lengths at the instant a
// savepoint was created. RollbackTo(name) truncates each queue back to
// the lengths stored for the most recent savepoint named `name`.
type savepointMark struct {
	name      string
	afterN    int
	commitN   int
	rollbackN int
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
	return &Tx{tx: sqlTx, client: c, ctx: ctx}, nil
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
//
// When commit succeeds, any model After* hooks queued during the
// transaction lifetime are drained in FIFO order. A hook returning
// an error is logged via the Client's slog logger (event:
// `quark.hook.after_post_commit_error`) but does NOT block the
// remaining hooks or fail the Commit — once the database confirmed
// the commit, no application-level handler can undo it (ADR-0013
// Regla 2). Commit failures surface the underlying database error
// and the queued hooks are discarded.
//
// The context each After* hook receives is the context the originating
// Query[T] captured at construction time (the ctx passed to
// [ForTx]). If the caller installed a deadline on that context via
// [context.WithTimeout] inside the [Client.Tx] callback, the hook
// may observe an already-expired ctx after Commit returns. Hooks
// that need a fresh context can derive one from
// [context.Background] inside their implementation.
func (t *Tx) Commit() error {
	if err := t.tx.Commit(); err != nil {
		t.discardAllHooks()
		return err
	}
	// Drain order: model After* hooks first (ORM contract), then the
	// user-registered OnCommit side-effects. OnRollback callbacks are
	// discarded — this is a commit, not a rollback.
	t.drainAfterHooks()
	t.drainCtxHooks(t.takeOnCommitHooks(), "quark.hook.on_commit_error")
	t.discardOnRollbackHooks()
	return nil
}

// Rollback aborts the transaction.
//
// All model After* hooks and OnCommit callbacks queued during the
// transaction lifetime are discarded without firing — rolled-back
// work never triggers the side-effects that would have followed it
// (ADR-0013 Regla 2). OnRollback callbacks DO fire, in FIFO order,
// after the underlying rollback is attempted, so callers can react
// to the abort (release a reservation, emit a "cancelled" event,
// etc.). An OnRollback callback returning an error is logged but
// does not block the rest of the chain.
func (t *Tx) Rollback() error {
	t.discardAfterHooks()
	t.discardOnCommitHooks()
	t.discardSavepointMarks()
	err := t.tx.Rollback()
	t.drainCtxHooks(t.takeOnRollbackHooks(), "quark.hook.on_rollback_error")
	return err
}

// OnCommit registers a callback to run after the transaction commits
// successfully. Callbacks fire in FIFO registration order, after the
// model After* hooks, with the transaction's context. A callback
// returning an error is logged via the Client's slog logger (event
// `quark.hook.on_commit_error`) but does NOT block the remaining
// callbacks — once the database has confirmed the commit, no
// application-level handler can undo it (ADR-0013 Regla 3).
//
// If the transaction rolls back instead, registered OnCommit
// callbacks are discarded without firing.
//
// Registering an OnCommit callback from inside another OnCommit
// callback (i.e. during the drain) is a no-op for the current
// commit: the queue was already lifted before the drain began, so
// the newly-registered fn will not fire. This prevents unbounded
// re-entrancy.
//
// Example:
//
//	err := client.Tx(ctx, func(tx *quark.Tx) error {
//	    if err := quark.ForTx[Order](ctx, tx).Create(o); err != nil {
//	        return err
//	    }
//	    tx.OnCommit(func(ctx context.Context) error {
//	        return bus.Publish(ctx, OrderCreated{ID: o.ID})
//	    })
//	    return nil
//	})
func (t *Tx) OnCommit(fn func(context.Context) error) {
	if t == nil || fn == nil {
		return
	}
	t.hooksMu.Lock()
	t.onCommitHooks = append(t.onCommitHooks, fn)
	t.hooksMu.Unlock()
}

// OnRollback registers a callback to run after the transaction rolls
// back. Callbacks fire in FIFO registration order with the
// transaction's context. A callback returning an error is logged via
// the Client's slog logger (event `quark.hook.on_rollback_error`)
// but does NOT block the remaining callbacks.
//
// If the transaction commits instead, registered OnRollback
// callbacks are discarded without firing.
//
// Example:
//
//	tx.OnRollback(func(ctx context.Context) error {
//	    log.Warn("order create rolled back", "id", o.ID)
//	    return nil
//	})
func (t *Tx) OnRollback(fn func(context.Context) error) {
	if t == nil || fn == nil {
		return
	}
	t.hooksMu.Lock()
	t.onRollbackHooks = append(t.onRollbackHooks, fn)
	t.hooksMu.Unlock()
}

// txCtx returns the transaction's stored context, falling back to
// context.Background() when the Tx was constructed without one (e.g.
// in tests that build a Tx literal). Keeps the drain helpers
// nil-safe.
func (t *Tx) txCtx() context.Context {
	if t.ctx != nil {
		return t.ctx
	}
	return context.Background()
}

// takeOnCommitHooks / takeOnRollbackHooks atomically lift-and-clear
// the respective queue under the mutex so the drain runs without
// holding the lock (a callback may re-enter Quark).
func (t *Tx) takeOnCommitHooks() []func(context.Context) error {
	t.hooksMu.Lock()
	h := t.onCommitHooks
	t.onCommitHooks = nil
	t.hooksMu.Unlock()
	return h
}

func (t *Tx) takeOnRollbackHooks() []func(context.Context) error {
	t.hooksMu.Lock()
	h := t.onRollbackHooks
	t.onRollbackHooks = nil
	t.hooksMu.Unlock()
	return h
}

// drainCtxHooks runs the ctx-aware callbacks in FIFO order, logging
// failures via Client.logger with the given event name but never
// propagating the error or stopping the cascade.
func (t *Tx) drainCtxHooks(hooks []func(context.Context) error, event string) {
	ctx := t.txCtx()
	for _, fn := range hooks {
		if err := fn(ctx); err != nil && t.client != nil && t.client.logger != nil {
			t.client.logger.Warn("transaction side-effect callback returned error",
				"event", event,
				"err", err)
		}
	}
}

// discardOnCommitHooks / discardOnRollbackHooks drop the respective
// queue without firing.
func (t *Tx) discardOnCommitHooks() {
	t.hooksMu.Lock()
	t.onCommitHooks = nil
	t.hooksMu.Unlock()
}

func (t *Tx) discardOnRollbackHooks() {
	t.hooksMu.Lock()
	t.onRollbackHooks = nil
	t.hooksMu.Unlock()
}

// discardAllHooks drops every queue. Called on commit failure — no
// side-effect fires when the commit itself errored.
func (t *Tx) discardAllHooks() {
	t.hooksMu.Lock()
	t.afterHooks = nil
	t.onCommitHooks = nil
	t.onRollbackHooks = nil
	t.savepointMarks = nil
	t.hooksMu.Unlock()
}

// discardSavepointMarks forgets all savepoint bookkeeping. Called from
// the terminal [Tx.Rollback] path; [Tx.discardAllHooks] clears it
// inline on the commit-failure path. Keeps savepointMarks consistent
// with the (now-empty) hook queues once the transaction is over.
func (t *Tx) discardSavepointMarks() {
	t.hooksMu.Lock()
	t.savepointMarks = nil
	t.hooksMu.Unlock()
}

// queueAfterHook appends fn to the per-tx After* hook queue. Called
// from query_crud.go around each CRUD operation when the underlying
// Query was bound to this transaction (BaseQuery.tx != nil); the
// non-tx CRUD path runs After* hooks inline as before.
//
// queueAfterHook is safe to call from any goroutine that owns a
// reference to the Tx; the hooksMu mutex serialises appends against
// drainAfterHooks / discardAfterHooks.
func (t *Tx) queueAfterHook(fn func() error) {
	if t == nil || fn == nil {
		return
	}
	t.hooksMu.Lock()
	t.afterHooks = append(t.afterHooks, fn)
	t.hooksMu.Unlock()
}

// drainAfterHooks runs every queued hook in FIFO order, logging
// individual failures via Client.logger but never propagating the
// error or stopping the cascade. The slice is cleared so a future
// Commit (e.g., after savepoint round-trips that re-enter the
// drain path) is a no-op.
// The model After* hook closures capture their own context (the one
// the originating Query[T] was built with), so this drain takes no
// ctx argument — unlike drainCtxHooks, which serves the F5-5
// OnCommit/OnRollback callbacks that receive the tx context.
func (t *Tx) drainAfterHooks() {
	t.hooksMu.Lock()
	hooks := t.afterHooks
	t.afterHooks = nil
	t.hooksMu.Unlock()
	for _, fn := range hooks {
		if err := fn(); err != nil && t.client != nil && t.client.logger != nil {
			t.client.logger.Warn("after-hook returned error post-commit",
				"event", "quark.hook.after_post_commit_error",
				"err", err)
		}
	}
}

// discardAfterHooks drops the queue without firing any hook. Called
// from Rollback and from Commit failures.
func (t *Tx) discardAfterHooks() {
	t.hooksMu.Lock()
	t.afterHooks = nil
	t.hooksMu.Unlock()
}

// markSavepoint records the current hook-queue lengths against name.
// Called after a SAVEPOINT statement succeeds.
func (t *Tx) markSavepoint(name string) {
	t.hooksMu.Lock()
	t.savepointMarks = append(t.savepointMarks, savepointMark{
		name:      name,
		afterN:    len(t.afterHooks),
		commitN:   len(t.onCommitHooks),
		rollbackN: len(t.onRollbackHooks),
	})
	t.hooksMu.Unlock()
}

// rollbackHooksTo discards every After*/OnCommit/OnRollback hook queued
// since the most recent savepoint named `name`, mirroring the ROLLBACK
// TO SAVEPOINT that just undid the writes those hooks were about to
// react to. Savepoints stacked above `name` are forgotten (ROLLBACK TO
// destroys nested savepoints) while `name` itself survives — a
// savepoint can be rolled back to repeatedly until released. A name
// with no recorded mark (e.g. a savepoint issued via raw Exec) is a
// no-op.
func (t *Tx) rollbackHooksTo(name string) {
	t.hooksMu.Lock()
	defer t.hooksMu.Unlock()
	idx := t.findSavepointMark(name)
	if idx < 0 {
		return
	}
	m := t.savepointMarks[idx]
	t.afterHooks = truncateHooks(t.afterHooks, m.afterN)
	t.onCommitHooks = truncateHooks(t.onCommitHooks, m.commitN)
	t.onRollbackHooks = truncateHooks(t.onRollbackHooks, m.rollbackN)
	t.savepointMarks = t.savepointMarks[:idx+1]
}

// releaseSavepointMark forgets the most recent savepoint named `name`
// and any stacked above it without touching the hook queues — a
// released savepoint's work merges into the surrounding transaction,
// so its hooks stay queued for the eventual commit or rollback.
func (t *Tx) releaseSavepointMark(name string) {
	t.hooksMu.Lock()
	defer t.hooksMu.Unlock()
	idx := t.findSavepointMark(name)
	if idx < 0 {
		return
	}
	t.savepointMarks = t.savepointMarks[:idx]
}

// findSavepointMark returns the index of the most recent mark with the
// given name, or -1. Most-recent matches SQL semantics: re-using a
// savepoint name shadows the earlier one, and ROLLBACK TO / RELEASE
// target the newest. Caller holds hooksMu.
func (t *Tx) findSavepointMark(name string) int {
	for i := len(t.savepointMarks) - 1; i >= 0; i-- {
		if t.savepointMarks[i].name == name {
			return i
		}
	}
	return -1
}

// truncateHooks reslices s to length n, zeroing the dropped trailing
// entries so their captured closures can be garbage-collected before
// the transaction ends. n >= len(s) is a no-op (defensive against a
// concurrent drain having already shortened the queue).
func truncateHooks[F any](s []F, n int) []F {
	if n >= len(s) {
		return s
	}
	var zero F
	for i := n; i < len(s); i++ {
		s[i] = zero
	}
	return s[:n]
}

// Savepoint creates a savepoint with the given name. Hooks queued by
// CRUD run between this call and a matching [Tx.RollbackTo] are
// unwound by that rollback (see [Tx.RollbackTo]).
func (t *Tx) Savepoint(name string) error {
	if err := t.client.guard.ValidateIdentifier(name); err != nil {
		return err
	}
	if _, err := t.tx.Exec(t.savepointStmt(name)); err != nil {
		return err
	}
	t.markSavepoint(name)
	return nil
}

// savepointStmt / rollbackToStmt / releaseSavepointStmt resolve the per-dialect
// savepoint DML. A dialect implementing [SavepointDialect] (SQL Server, Oracle)
// overrides the ANSI default; everything else gets ANSI
// SAVEPOINT / ROLLBACK TO SAVEPOINT / RELEASE SAVEPOINT (correct for
// PostgreSQL, MySQL, MariaDB, SQLite). A "" release statement means the engine
// releases savepoints at COMMIT and has no explicit statement (BB-9).
func (t *Tx) savepointStmt(name string) string {
	if sd, ok := t.client.dialect.(SavepointDialect); ok {
		return sd.SavepointStmt(name)
	}
	return "SAVEPOINT " + t.client.dialect.Quote(name)
}

func (t *Tx) rollbackToStmt(name string) string {
	if sd, ok := t.client.dialect.(SavepointDialect); ok {
		return sd.RollbackToSavepointStmt(name)
	}
	return "ROLLBACK TO SAVEPOINT " + t.client.dialect.Quote(name)
}

func (t *Tx) releaseSavepointStmt(name string) string {
	if sd, ok := t.client.dialect.(SavepointDialect); ok {
		return sd.ReleaseSavepointStmt(name)
	}
	return "RELEASE SAVEPOINT " + t.client.dialect.Quote(name)
}

// RollbackTo rolls back to the named savepoint. Beyond undoing the SQL
// written since the savepoint, it discards the After*/OnCommit/
// OnRollback hooks queued in that window so the rolled-back work does
// not trigger its side-effects on the eventual commit (ADR-0013 Regla
// 2). Reactions to the partial rollback flow through the error the
// nested scope returns, not through OnRollback (which fires only on a
// whole-transaction rollback).
func (t *Tx) RollbackTo(name string) error {
	if err := t.client.guard.ValidateIdentifier(name); err != nil {
		return err
	}
	if _, err := t.tx.Exec(t.rollbackToStmt(name)); err != nil {
		return err
	}
	t.rollbackHooksTo(name)
	return nil
}

// ReleaseSavepoint releases the named savepoint. Hooks queued since it
// was set stay on the transaction queues — released work is part of
// the surrounding transaction and its side-effects fire with it.
func (t *Tx) ReleaseSavepoint(name string) error {
	if err := t.client.guard.ValidateIdentifier(name); err != nil {
		return err
	}
	if stmt := t.releaseSavepointStmt(name); stmt != "" {
		if _, err := t.tx.Exec(stmt); err != nil {
			return err
		}
	}
	t.releaseSavepointMark(name)
	return nil
}

func (t *Tx) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	spName := fmt.Sprintf("sp_%d", t.savepointSeq.Add(1))
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

	// F5-5: stash the *Tx in the query context so model hooks
	// dispatched from this query can retrieve it via
	// [TxFromContext]. The hook interfaces only receive ctx (ADR-0013
	// rejected widening their signatures), so the context value is
	// the channel through which a BeforeCreate/AfterUpdate/etc. hook
	// reaches the active transaction — e.g. to register an OnCommit
	// side-effect of its own.
	ctx = context.WithValue(ctx, txContextKey{}, tx)

	return &Query[T]{
		BaseQuery: BaseQuery{
			ctx:     ctx,
			client:  tx.client,
			dialect: tx.client.dialect,
			guard:   tx.client.guard,
			table:   meta.Table,
			pk:      meta.PK,
			exec:    tx.tx,
			tx:      tx, // F5-4: post-commit After-hook queue lives on tx.
			meta:    meta,
		},
	}
}

// txContextKey is the unexported context key under which [ForTx]
// stashes the active *Tx. Unexported so no external package can
// collide with or forge the value.
type txContextKey struct{}

// TxFromContext returns the *Tx the context is currently scoped to,
// or nil when the context was not enriched by a [ForTx] call. Note
// that this includes the [Client.Tx] callback body itself: that
// callback receives the *Tx as a parameter, and the bare ctx it
// captures is NOT enriched until it flows through ForTx[T]. The
// helper is therefore meant for lifecycle hooks (which only get a
// context), not for the Tx callback body (which already has the tx).
//
// The primary use is inside a lifecycle hook: the hook interfaces
// ([BeforeCreateHook], [AfterUpdateHook], …) receive only a context,
// so a hook that needs to register a commit/rollback side-effect
// reaches the transaction through this helper:
//
//	func (o *Order) AfterCreate(ctx context.Context) error {
//	    if tx := quark.TxFromContext(ctx); tx != nil {
//	        tx.OnCommit(func(ctx context.Context) error {
//	            return bus.Publish(ctx, OrderCreated{ID: o.ID})
//	        })
//	    }
//	    return nil
//	}
//
// Returning nil is the normal case for non-transactional CRUD; the
// caller must nil-check before use.
func TxFromContext(ctx context.Context) *Tx {
	if ctx == nil {
		return nil
	}
	tx, _ := ctx.Value(txContextKey{}).(*Tx)
	return tx
}
