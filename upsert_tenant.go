// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// QK-43. Under RowLevelSecurityClient the update branch of Upsert and
// UpsertBatch was not confined to the tenant: ON CONFLICT … DO UPDATE, ON
// DUPLICATE KEY UPDATE and MERGE … WHEN MATCHED applied to whichever row held
// the conflicting key, in any tenant. Tenant A upserting a code that tenant B
// already had rewrote B's row — and on SQL Server and Oracle, where an empty
// updateCols means "every column but the conflict ones", moved it into A by
// writing A's id into the tenant column.
//
// The update branch now carries the tenant predicate on every engine:
//
//	PostgreSQL, SQLite  ON CONFLICT (…) DO UPDATE SET … WHERE <table>.<tenant> = ?
//	MySQL, MariaDB      ON DUPLICATE KEY UPDATE c = IF(<tenant> = ?, VALUES(c), c), …
//	SQL Server          MERGE … WHEN MATCHED AND target.<tenant> = @p THEN UPDATE …
//	Oracle              MERGE … WHEN MATCHED THEN UPDATE SET … WHERE target.<tenant> = :n
//
// so a row of another tenant is never written. A conflict with such a row
// cannot be an insert either — the key is taken — so the call returns
// ErrConstraintViolation, the error a Create of the same key returns, and
// writes nothing: UpsertBatch runs in a transaction under this strategy and
// rolls the batch back. A dialect quark does not know how to guard refuses
// the upsert with ErrUnsupportedFeature rather than run it unguarded.

// tenantGuarded reports whether the query's writes are confined by
// RowLevelSecurityClient: a resolved tenant and the column that holds it.
func (q *BaseQuery) tenantGuarded() bool {
	return q.tenantID != "" && q.tenantCol != ""
}

// errUpsertOutsideTenant is what an upsert returns when its conflict key is
// held by a row of another tenant (QK-43).
func (q *BaseQuery) errUpsertOutsideTenant() error {
	return fmt.Errorf("%w: upsert into %s under RowLevelSecurityClient: a row with the same conflict key belongs to another tenant; it was not updated and no row was inserted",
		ErrConstraintViolation, q.table)
}

// warnForeignTenantValue logs a value for the tenant column that was replaced
// by the resolved tenant (QK-42, QK-44).
func (q *BaseQuery) warnForeignTenantValue() {
	if q.client == nil || q.client.logger == nil {
		return
	}
	q.client.logger.Warn("the write carried another tenant's id in the tenant column; RowLevelSecurityClient writes the resolved tenant instead",
		"event", "quark.tenant.foreign_value_replaced",
		"table", q.table,
		"column", q.tenantCol,
	)
}

// upsertFamily names the conflict clause an INSERT-based upsert uses on the
// query's dialect: "on_conflict" (PostgreSQL, SQLite), "duplicate_key"
// (MySQL, MariaDB), "merge" (SQL Server, Oracle), or "" for a dialect quark
// does not know how to confine to a tenant.
func (q *BaseQuery) upsertFamily() string {
	switch q.dialect.Name() {
	case "postgres", "sqlite":
		return "on_conflict"
	case "mysql", "mariadb":
		return "duplicate_key"
	case "mssql", "oracle":
		return "merge"
	default:
		return ""
	}
}

// checkTenantUpsert refuses an upsert under RowLevelSecurityClient that
// cannot be confined: a dialect without a known guarded form, or a model
// without the tenant column (the guard would name a column that is not
// there). Nil when the query is not tenant-guarded.
func (q *BaseQuery) checkTenantUpsert() error {
	if !q.tenantGuarded() {
		return nil
	}
	if q.upsertFamily() == "" {
		return fmt.Errorf("%w: Upsert under RowLevelSecurityClient needs an update branch confined to the tenant, and quark has no such form for dialect %q",
			ErrUnsupportedFeature, q.dialect.Name())
	}
	if q.meta == nil {
		return fmt.Errorf("%w: Upsert under RowLevelSecurityClient needs model metadata", ErrInvalidModel)
	}
	if _, ok := q.meta.FieldByCol[strings.ToLower(q.tenantCol)]; !ok {
		return fmt.Errorf("%w: Upsert under RowLevelSecurityClient needs the tenant column %q on %s",
			ErrInvalidModel, q.tenantCol, q.table)
	}
	return q.guard.ValidateIdentifier(q.tenantCol)
}

// guardedConflictClause renders the conflict clause of an INSERT-based upsert
// (PostgreSQL, SQLite, MySQL, MariaDB) with its update branch confined to the
// tenant. argIndex is the next placeholder index after the INSERT's own
// arguments; the tenant binds come back in args. hasUpdate reports whether
// there is an update branch at all: without updateCols, PostgreSQL and SQLite
// do nothing on a conflict and MySQL assigns the first conflict column to
// itself — insert-or-ignore, which writes no row of another tenant and is
// not reported as a conflict.
func (q *BaseQuery) guardedConflictClause(conflictCols, updateCols []string, argIndex int) (clause string, args []any, hasUpdate bool, err error) {
	for _, c := range append(append([]string{}, conflictCols...), updateCols...) {
		if err := q.guard.ValidateIdentifier(c); err != nil {
			return "", nil, false, err
		}
	}
	tenant := q.dialect.Quote(q.tenantCol)
	switch q.upsertFamily() {
	case "on_conflict":
		quoted := make([]string, len(conflictCols))
		for i, c := range conflictCols {
			quoted[i] = q.dialect.Quote(c)
		}
		if len(updateCols) == 0 {
			return fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", strings.Join(quoted, ", ")), nil, false, nil
		}
		sets := make([]string, len(updateCols))
		for i, c := range updateCols {
			sets[i] = fmt.Sprintf("%s = excluded.%s", q.dialect.Quote(c), q.dialect.Quote(c))
		}
		// The existing row is referenced by the table's name, without the
		// schema: that is the form both engines accept in this clause.
		return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s WHERE %s.%s = %s",
				strings.Join(quoted, ", "), strings.Join(sets, ", "),
				q.dialect.Quote(q.table), tenant, q.dialect.Placeholder(argIndex)),
			[]any{q.tenantID}, true, nil
	case "duplicate_key":
		if len(updateCols) == 0 {
			// The no-op of the unguarded clause, which writes no row of any
			// tenant. It was IF(<tenant> = ?, VALUES(<col>), <col>), which on
			// a duplicate of another unique key wrote the incoming value into
			// the tenant's own row (QK-61).
			qc := q.dialect.Quote(conflictCols[0])
			return " ON DUPLICATE KEY UPDATE " + qc + " = " + qc, nil, false, nil
		}
		// Every assignment keeps the existing value unless the existing row
		// is the tenant's. MySQL evaluates the assignments left to right and
		// a later one sees an earlier one's result, which is safe here: the
		// only column the condition reads is the tenant column, and an
		// assignment to it writes the resolved tenant onto a row that already
		// holds it.
		sets := make([]string, len(updateCols))
		for i, c := range updateCols {
			qc := q.dialect.Quote(c)
			sets[i] = fmt.Sprintf("%s = IF(%s = %s, VALUES(%s), %s)", qc, tenant, q.dialect.Placeholder(argIndex+i), qc, qc)
			args = append(args, q.tenantID)
		}
		return " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", "), args, true, nil
	default:
		return "", nil, false, fmt.Errorf("%w: no tenant-guarded conflict clause for dialect %q", ErrUnsupportedFeature, q.dialect.Name())
	}
}

// finishGuardedDuplicateKey completes a tenant-guarded MySQL or MariaDB
// upsert of one row. n is the statement's rows affected: 1 for an insert, 2
// for an update, and 0 both when the tenant's own row already held the values
// and when the conflicting row belongs to another tenant — the guard kept its
// values. Those two are told apart by reading the row back by its conflict
// key within the tenant, which also returns the key of an updated row:
// LAST_INSERT_ID does not, and RETURNING (MariaDB) would hand back the other
// tenant's id, so neither is used under the guard.
func (q *BaseQuery) finishGuardedDuplicateKey(ctx context.Context, v reflect.Value, conflictCols []string, n int64, hasUpdate bool) error {
	needPK := q.pk.Column != "" && (q.meta == nil || !q.meta.HasCompositePK) && isZeroPKValue(v.Field(q.pk.Index))
	if n != 0 && !needPK {
		return nil
	}
	var where []string
	var args []any
	argIndex := 1
	for _, c := range conflictCols {
		fm, ok := q.meta.FieldByCol[strings.ToLower(c)]
		if !ok {
			return fmt.Errorf("%w: conflict column %q is not a field of %s", ErrInvalidQuery, c, q.table)
		}
		f := v.Field(fm.Index)
		if (f.Kind() == reflect.Ptr || f.Kind() == reflect.Interface) && f.IsNil() {
			where = append(where, q.dialect.Quote(c)+" IS NULL")
			continue
		}
		where = append(where, q.dialect.Quote(c)+" = "+q.dialect.Placeholder(argIndex))
		args = append(args, q.bindColumnArg(c, f.Interface()))
		argIndex++
	}
	where = append(where, q.dialect.Quote(q.tenantCol)+" = "+q.dialect.Placeholder(argIndex))
	args = append(args, q.tenantID)

	selectCol := q.pk.Column
	if selectCol == "" || (q.meta != nil && q.meta.HasCompositePK) {
		selectCol = q.tenantCol
	}
	sqlStr := "SELECT " + q.dialect.Quote(selectCol) + " FROM " + q.fullTableName() +
		" WHERE " + strings.Join(where, " AND ")

	var dest reflect.Value
	if needPK {
		dest = reflect.New(v.Field(q.pk.Index).Type())
	} else {
		var sink any
		dest = reflect.ValueOf(&sink)
	}
	err := q.executeQueryRow(ctx, sqlStr, args).Scan(dest.Interface())
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if n == 0 && hasUpdate {
			return q.errUpsertOutsideTenant()
		}
		return nil
	case err != nil:
		return wrapDBError(err)
	}
	if needPK {
		v.Field(q.pk.Index).Set(dest.Elem())
	}
	return nil
}

// confineTenantColumn returns data with every key that names the tenant
// column set to the resolved tenant, under RowLevelSecurityClient (QK-44).
// UpdateMap wrote its map verbatim, so {"tenant_id": "tb"} moved a row of
// tenant ta into tb — the move Create and the updates by entity no longer
// allow since QK-42. A different value is replaced and logged, as there. The
// caller's map is not modified.
func (q *BaseQuery) confineTenantColumn(data map[string]any) map[string]any {
	if !q.tenantGuarded() {
		return data
	}
	var out map[string]any
	for k, val := range data {
		if !strings.EqualFold(k, q.tenantCol) {
			continue
		}
		if s, ok := val.(string); ok && s == q.tenantID {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(data))
			for k2, v2 := range data {
				out[k2] = v2
			}
		}
		out[k] = q.tenantID
	}
	if out == nil {
		return data
	}
	q.warnForeignTenantValue()
	return out
}

// upsertGuardedInsertStyle runs one tenant-guarded INSERT-based upsert on
// PostgreSQL, SQLite, MySQL or MariaDB. insertSQL is the INSERT without its
// RETURNING clause, returning that clause ("" when there is none) and args
// the INSERT's arguments.
//
// On PostgreSQL and SQLite a conflict with another tenant's row leaves the
// statement with nothing to return and nothing affected — the WHERE on DO
// UPDATE skipped it — and that is the error. On MySQL and MariaDB see
// finishGuardedDuplicateKey.
func (q *BaseQuery) upsertGuardedInsertStyle(ctx context.Context, v reflect.Value, insertSQL, returning string, args []any, conflictCols, updateCols []string) error {
	clause, guardArgs, hasUpdate, err := q.guardedConflictClause(conflictCols, updateCols, len(args)+1)
	if err != nil {
		return err
	}
	args = append(args, guardArgs...)
	if q.upsertFamily() == "duplicate_key" {
		res, err := q.executeExec(ctx, insertSQL+clause, args)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		return q.finishGuardedDuplicateKey(ctx, v, conflictCols, n, hasUpdate)
	}
	if returning != "" {
		err := q.scanReturning(q.executeQueryRow(ctx, insertSQL+clause+returning, args), v)
		if errors.Is(err, sql.ErrNoRows) {
			if hasUpdate {
				return q.errUpsertOutsideTenant()
			}
			// DO NOTHING on a conflict, with the tenant's row or another's:
			// nothing written, nothing to report (QK-48).
			return nil
		}
		return err
	}
	res, err := q.executeExec(ctx, insertSQL+clause, args)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); hasUpdate && n == 0 {
		return q.errUpsertOutsideTenant()
	}
	return nil
}

// splitReturning separates an INSERT built by buildInsert from its RETURNING
// clause, which an upsert appends after its conflict clause.
func splitReturning(insert string) (string, string) {
	if i := strings.Index(insert, " RETURNING "); i != -1 {
		return insert[:i], insert[i:]
	}
	return insert, ""
}
