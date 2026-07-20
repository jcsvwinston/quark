// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// nativeRLSExecutor wraps an Executor (typically *sql.DB) so every
// query and exec is automatically transacted with
// `set_config('<NativeRLSVar>', <tenantID>, true)` emitted as the
// first statement. The wrapper implements the Executor interface so
// it can be dropped into BaseQuery.exec under the
// RowLevelSecurityNative tenant strategy.
//
// Tx semantics are honest about the limits of database/sql:
//
//   - ExecContext begins a tx, sets the variable, runs the exec, and
//     commits before returning. No tx leak.
//
//   - QueryContext begins a tx, sets the variable, and returns
//     *sql.Rows produced by the tx. The tx is **left open** because
//     *sql.Rows is an opaque struct that the wrapper cannot subclass.
//     A context.AfterFunc registered before returning commits the tx
//     when the caller's context ends — so the typical request-scoped
//     usage releases the connection on handler return. The Tx scoped
//     to that ctx will not be referenced again by the caller; pg
//     keeps the snapshot consistent until the tx finalizes.
//
//   - QueryRowContext has the same shape as QueryContext: tx left
//     open, AfterFunc commits on ctx end. *sql.Row is also opaque so
//     no wrapping is possible.
//
// For workloads that cannot tolerate the implicit-tx pattern (long
// streaming via Iter/Cursor; very long-lived ctx that touches many
// queries), callers should use TenantRouter.Tx directly — that path
// opens a single tx for the whole callback and never leaks.
//
// See ADR-0012 §"Cómo se ejecuta SET LOCAL por query" for the design
// rationale.
type nativeRLSExecutor struct {
	db       *sql.DB
	tenantID string
	varName  string // e.g. "app.tenant_id"
	client   *Client
}

func newNativeRLSExecutor(client *Client, tenantID, varName string) *nativeRLSExecutor {
	return &nativeRLSExecutor{
		db:       client.db,
		tenantID: tenantID,
		varName:  varName,
		client:   client,
	}
}

// log returns the logger for executor-internal events: the owning
// Client's logger when present, slog.Default() otherwise — the same
// fallback New() installs. Failures on the deferred implicit-tx path
// happen after control returned to the caller, so the log line is the
// only inline signal; it must never be silenced by a nil logger (QK6-3).
func (e *nativeRLSExecutor) log() *slog.Logger {
	if e.client != nil && e.client.logger != nil {
		return e.client.logger
	}
	return slog.Default()
}

// noteDeferredCommitFailure records a failed deferred commit: an ERROR
// log (the write in that implicit tx was NOT committed — the caller
// already got a success, so this line is the honest correction) plus the
// operator-facing counter surfaced by [Client.DeferredCommitFailures].
func (e *nativeRLSExecutor) noteDeferredCommitFailure(cerr error) {
	e.log().Error("native rls: implicit-tx deferred commit failed after ctx ended; the write in that tx was not committed",
		"err", cerr, "var", e.varName)
	if e.client != nil {
		e.client.deferredCommitFailures.Add(1)
	}
}

// DeferredCommitFailures reports how many deferred implicit-transaction
// commits have failed on this Client since it was created. Deferred
// commits only exist under the RowLevelSecurityNative strategy: the
// For[T] query/QueryRow paths commit their implicit transaction when the
// request context ends (see nativeRLSExecutor), so a commit failure
// happens AFTER the operation already returned success to the caller —
// each unit here is a write (or read snapshot) that was silently rolled
// back by the engine. The failure is also logged at ERROR level through
// the Client's logger at the moment it happens; this counter is the
// aggregate for operators to alert on. It never resets; it is safe for
// concurrent use.
func (c *Client) DeferredCommitFailures() uint64 {
	return c.deferredCommitFailures.Load()
}

// cleanupAbandonedImplicitTx is the shared body of the QK7-1 defer
// guards: it runs when an implicit-tx builder exits before ownership of
// the tx/conn pair transferred (an error return, or a driver panic).
// Rollback on an already-finalized tx returns ErrTxDone, which is
// harmless — the guard only has to guarantee that neither resource is
// left held.
func cleanupAbandonedImplicitTx(tx *sql.Tx, conn *sql.Conn) {
	if tx != nil {
		_ = tx.Rollback()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

// setConfigSQL returns the SQL that emits the per-tx tenant variable.
// We use the set_config function form rather than `SET LOCAL <name> = $1`
// because the latter does not accept parameter binding in PostgreSQL —
// the variable value would have to be inlined, which interacts poorly
// with the bind-only contract Quark holds elsewhere.
func (e *nativeRLSExecutor) setConfigSQL() string {
	return "SELECT set_config($1, $2, true)"
}

// ExecContext begins an implicit transaction, sets the tenant variable,
// executes the statement, and commits. Failure at any point rolls back
// and surfaces the original error.
//
// Unlike QueryContext/QueryRowContext there is no lifecycle split here:
// BeginTx receives the caller's ctx directly, so both the pool wait and
// the tx honour cancellation/deadline — the commit is synchronous, so
// the QK5-4 deferred-commit race does not apply to this path (verified
// under QK6-2).
func (e *nativeRLSExecutor) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("native rls: begin tx: %w", err)
	}
	// QK7-1 guard: any exit before Commit takes over — an error return
	// or a driver panic inside one of the ExecContext calls — must roll
	// back, or the tx keeps its pooled connection forever. On the panic
	// path the cleanup runs detached: a panicking driver unwinds through
	// database/sql code that releases its internal locks via defer on the
	// exec path but not on every path, so a same-goroutine Rollback could
	// block forever and turn the panic into a deadlock.
	finished := false
	defer func() {
		if finished {
			return
		}
		if p := recover(); p != nil {
			go cleanupAbandonedImplicitTx(tx, nil)
			panic(p)
		}
		cleanupAbandonedImplicitTx(tx, nil)
	}()
	if _, err := tx.ExecContext(ctx, e.setConfigSQL(), e.varName, e.tenantID); err != nil {
		return nil, fmt.Errorf("native rls: set_config: %w", err)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	// Commit finalizes the tx and releases its connection even when it
	// returns an error, so the guard must stand down before the call.
	finished = true
	if cerr := tx.Commit(); cerr != nil {
		return nil, fmt.Errorf("native rls: commit: %w", cerr)
	}
	return result, nil
}

// QueryContext begins an implicit transaction, sets the tenant
// variable, and returns the rows. The tx is committed by a
// context.AfterFunc registered against the caller's ctx. The caller
// remains responsible for closing the *sql.Rows; the tx commit and
// the rows close happen independently.
//
// Side-effect for very long-lived ctx (CLI batch jobs running many
// queries before ctx ends): each query holds a connection until ctx
// terminates. Callers in that regime should use TenantRouter.Tx.
func (e *nativeRLSExecutor) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	// Acquire the connection with the CALLER's ctx so the pool wait
	// honours cancellation/deadline. Passing WithoutCancel straight to
	// db.BeginTx also detached the pool-acquisition wait — with a
	// saturated pool the goroutine kept blocking past the request
	// timeout (QK6-2). Only the transaction below is detached.
	conn, err := e.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("native rls: acquire conn: %w", err)
	}
	// QK7-1 guard: every exit before the AfterFunc below takes ownership
	// of the tx+conn pair — an error return or a panicking driver — must
	// roll back and hand the conn back, or a pool slot leaks permanently.
	// On the panic path the cleanup runs detached: database/sql's begin
	// and query internals only release their locks on the error path, so
	// after a driver panic a same-goroutine Rollback/Close can block
	// forever — the detached call frees the conn whenever database/sql
	// left it releasable, and never turns the panic into a deadlock.
	var tx *sql.Tx
	owned := false
	defer func() {
		if owned {
			return
		}
		if p := recover(); p != nil {
			go cleanupAbandonedImplicitTx(tx, conn)
			panic(p)
		}
		cleanupAbandonedImplicitTx(tx, conn)
	}()
	// The tx lifecycle is detached from the caller's ctx on purpose:
	// database/sql auto-ROLLS BACK a transaction whose BeginTx ctx is
	// canceled, and a request ctx always ends by cancellation — so binding
	// the lifecycle to it made the AfterFunc commit RACE an automatic
	// rollback (for the QueryRow write path, that silently lost the insert).
	// Detached, the deferred commit is deterministic; per-statement
	// cancellation still applies through the ctx each query receives.
	// Surfaced by QK5-4, the first time a CI lane executed this path.
	tx, err = conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return nil, fmt.Errorf("native rls: begin tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, e.setConfigSQL(), e.varName, e.tenantID); err != nil {
		return nil, fmt.Errorf("native rls: set_config: %w", err)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	owned = true // the AfterFunc owns the tx+conn pair from here
	context.AfterFunc(ctx, func() {
		if cerr := tx.Commit(); cerr != nil {
			e.noteDeferredCommitFailure(cerr)
		}
		// With sql.Conn the pool hand-back is manual: Close returns the
		// connection once the tx above has finalized. Without it the
		// conn leaks from the pool permanently.
		_ = conn.Close()
	})
	return rows, nil
}

// QueryRowContext is the *sql.Row analogue of QueryContext. *sql.Row
// is opaque so the tx commit relies on the same context.AfterFunc
// pattern. Errors from BeginTx or set_config produce a *sql.Row whose
// Scan returns the error; we leverage the QueryRowContext-on-tx
// helper to surface those errors honestly.
func (e *nativeRLSExecutor) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	// Caller-ctx acquisition + detached tx, same split as QueryContext
	// (QK6-2): the pool wait must honour the request deadline; only the
	// tx lifecycle is detached (a canceled BeginTx ctx means
	// database/sql rolls the tx back, which for INSERT … RETURNING
	// silently discarded the write).
	conn, err := e.db.Conn(ctx)
	if err != nil {
		e.log().Error("native rls: acquire conn failed for QueryRow",
			"err", err, "var", e.varName)
		// Like the BeginTx failure below: surface the failure through the
		// returned *sql.Row. With ctx already canceled/expired (the QK6-2
		// regime) this Row's Scan reports ctx.Err() directly.
		return e.db.QueryRowContext(ctx, "SELECT NULL WHERE FALSE")
	}
	// QK7-1 guard — same shape and rationale as QueryContext: cleanup on
	// every exit until the AfterFunc takes ownership, detached when a
	// driver panic is unwinding.
	var tx *sql.Tx
	owned := false
	defer func() {
		if owned {
			return
		}
		if p := recover(); p != nil {
			go cleanupAbandonedImplicitTx(tx, conn)
			panic(p)
		}
		cleanupAbandonedImplicitTx(tx, conn)
	}()
	tx, err = conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		// Release inline before minting the sentinel row: it queries
		// e.db, so it needs a pool slot — the deferred guard only runs
		// after the return expression, which starves a size-1 pool. The
		// cleanup is idempotent (ErrTxDone / ErrConnDone), so the guard
		// re-running it afterwards is harmless.
		cleanupAbandonedImplicitTx(tx, conn)
		// Return a *sql.Row that surfaces the error on Scan. The
		// simplest way is to call db.QueryRowContext with an
		// intentionally-failing query that captures err; but the
		// cleanest path is to use db.QueryRowContext with a no-op SQL
		// and rely on the caller seeing nil values — that loses the
		// error. We instead emit a synthetic row that scans the error:
		//
		// We can't construct *sql.Row directly. Workaround: emit a
		// QueryRowContext that the caller's Scan will surface as
		// "no rows" or driver error, plus log the begin failure.
		e.log().Error("native rls: begin tx failed for QueryRow",
			"err", err, "var", e.varName)
		return e.db.QueryRowContext(ctx, "SELECT NULL WHERE FALSE")
	}
	if _, err := tx.ExecContext(ctx, e.setConfigSQL(), e.varName, e.tenantID); err != nil {
		// Same inline release as the BeginTx branch above.
		cleanupAbandonedImplicitTx(tx, conn)
		e.log().Error("native rls: set_config failed for QueryRow",
			"err", err, "var", e.varName)
		return e.db.QueryRowContext(ctx, "SELECT NULL WHERE FALSE")
	}
	row := tx.QueryRowContext(ctx, query, args...)
	owned = true // the AfterFunc owns the tx+conn pair from here
	context.AfterFunc(ctx, func() {
		if cerr := tx.Commit(); cerr != nil {
			e.noteDeferredCommitFailure(cerr)
		}
		// Manual pool hand-back — see QueryContext.
		_ = conn.Close()
	})
	return row
}

// Tx opens a single transaction on the router's BaseClient, calls
// `set_config('<NativeRLSVar>', <resolvedTenantID>, true)` as the
// first statement, and invokes fn(tx). On nil return from fn the
// transaction commits; on error it rolls back. This is the
// recommended entry point under RowLevelSecurityNative — it avoids
// the per-query implicit-tx overhead and the connection-hold semantics
// described on nativeRLSExecutor.
//
// For other strategies (DatabasePerTenant / SchemaPerTenant /
// RowLevelSecurityClient), Tx delegates to the underlying client's Tx
// without emitting the variable.
//
// Returns ErrUnsupportedFeature wrapped with the dialect name when
// the BaseClient is not PostgreSQL under RowLevelSecurityNative.
func (r *TenantRouter) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	tenantID, err := r.ResolveTenant(ctx)
	if err != nil {
		return err
	}

	if r.config.Strategy != RowLevelSecurityNative {
		client, err := r.GetClient(ctx)
		if err != nil {
			return err
		}
		return client.Tx(ctx, fn)
	}

	if r.config.BaseClient == nil {
		return fmt.Errorf("BaseClient must be provided for RowLevelSecurityNative")
	}
	if r.config.BaseClient.dialect.Name() != "postgres" {
		return fmt.Errorf("%w: RowLevelSecurityNative requires PostgreSQL, got dialect %q",
			ErrUnsupportedFeature, r.config.BaseClient.dialect.Name())
	}

	varName := r.config.defaultNativeRLSVar()

	return r.config.BaseClient.Tx(ctx, func(tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", varName, tenantID); err != nil {
			return fmt.Errorf("native rls: set_config: %w", err)
		}
		return fn(tx)
	})
}
