// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"reflect"
	"strings"
)

// QK-40. The writes that address one row by its key — Update, UpdateBatch,
// Delete, HardDelete, DeleteBatch and Restore — wrote by the key alone and
// ignored the query's conditions, the tenant predicate of
// RowLevelSecurityClient among them: an id taken from a request wrote another
// tenant's row even when the caller had guarded the query with Where. They
// now AND the conditions with the key, the way UpdateFields does since QK-39,
// so a condition can only narrow which row is written, never widen it.
//
// When no row with the key satisfies the conditions the write touches
// nothing, reports zero rows affected and returns no error — what UpdateMap,
// DeleteBy and UpdateFields already report when their conditions match
// nothing — and runs no After hook, records no audit entry and emits no
// event, because nothing happened.

// errExcludedByWhere is how saveAny tells its caller that the row it was
// asked to update does not satisfy the query's conditions, so nothing was
// written. It never leaves the package: Update turns it into (0, nil) and
// skips its After hook, audit entry and event; an association save treats it
// as an association that was not written.
var errExcludedByWhere = errors.New("quark: no row with the key satisfies the query's conditions")

// pkValueOf returns the entity's key in the shape keyWhere takes: the value
// itself for a single primary key, a []any in CompositePK order for a
// composite one.
func (q *BaseQuery) pkValueOf(v reflect.Value) any {
	if q.meta != nil && q.meta.HasCompositePK {
		vals := make([]any, len(q.meta.CompositePK))
		for j, cpk := range q.meta.CompositePK {
			vals[j] = v.Field(cpk.Index).Interface()
		}
		return vals
	}
	return getPKValue(v, q.pk)
}

// keyWhere renders the primary-key predicate for pkValue (see pkValueOf),
// with its placeholders starting at argIndex.
func (q *BaseQuery) keyWhere(pkValue any, argIndex int) (string, []any) {
	var sb strings.Builder
	var args []any
	if q.meta != nil && q.meta.HasCompositePK {
		pkVals, _ := pkValue.([]any)
		for j, cpk := range q.meta.CompositePK {
			if j > 0 {
				sb.WriteString(" AND ")
			}
			sb.WriteString(q.dialect.Quote(cpk.Column))
			sb.WriteString(" = ")
			sb.WriteString(q.dialect.Placeholder(argIndex))
			var val any
			if j < len(pkVals) {
				val = pkVals[j]
			}
			args = append(args, val)
			argIndex++
		}
		return sb.String(), args
	}
	sb.WriteString(q.dialect.Quote(q.pk.Column))
	sb.WriteString(" = ")
	sb.WriteString(q.dialect.Placeholder(argIndex))
	return sb.String(), []any{pkValue}
}

// keyPassesWhere reports whether a row with the key satisfies the query's
// conditions, scopes included. It runs on the primary, inside the caller's
// transaction when there is one, and only where a guarded UPDATE cannot tell
// by itself:
//
//   - after it affected no row. On a versioned model that is the version
//     predicate failing (ErrStaleEntity) when the row passes the conditions,
//     and the conditions excluding it otherwise; ErrStaleEntity is not the
//     answer for a row the caller's own conditions left out. On MySQL and
//     MariaDB it also tells "matched, nothing changed" — their rows-affected
//     counts changed rows, not matched ones — from "excluded".
//   - before Update writes a loaded belongs_to association, which it writes
//     ahead of the entity's own row: a row the conditions exclude must not
//     have its associations rewritten either.
//
// The probe asks only about rows the conditions let the caller see, so its
// answer cannot tell a row of another tenant from a row that does not exist.
func (q *BaseQuery) keyPassesWhere(ctx context.Context, pkValue any) (bool, error) {
	keySQL, args := q.keyWhere(pkValue, 1)
	whereSQL, whereArgs, err := q.whereForWrite(len(args) + 1)
	if err != nil {
		return false, err
	}
	var sb strings.Builder
	sb.WriteString("SELECT COUNT(*) FROM ")
	sb.WriteString(q.fullTableName())
	sb.WriteString(" WHERE ")
	sb.WriteString(keySQL)
	if whereSQL != "" {
		sb.WriteString(" AND ")
		sb.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}
	var n int64
	if err := q.executeQueryRow(ctx, sb.String(), args).Scan(&n); err != nil {
		return false, wrapDBError(err)
	}
	return n > 0, nil
}

// tenantScopeFor returns the tenant predicate a write to a related model
// carries: the RowLevelSecurityClient tenant, as a scope, when the related
// model has the tenant column; nil otherwise. An association Update writes
// is a by-key write like the entity's own, and before this it carried no
// tenant predicate at all: a loaded association of another tenant was
// rewritten by its key.
func (q *BaseQuery) tenantScopeFor(meta *ModelMeta) []condition {
	if q.tenantID == "" || q.tenantCol == "" || meta == nil {
		return nil
	}
	if _, ok := meta.FieldByCol[strings.ToLower(q.tenantCol)]; !ok {
		return nil
	}
	return []condition{{
		column:   q.tenantCol,
		operator: "=",
		value:    q.tenantID,
		logic:    "AND",
		scope:    true,
	}}
}

// hasLoadedBelongsTo reports whether saveAny is about to write a belongs_to
// association of the entity — one whose field is loaded (non-zero).
func hasLoadedBelongsTo(meta *ModelMeta, elem reflect.Value) bool {
	for _, rel := range meta.Relations {
		if rel.Type != "belongs_to" {
			continue
		}
		if f := elem.FieldByName(rel.Field); f.IsValid() && !f.IsZero() {
			return true
		}
	}
	return false
}
