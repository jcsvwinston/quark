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

// DEP-2026-006 (QK-59, QK-65). Upsert and UpsertBatch do the same on the six
// engines. An empty updateCols is insert-or-ignore everywhere: the
// conflicting row is left as it was, nil is returned, and the entity's key is
// written back only where a row was inserted. A duplicate of a unique key
// other than the conflict columns is ErrConstraintViolation everywhere, with
// nothing written: the conflict columns name the key the caller asked Upsert
// to arbitrate, and a duplicate elsewhere is the error a Create of the same
// row would be.
//
// PostgreSQL and SQLite do both by themselves: ON CONFLICT (…) names its
// arbiter. MySQL and MariaDB have no conflict target, so every assignment of
// ON DUPLICATE KEY UPDATE is guarded by the conflict columns
// (mysqlConflictMatch) and a statement that changed nothing reads the row
// back (upsertNotWritten): no row with the conflict key means the duplicate
// was elsewhere. SQL Server and Oracle match on the conflict columns only,
// and their identity key is not written by the MERGE's insert branch, so an
// entity that carries the key of another row and a conflict value no row has
// would insert a second row: the key is checked before the MERGE
// (carriedKeyIsAnotherRow), and without updateCols the MERGE has no WHEN
// MATCHED branch.
//
// The tenant-guarded update branch (QK-43) is the same clause with one more
// condition: conflictClause renders it for the INSERT-based engines,
// mergeMatchedClause for the MERGE ones, and upsertNotWritten tells the
// outcomes of a statement that met a row and wrote nothing apart — ignored,
// unchanged, another tenant's, or a duplicate elsewhere — by reading that
// row back.

// errUpsertDuplicateElsewhere is what an upsert returns when the entity
// duplicates a unique key other than the conflict columns (QK-65).
func (q *BaseQuery) errUpsertDuplicateElsewhere() error {
	return fmt.Errorf("%w: upsert into %s: the entity duplicates a unique key other than the conflict columns; nothing was written",
		ErrConstraintViolation, q.table)
}

// conflictClause renders the conflict clause of an INSERT-based upsert
// (PostgreSQL, SQLite, MySQL, MariaDB). argIndex is the next placeholder
// index after the INSERT's own arguments; the binds the clause adds come back
// in args. hasUpdate reports whether there is an update branch at all:
// without updateCols the clause is insert-or-ignore, which writes no row of
// any tenant and is not reported as a conflict (QK-59).
//
// Without a guard the clause is the dialect's own (Dialect.UpsertSQL). Under
// RowLevelSecurityClient the update branch only fires for a row of the
// resolved tenant (QK-43):
//
//	PostgreSQL, SQLite  ON CONFLICT (…) DO UPDATE SET … WHERE <table>.<tenant> = ?
//	MySQL, MariaDB      ON DUPLICATE KEY UPDATE c = IF(<conflict match> AND <tenant> = ?, VALUES(c), c), …
func (q *BaseQuery) conflictClause(conflictCols, updateCols []string, argIndex int) (clause string, args []any, hasUpdate bool, err error) {
	for _, c := range append(append([]string{}, conflictCols...), updateCols...) {
		if err := q.guard.ValidateIdentifier(c); err != nil {
			return "", nil, false, err
		}
	}
	hasUpdate = len(updateCols) > 0
	if !q.tenantGuarded() {
		return q.dialect.UpsertSQL(conflictCols, updateCols, argIndex), nil, hasUpdate, nil
	}
	tenant := q.dialect.Quote(q.tenantCol)
	switch q.upsertFamily() {
	case "on_conflict":
		quoted := make([]string, len(conflictCols))
		for i, c := range conflictCols {
			quoted[i] = q.dialect.Quote(c)
		}
		if !hasUpdate {
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
		if !hasUpdate {
			// The no-op of the unguarded clause, which writes no row of any
			// tenant (QK-61).
			qc := q.dialect.Quote(conflictCols[0])
			return " ON DUPLICATE KEY UPDATE " + qc + " = " + qc, nil, false, nil
		}
		// Every assignment keeps the existing value unless the row met is
		// the one the conflict columns name and it is the tenant's. MySQL
		// evaluates the assignments left to right and a later one sees an
		// earlier one's result, which is safe here: the condition reads the
		// conflict columns, which no assignment changes, and the tenant
		// column, which an assignment to it writes with the value it
		// already holds.
		match := mysqlConflictMatch(q.dialect, conflictCols)
		sets := make([]string, len(updateCols))
		for i, c := range updateCols {
			qc := q.dialect.Quote(c)
			sets[i] = fmt.Sprintf("%s = IF(%s AND %s = %s, VALUES(%s), %s)", qc, match, tenant, q.dialect.Placeholder(argIndex+i), qc, qc)
			args = append(args, q.tenantID)
		}
		return " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", "), args, true, nil
	default:
		return "", nil, false, fmt.Errorf("%w: no tenant-guarded conflict clause for dialect %q", ErrUnsupportedFeature, q.dialect.Name())
	}
}

// upsertRow is the row an upsert met, read back by its conflict columns.
type upsertRow struct {
	found bool
	// key is the row's single-column key, when the model has one; an
	// invalid Value otherwise.
	key reflect.Value
	// tenant is the row's tenant column, read under RowLevelSecurityClient.
	tenant sql.NullString
	// version is the row's version column, read on a versioned model.
	version int64
}

// readConflictRow reads the row the entity's conflict columns name, in any
// tenant: an upsert that met a row and wrote nothing cannot tell by itself
// whether the row is the tenant's, at the entity's version, or there at
// all. It runs on the primary, inside the caller's transaction when there is
// one.
func (q *BaseQuery) readConflictRow(ctx context.Context, v reflect.Value, conflictCols []string) (upsertRow, error) {
	var where []string
	var args []any
	argIndex := 1
	for _, c := range conflictCols {
		fm, ok := q.meta.FieldByCol[strings.ToLower(c)]
		if !ok {
			return upsertRow{}, fmt.Errorf("%w: conflict column %q is not a field of %s", ErrInvalidQuery, c, q.table)
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

	row := upsertRow{found: true}
	var cols []string
	var dests []any
	singleKey := q.pk.Column != "" && (q.meta == nil || !q.meta.HasCompositePK)
	if singleKey {
		row.key = reflect.New(v.Field(q.pk.Index).Type())
		cols = append(cols, q.dialect.Quote(q.pk.Column))
		dests = append(dests, row.key.Interface())
	}
	if q.tenantGuarded() {
		cols = append(cols, q.dialect.Quote(q.tenantCol))
		dests = append(dests, &row.tenant)
	}
	if vfm := versionFieldOf(q.meta); vfm != nil {
		cols = append(cols, q.dialect.Quote(vfm.Column))
		dests = append(dests, &row.version)
	}
	if len(cols) == 0 {
		var sink any
		cols = append(cols, q.dialect.Quote(conflictCols[0]))
		dests = append(dests, &sink)
	}
	sqlStr := "SELECT " + strings.Join(cols, ", ") + " FROM " + q.fullTableName() + " WHERE " + strings.Join(where, " AND ")
	err := q.executeQueryRow(ctx, sqlStr, args).Scan(dests...)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return upsertRow{}, nil
	case err != nil:
		return upsertRow{}, wrapDBError(err)
	}
	if singleKey {
		row.key = row.key.Elem()
	}
	return row, nil
}

// keyExists reports whether a row holds the key, in any tenant.
func (q *BaseQuery) keyExists(ctx context.Context, pkValue any) (bool, error) {
	keySQL, args := q.keyWhere(pkValue, 1)
	var n int64
	if err := q.executeQueryRow(ctx, "SELECT COUNT(*) FROM "+q.fullTableName()+" WHERE "+keySQL, args).Scan(&n); err != nil {
		return false, wrapDBError(err)
	}
	return n > 0, nil
}

// carriesKey reports whether the entity carries a non-zero single-column key.
func (q *BaseQuery) carriesKey(v reflect.Value) bool {
	return q.pk.Column != "" && (q.meta == nil || !q.meta.HasCompositePK) && !isZeroPKValue(v.Field(q.pk.Index))
}

// upsertNotWritten is the outcome of an upsert that met a row and wrote
// nothing, decided from the row read back by the conflict columns:
//
//   - no row holds the conflict columns' values: the duplicate was on another
//     unique key, ErrConstraintViolation (QK-65);
//   - no update branch: insert-or-ignore, nil, the key left as it was (QK-59);
//   - the row is another tenant's: ErrConstraintViolation, nothing written
//     (QK-43);
//   - the entity carries the key of another row, and a row holds that key:
//     the statement met that row on its key and left it alone — on
//     PostgreSQL the INSERT fails on the key — so ErrConstraintViolation;
//   - otherwise the row already held the values: nil, and the row is the one
//     the update branch met, so it is handed back for the entity's key.
func (q *BaseQuery) upsertNotWritten(ctx context.Context, v reflect.Value, conflictCols []string, hasUpdate bool) (upsertRow, error) {
	row, err := q.readConflictRow(ctx, v, conflictCols)
	if err != nil {
		return row, err
	}
	if !row.found {
		return row, q.errUpsertDuplicateElsewhere()
	}
	if !hasUpdate {
		return row, nil
	}
	if q.tenantGuarded() && (!row.tenant.Valid || row.tenant.String != q.tenantID) {
		return row, q.errUpsertOutsideTenant()
	}
	if row.key.IsValid() && q.carriesKey(v) && !reflect.DeepEqual(row.key.Interface(), v.Field(q.pk.Index).Interface()) {
		taken, err := q.keyExists(ctx, getPKValue(v, q.pk))
		if err != nil {
			return row, err
		}
		if taken {
			return row, q.errUpsertDuplicateElsewhere()
		}
	}
	return row, nil
}

// carriedKeyIsAnotherRow refuses, before a MERGE, an entity that carries the
// key of a row other than the one its conflict columns name: the MERGE
// matches on the conflict columns only and its insert branch does not write
// the identity key, so it would insert a second row with a key of its own and
// leave the carried key unwritten — on PostgreSQL the INSERT fails on the
// primary key instead (QK-65). Only an entity with a single integer key is
// checked: any other key is written by the insert branch, where the engine
// refuses a duplicate itself. When a row holds the conflict columns' values
// the MERGE meets it whatever key the entity carries, as ON CONFLICT does.
func (q *BaseQuery) carriedKeyIsAnotherRow(ctx context.Context, v reflect.Value, conflictCols []string) error {
	if !q.upsertIntegerKey() || isZeroPKValue(v.Field(q.pk.Index)) {
		return nil
	}
	row, err := q.readConflictRow(ctx, v, conflictCols)
	if err != nil {
		return err
	}
	if row.found {
		return nil
	}
	taken, err := q.keyExists(ctx, getPKValue(v, q.pk))
	if err != nil {
		return err
	}
	if taken {
		return q.errUpsertDuplicateElsewhere()
	}
	return nil
}

// upsertInsertStyleRow runs one INSERT-based upsert on PostgreSQL, SQLite,
// MySQL or MariaDB, plain or tenant-guarded. insertSQL is the INSERT without
// its RETURNING clause, returning that clause ("" when there is none) and
// args the INSERT's arguments.
//
// A statement that met a row and wrote nothing — RETURNING hands back no row
// on PostgreSQL and SQLite, zero rows affected on MySQL and MariaDB, whose
// count is of changed rows — is told apart by upsertNotWritten.
func (q *BaseQuery) upsertInsertStyleRow(ctx context.Context, v reflect.Value, insertSQL, returning string, args []any, conflictCols, updateCols []string) error {
	clause, clauseArgs, hasUpdate, err := q.conflictClause(conflictCols, updateCols, len(args)+1)
	if err != nil {
		return err
	}
	args = append(args, clauseArgs...)
	if q.upsertFamily() == "duplicate_key" {
		// A single integer key: the statement reports the key of the row it
		// inserted or met (QK-63, see duplicateKeyKeyAssignment).
		carried := q.carriesKey(v)
		keyAssign, keyArgs := q.duplicateKeyKeyAssignment(clause, len(args)+1)
		args = append(args, keyArgs...)
		res, err := q.executeExec(ctx, insertSQL+clause+keyAssign, args)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			row, err := q.upsertNotWritten(ctx, v, conflictCols, hasUpdate)
			if err != nil {
				return err
			}
			if hasUpdate && row.key.IsValid() {
				// The update branch met the tenant's own row and changed no
				// value: the row's key is the entity's, as when a value
				// changes (QK-63).
				v.Field(q.pk.Index).Set(row.key)
				q.invalidateRowTags(ctx, getPKValue(v, q.pk))
			}
			return nil
		}
		if keyAssign != "" {
			if key, found, write := duplicateKeyRowKey(res, carried); found {
				if write {
					setPKValue(v, q.pk, key)
				}
				// executeExec dropped the table tag; the row's tag can only
				// be dropped now that its key is known (QK-66).
				q.invalidateRowTags(ctx, getPKValue(v, q.pk))
			}
		}
		return nil
	}
	if returning != "" {
		err := q.scanReturning(q.executeQueryRow(ctx, insertSQL+clause+returning, args), v)
		if errors.Is(err, sql.ErrNoRows) {
			// Nothing to return: DO NOTHING met a row, or the guarded DO
			// UPDATE skipped it. The entity's key is not written back.
			_, err := q.upsertNotWritten(ctx, v, conflictCols, hasUpdate)
			return err
		}
		if err != nil {
			return err
		}
		// A single-row query drops nothing from the cache; the upsert does:
		// the table tag and the tag of the row it inserted or updated, whose
		// key RETURNING just wrote into the entity (QK-66).
		q.invalidateInsert(ctx, getPKValue(v, q.pk))
		return nil
	}
	res, err := q.executeExec(ctx, insertSQL+clause, args)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err := q.upsertNotWritten(ctx, v, conflictCols, hasUpdate)
		return err
	}
	return nil
}
