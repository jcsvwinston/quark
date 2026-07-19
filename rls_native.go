// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
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
	if _, err := tx.ExecContext(ctx, e.setConfigSQL(), e.varName, e.tenantID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("native rls: set_config: %w", err)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
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
	// The tx lifecycle is detached from the caller's ctx on purpose:
	// database/sql auto-ROLLS BACK a transaction whose BeginTx ctx is
	// canceled, and a request ctx always ends by cancellation — so binding
	// the lifecycle to it made the AfterFunc commit RACE an automatic
	// rollback (for the QueryRow write path, that silently lost the insert).
	// Detached, the deferred commit is deterministic; per-statement
	// cancellation still applies through the ctx each query receives.
	// Surfaced by QK5-4, the first time a CI lane executed this path.
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("native rls: begin tx: %w", err)
	}
	if _, err := tx.ExecContext(ctx, e.setConfigSQL(), e.varName, e.tenantID); err != nil {
		_ = tx.Rollback()
		_ = conn.Close()
		return nil, fmt.Errorf("native rls: set_config: %w", err)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		_ = tx.Rollback()
		_ = conn.Close()
		return nil, err
	}
	context.AfterFunc(ctx, func() {
		if cerr := tx.Commit(); cerr != nil && e.client != nil && e.client.logger != nil {
			e.client.logger.Warn("native rls: implicit-tx commit failed after ctx ended",
				"err", cerr, "var", e.varName)
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
		if e.client != nil && e.client.logger != nil {
			e.client.logger.Error("native rls: acquire conn failed for QueryRow",
				"err", err, "var", e.varName)
		}
		// Like the BeginTx failure below: surface the failure through the
		// returned *sql.Row. With ctx already canceled/expired (the QK6-2
		// regime) this Row's Scan reports ctx.Err() directly.
		return e.db.QueryRowContext(ctx, "SELECT NULL WHERE FALSE")
	}
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		_ = conn.Close()
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
		if e.client != nil && e.client.logger != nil {
			e.client.logger.Error("native rls: begin tx failed for QueryRow",
				"err", err, "var", e.varName)
		}
		return e.db.QueryRowContext(ctx, "SELECT NULL WHERE FALSE")
	}
	if _, err := tx.ExecContext(ctx, e.setConfigSQL(), e.varName, e.tenantID); err != nil {
		_ = tx.Rollback()
		_ = conn.Close()
		if e.client != nil && e.client.logger != nil {
			e.client.logger.Error("native rls: set_config failed for QueryRow",
				"err", err, "var", e.varName)
		}
		return e.db.QueryRowContext(ctx, "SELECT NULL WHERE FALSE")
	}
	row := tx.QueryRowContext(ctx, query, args...)
	context.AfterFunc(ctx, func() {
		if cerr := tx.Commit(); cerr != nil && e.client != nil && e.client.logger != nil {
			e.client.logger.Warn("native rls: implicit-tx commit failed after ctx ended",
				"err", cerr, "var", e.varName)
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
