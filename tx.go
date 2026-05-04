// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
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
func (c *Client) Tx(ctx context.Context, fn func(tx *Tx) error) error {
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
