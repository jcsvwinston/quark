// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
	"strings"
)

// softDeletePredicate returns the soft-delete WHERE condition that should be
// prepended to the query, or nil when none applies. Behaviour:
//
//   - The model has no deleted_at column → nil (soft-delete is a no-op).
//   - q.unscoped is true → nil (caller asked for everything).
//   - q.onlyTrashed is true → "deleted_at IS NOT NULL" (caller wants only the
//     trashed rows; the inverse of the default scope).
//   - default → "deleted_at IS NULL".
//
// Centralised so the three call sites (buildSelect, Count, aggregate) stay
// in lock-step. Phase-1 F1-5.
func (q *BaseQuery) softDeletePredicate() *condition {
	if q.meta == nil {
		return nil
	}
	if _, hasDeletedAt := q.meta.FieldByCol["deleted_at"]; !hasDeletedAt {
		return nil
	}
	if q.unscoped {
		return nil
	}
	op := "IS NULL"
	if q.onlyTrashed {
		op = "IS NOT NULL"
	}
	return &condition{
		column:   "deleted_at",
		operator: op,
		logic:    "AND",
	}
}

// Restore clears the deleted_at column on the row identified by entity's
// primary key, "untrashing" it. Returns the number of rows affected.
//
// Restore implicitly scopes to currently-trashed rows (deleted_at IS NOT
// NULL): a Restore on a row that was never deleted is a 0-row no-op rather
// than a corrupting NULL-write. Useful as the inverse of Delete in admin
// flows.
//
// Phase-1 F1-5. Returns ErrInvalidModel when the model has no deleted_at.
func (q *Query[T]) Restore(entity *T) (int64, error) {
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if _, hasDeletedAt := q.meta.FieldByCol["deleted_at"]; !hasDeletedAt {
		return 0, fmt.Errorf("%w: Restore requires a deleted_at column on %s", ErrInvalidModel, q.meta.Table)
	}

	v := reflect.ValueOf(entity).Elem()
	pkVal := getPKValue(v, q.pk)
	if pkVal == nil {
		return 0, fmt.Errorf("%w: Restore requires a populated primary key", ErrInvalidQuery)
	}

	var sqlBuf strings.Builder
	sqlBuf.WriteString("UPDATE ")
	sqlBuf.WriteString(q.fullTableName())
	sqlBuf.WriteString(" SET ")
	sqlBuf.WriteString(q.dialect.Quote("deleted_at"))
	sqlBuf.WriteString(" = NULL WHERE ")
	sqlBuf.WriteString(q.dialect.Quote(q.pk.Column))
	sqlBuf.WriteString(" = ")
	sqlBuf.WriteString(q.dialect.Placeholder(1))
	// Only restore rows that are actually trashed; a Restore on a live row
	// must be a no-op rather than a stealth NULL write that could collide
	// with future logic.
	sqlBuf.WriteString(" AND ")
	sqlBuf.WriteString(q.dialect.Quote("deleted_at"))
	sqlBuf.WriteString(" IS NOT NULL")

	if q.tenantID != "" && q.tenantCol != "" {
		sqlBuf.WriteString(" AND ")
		sqlBuf.WriteString(q.dialect.Quote(q.tenantCol))
		sqlBuf.WriteString(" = ")
		sqlBuf.WriteString(q.dialect.Placeholder(2))
	}

	args := []any{pkVal}
	if q.tenantID != "" && q.tenantCol != "" {
		args = append(args, q.tenantID)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	res, err := q.executeExec(ctx, sqlBuf.String(), args)
	if err != nil {
		return 0, fmt.Errorf("Restore failed: %w", err)
	}
	rowsAffected := int64(0)
	if res != nil {
		rowsAffected, _ = res.RowsAffected()
	}

	// Clear the entity's deleted_at field in memory so the in-process struct
	// reflects the restored state without a re-read.
	if rowsAffected > 0 {
		if fm, ok := q.meta.FieldByCol["deleted_at"]; ok {
			f := v.Field(fm.Index)
			if f.CanSet() {
				f.Set(reflect.Zero(f.Type()))
			}
		}
	}

	return rowsAffected, nil
}
