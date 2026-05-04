// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
)

// Routine is a builder for executing database functions and stored procedures
// that return results (table-valued functions or scalar functions).
type Routine[T any] struct {
	provider ClientProvider
	ctx      context.Context
	routine  string
	args     []any
	err      error
}

// NewRoutine creates a new Routine builder for the given procedure/function.
func NewRoutine[T any](ctx context.Context, provider ClientProvider, routine string, args ...any) *Routine[T] {
	return &Routine[T]{
		provider: provider,
		ctx:      ctx,
		routine:  routine,
		args:     args,
	}
}

// execute runs the routine and returns the rows.
func (r *Routine[T]) execute() (*sql.Rows, *Client, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	
	client, err := r.provider.GetClient(r.ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get client: %w", err)
	}

	if err := client.guard.ValidateIdentifier(r.routine); err != nil {
		return nil, nil, fmt.Errorf("invalid routine name: %w", err)
	}

	sqlStr := client.dialect.BuildRoutineQuery(r.routine, len(r.args))

	// We use the basic exec flow, but it bypasses the standard Query middleware since it's a Routine.
	// For production, we should wrap this in a RoutineFunc or reuse QueryFunc if possible.
	// Here we just execute directly for simplicity.
	rows, err := client.db.QueryContext(r.ctx, sqlStr, r.args...)
	if err != nil {
		return nil, nil, err
	}

	return rows, client, nil
}

// List executes the routine and maps the resulting rows to a slice of T.
func (r *Routine[T]) List() ([]T, error) {
	rows, client, err := r.execute()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []T
	var zero T
	isStruct := reflect.TypeOf(zero).Kind() == reflect.Struct

	// Reusing the mapping logic from Query
	// Since we don't have a Query instance, we create a dummy one just for scanRow
	dummyQuery := &Query[T]{
		BaseQuery: BaseQuery{
			dialect: client.dialect,
			guard:   client.guard,
		},
	}
	if isStruct {
		dummyQuery.meta = GetModelMeta[T]()
	}

	for rows.Next() {
		var item T
		if isStruct {
			if err := dummyQuery.scanRow(rows, &item); err != nil {
				return nil, err
			}
		} else {
			if err := rows.Scan(&item); err != nil {
				return nil, err
			}
		}
		results = append(results, item)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// First executes the routine and returns the first row.
func (r *Routine[T]) First() (T, error) {
	results, err := r.List()
	if err != nil {
		var zero T
		return zero, err
	}
	if len(results) == 0 {
		var zero T
		return zero, sql.ErrNoRows
	}
	return results[0], nil
}

// Scalar executes a routine and returns a single scalar value.
// It is a convenient alias for First() when T is a primitive type.
func (r *Routine[T]) Scalar() (T, error) {
	return r.First()
}

// Call executes a stored procedure that does not return a result set,
// but may modify OUT parameters.
func Call(ctx context.Context, provider ClientProvider, procedure string, args ...any) error {
	client, err := provider.GetClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to get client: %w", err)
	}

	if err := client.guard.ValidateIdentifier(procedure); err != nil {
		return fmt.Errorf("invalid procedure name: %w", err)
	}

	sqlStr := client.dialect.BuildProcedureCall(procedure, len(args))

	_, err = client.db.ExecContext(ctx, sqlStr, args...)
	return err
}
