// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"time"
)

// Cursor provides manual iteration over query results.
// Similar to sql.Rows but type-safe for model T.
type Cursor[T any] struct {
	rows   *sql.Rows
	ctx    context.Context
	cancel context.CancelFunc
	query  *Query[T]
	sql    string
	args   []any
	start  time.Time
	closed bool
}

// Next advances to the next row. Returns false when done or on error.
func (c *Cursor[T]) Next() bool {
	if c.closed {
		return false
	}
	return c.rows.Next()
}

// Scan copies the current row into the destination struct.
func (c *Cursor[T]) Scan(dest *T) error {
	return c.query.scanRow(c.rows, dest)
}

// Close releases resources and notifies observers.
//
// F5-4: when the underlying rows close cleanly (no rows.Err) and
// the model implements [AfterFindHook], the hook fires here exactly
// once. A row-level error short-circuits AfterFind because the read
// effectively did not "succeed". Errors from AfterFind replace the
// would-be-returned err, mirroring [Query.Iter].
func (c *Cursor[T]) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	defer c.cancel()

	closeErr := c.rows.Close()
	rowsErr := c.rows.Err()

	// Notify observers
	duration := time.Since(c.start)
	c.query.notifyObservers(QueryEvent{
		SQL:       c.sql,
		Args:      c.args,
		Duration:  duration,
		Table:     c.query.table,
		Operation: "SELECT (cursor)",
	})

	if closeErr != nil {
		return closeErr
	}
	if rowsErr != nil {
		return rowsErr
	}
	return c.query.callAfterFind(c.query.ctx)
}

// Err returns any error encountered during iteration.
func (c *Cursor[T]) Err() error {
	return c.rows.Err()
}
