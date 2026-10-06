// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// CreateBatch on an engine without RETURNING, with a generated key the caller
// needs back: MySQL and SQL Server (QK-36).
//
// Until A12 Q3 every row went in its own INSERT, one round trip each, because
// that was the only way to read each row's key back. The per-row form stays,
// as the fallback; what changed is that a chunk now goes in ONE round trip
// wherever the engine can prove which key each row got, and only there. A wrong key handed back to the caller is data
// corruption — the next Update writes another row — so "probably right" is
// not enough to leave the per-row form.
//
//   - SQL Server: the chunk is one insert-only MERGE whose OUTPUT clause
//     returns each row's key next to the row's position in the chunk (a
//     plain INSERT … OUTPUT does not promise the order of its rows). The key
//     is the IDENTITY value the per-row form read with SCOPE_IDENTITY(). If
//     the MERGE fails, the server undoes it and says so, and the chunk goes
//     row by row: the caller sees what the per-row form leaves.
//   - MySQL: the chunk is one multi-row INSERT. LAST_INSERT_ID() is the key
//     of its FIRST row, and the others follow only if the engine allocated
//     them as one consecutive run. MySQL documents that it does for any
//     statement under innodb_autoinc_lock_mode 0 and 1, and does NOT promise
//     it under 2 — the MySQL 8 default — unless no other kind of insert runs
//     at the same time, which no client can check. So the one-round-trip form runs only where it is documented
//     (mysqlBatchIDsProvable), and computes each key from the first, the row
//     count and auto_increment_increment read on the same connection. A
//     multi-row INSERT is atomic where single-row INSERTs were not, so the
//     chunk runs inside a transaction (or a savepoint of the caller's) and,
//     if it fails, is undone and run again row by row: the caller sees what
//     the per-row form would have left.
//
// Everything else — another engine, a key that is not an integer, a table
// the probe cannot vouch for, a chunk too small to win — takes the per-row
// form, unchanged.
func (q *Query[T]) createBatchBackfill(entities []*T, columns []string, colIndexes []int, colTags []string) error {
	first := reflect.ValueOf(entities[0])
	if first.Kind() == reflect.Ptr {
		first = first.Elem()
	}
	if len(colIndexes) == 0 || !isIntegerKind(first.Field(q.pk.Index).Kind()) {
		return q.createBatchBackfillPerRow(entities, columns, colIndexes, colTags)
	}
	switch q.dialect.Name() {
	case "mssql":
		if q.mssqlBatchIDsProvable() {
			return q.createBatchMSSQL(entities, columns, colIndexes, colTags)
		}
	case "mysql":
		if q.mysqlBatchIDsProvable() {
			return q.createBatchMySQL(entities, columns, colIndexes, colTags)
		}
	}
	return q.createBatchBackfillPerRow(entities, columns, colIndexes, colTags)
}

func isIntegerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// batchIDsProbe asks the engine once per client and table whether the
// one-round-trip form can prove its keys, and remembers the answer. A probe
// that fails is not remembered: the batch takes the per-row form and the next
// one asks again.
func (q *BaseQuery) batchIDsProbe(probe func() (bool, error)) bool {
	key := q.dialect.Name() + "\x00" + q.fullTableName() + "\x00" + q.pk.Column
	if v, ok := q.client.batchIDsProvable.Load(key); ok {
		return v.(bool)
	}
	ok, err := probe()
	if err != nil {
		return false
	}
	q.client.batchIDsProvable.Store(key, ok)
	return ok
}

// --- SQL Server ------------------------------------------------------------------

// mssqlBatchIDsProvable: the table is a user table, its key column is an
// IDENTITY column, and no INSTEAD OF trigger takes its inserts. Those are the
// conditions under which the key the per-row form reads, SCOPE_IDENTITY()
// after the row's INSERT, and the key the one-round-trip form reads, the
// inserted row's key column in an OUTPUT clause, are the same value; on any
// other table the per-row form keeps its behaviour, including the error a
// NULL SCOPE_IDENTITY() gives it.
func (q *Query[T]) mssqlBatchIDsProvable() bool {
	return q.batchIDsProbe(func() (bool, error) {
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()
		var userTable, identity sql.NullInt64
		var insteadOf int64
		err := q.client.queryRowStmt(ctx, q.exec, stmt{kind: StatementIntrospection, op: "QUERY_ROW", table: q.table},
			"SELECT OBJECTPROPERTY(OBJECT_ID(@p1), 'IsUserTable'), COLUMNPROPERTY(OBJECT_ID(@p1), @p2, 'IsIdentity'), "+
				"(SELECT COUNT(*) FROM sys.triggers WHERE parent_id = OBJECT_ID(@p1) AND is_instead_of_trigger = 1)",
			[]any{q.fullTableName(), q.pk.Column}).Scan(&userTable, &identity, &insteadOf)
		if err != nil {
			return false, err
		}
		return userTable.Valid && userTable.Int64 == 1 && identity.Valid && identity.Int64 == 1 && insteadOf == 0, nil
	})
}

// createBatchMSSQL sends each chunk as one batch around one MERGE:
//
//	SET NOCOUNT ON;
//	DECLARE @quark_tc INT = @@TRANCOUNT;
//	DECLARE @quark_ids TABLE (quark_ord INT NOT NULL PRIMARY KEY, quark_id BIGINT NULL);
//	BEGIN TRY
//	MERGE INTO t AS quark_t
//	USING (VALUES (0, @p1, @p2), (1, @p3, @p4)) AS quark_src (quark_ord, a, b) ON 1 = 0
//	WHEN NOT MATCHED THEN INSERT (a, b) VALUES (quark_src.a, quark_src.b)
//	OUTPUT quark_src.quark_ord, INSERTED.id INTO @quark_ids (quark_ord, quark_id);
//	END TRY
//	BEGIN CATCH
//	IF XACT_STATE() = -1 OR @@TRANCOUNT <> @quark_tc THROW;
//	SELECT -1, NULL;
//	RETURN;
//	END CATCH;
//	SELECT quark_ord, quark_id FROM @quark_ids ORDER BY quark_ord;
//
// Why a MERGE and not INSERT … OUTPUT INSERTED.id: SQL Server does not
// guarantee that OUTPUT returns the rows in the order of the VALUES list, so
// the keys of a plain multi-row INSERT cannot be matched to the entities. An
// insert-only MERGE can name a column of its source in OUTPUT, so each key
// comes back with the position of its row. The ON 1 = 0 never matches: every
// source row is inserted, as the INSERT would insert it.
//
// A MERGE is one statement, so it inserts the whole chunk or nothing, where
// the per-row form left the rows before a failing one. When it fails, the
// CATCH block answers a single row (-1, NULL), which says that the server
// caught the error and undid the statement; the chunk then goes row by row,
// and the caller sees what the per-row form leaves. When the error took the
// caller's transaction with it — a deadlock, or any error under
// SET XACT_ABORT ON — running the rows again would run them outside it, so
// the error is raised instead, as the per-row form would have raised it.
//
// The rows of a VALUES list share one type per column, the highest of the
// parameters' types. Every row binds the same Go type for a column — that is
// what a struct field is — and a NULL parameter widens nothing, so the
// values reach the table converted exactly as single-row INSERTs convert
// them. A chunk where a column's values are not all of one type (a field of
// interface type) goes row by row instead.
func (q *Query[T]) createBatchMSSQL(entities []*T, columns []string, colIndexes []int, colTags []string) error {
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	rowsPerChunk := max(batchBindParamCeiling("mssql")/len(colIndexes), 1)
	for start := 0; start < len(entities); start += rowsPerChunk {
		chunk := entities[start:min(start+rowsPerChunk, len(entities))]
		done, err := q.createBatchMSSQLChunk(ctx, chunk, columns, colIndexes, colTags)
		if err != nil {
			return err
		}
		if !done {
			if err := q.backfillPerRow(ctx, chunk, columns, colIndexes, colTags); err != nil {
				return err
			}
		}
	}
	return nil
}

// createBatchMSSQLChunk runs one chunk's MERGE and sets the keys. done=false
// with no error means nothing was inserted and the chunk goes row by row.
func (q *Query[T]) createBatchMSSQLChunk(ctx context.Context, chunk []*T, columns []string, colIndexes []int, colTags []string) (done bool, err error) {
	args := make([]any, 0, len(chunk)*len(colIndexes))
	colTypes := make([]reflect.Type, len(colIndexes))
	for _, entity := range chunk {
		v := reflect.ValueOf(entity).Elem()
		q.ensureTenantID(v)
		for j, ci := range colIndexes {
			a := q.bindColumnArg(colTags[j], v.Field(ci).Interface())
			if a != nil {
				switch t := reflect.TypeOf(a); {
				case colTypes[j] == nil:
					colTypes[j] = t
				case colTypes[j] != t:
					return false, nil
				}
			}
			args = append(args, a)
		}
	}

	var b strings.Builder
	b.Grow(512 + len(chunk)*(10+8*len(colIndexes)) + 4*len(strings.Join(columns, ", ")))
	b.WriteString("SET NOCOUNT ON;\nDECLARE @quark_tc INT = @@TRANCOUNT;\n")
	b.WriteString("DECLARE @quark_ids TABLE (quark_ord INT NOT NULL PRIMARY KEY, quark_id BIGINT NULL);\n")
	b.WriteString("BEGIN TRY\nMERGE INTO ")
	b.WriteString(q.fullTableName())
	b.WriteString(" AS quark_t\nUSING (VALUES ")
	argIndex := 1
	for ord := range chunk {
		if ord > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		b.WriteString(strconv.Itoa(ord))
		for range colIndexes {
			b.WriteString(", ")
			writePlaceholder(&b, q.dialect, argIndex)
			argIndex++
		}
		b.WriteByte(')')
	}
	b.WriteString(") AS quark_src (quark_ord")
	for _, col := range columns {
		b.WriteString(", ")
		b.WriteString(col)
	}
	b.WriteString(") ON 1 = 0\nWHEN NOT MATCHED THEN INSERT (")
	b.WriteString(strings.Join(columns, ", "))
	b.WriteString(") VALUES (")
	for j, col := range columns {
		if j > 0 {
			b.WriteString(", ")
		}
		b.WriteString("quark_src.")
		b.WriteString(col)
	}
	b.WriteString(")\nOUTPUT quark_src.quark_ord, INSERTED.")
	b.WriteString(q.dialect.Quote(q.pk.Column))
	b.WriteString(" INTO @quark_ids (quark_ord, quark_id);\nEND TRY\nBEGIN CATCH\n")
	b.WriteString("IF XACT_STATE() = -1 OR @@TRANCOUNT <> @quark_tc THROW;\nSELECT -1, NULL;\nRETURN;\nEND CATCH;\n")
	b.WriteString("SELECT quark_ord, quark_id FROM @quark_ids ORDER BY quark_ord;")

	// A write that reads rows back: on the primary, never a replica. The
	// seam drops nothing from the cache for it; invalidateBatchInsert does.
	rows, err := q.executeQueryPrimary(ctx, b.String(), args)
	if err != nil {
		return false, wrapDBError(err)
	}
	defer rows.Close()
	ids := make([]int64, len(chunk))
	got := 0
	for rows.Next() {
		var ord int64
		var id sql.NullInt64
		if err := rows.Scan(&ord, &id); err != nil {
			return false, wrapDBError(err)
		}
		if ord == -1 {
			// The server caught the MERGE's error and undid it.
			return false, nil
		}
		if ord < 0 || ord >= int64(len(chunk)) || !id.Valid {
			return false, fmt.Errorf("%w: CreateBatch read back key %v for row %d of a chunk of %d", ErrInvalidQuery, id, ord, len(chunk))
		}
		ids[ord] = id.Int64
		got++
	}
	if err := rows.Err(); err != nil {
		return false, wrapDBError(err)
	}
	if got != len(chunk) {
		return false, fmt.Errorf("%w: CreateBatch inserted a chunk of %d rows and read back %d keys", ErrInvalidQuery, len(chunk), got)
	}
	pks := make([]any, len(chunk))
	for k, entity := range chunk {
		setPKValue(reflect.ValueOf(entity).Elem(), q.pk, ids[k])
		pks[k] = ids[k]
	}
	q.invalidateBatchInsert(ctx, pks)
	return true, nil
}

// --- MySQL -----------------------------------------------------------------------

// mysqlBatchMinRows is the smallest chunk the one-round-trip form takes. It
// costs four round trips — BEGIN, the INSERT, the read of
// auto_increment_increment, COMMIT — so a chunk of fewer rows is cheaper row
// by row.
const mysqlBatchMinRows = 5

// mysqlBatchIDsProvable: the conditions under which MySQL documents that the
// keys of a multi-row INSERT are one consecutive run (MySQL 8.0 Reference
// Manual, "AUTO_INCREMENT Handling in InnoDB"):
//
//   - innodb_autoinc_lock_mode is 0 ("traditional") or 1 ("consecutive"):
//     "auto-increment numbers assigned by any given statement are
//     consecutive". Under 2 ("interleaved"), the MySQL 8 default, the manual
//     promises it only "if the only statements executing are simple
//     inserts", which no client can check. The variable is read-only while
//     the server runs, so reading it once per client is reading it.
//   - the table is InnoDB, the setting's subject, and the key column is its
//     AUTO_INCREMENT column — LAST_INSERT_ID() reports that column, whatever
//     the model calls its key.
//   - no trigger runs on INSERT: a BEFORE INSERT trigger can set the key
//     itself, and then LAST_INSERT_ID() is not the row's key.
//   - the server is MySQL, not TiDB or Vitess, which answer to the same
//     variables with allocators of their own.
//
// auto_increment_increment is the step between consecutive keys. It has a
// session value, so it is read on the INSERT's own connection, every time.
func (q *Query[T]) mysqlBatchIDsProvable() bool {
	if q.tx == nil && q.exec != Executor(q.client.db) {
		return false
	}
	if q.tx != nil && q.exec != Executor(q.tx.tx) {
		return false
	}
	return q.batchIDsProbe(func() (bool, error) {
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()
		var schema any
		if q.schema != "" {
			schema = q.schema
		}
		var lockMode int64
		var version, engine, extra string
		var triggers int64
		err := q.client.queryRowStmt(ctx, q.exec, stmt{kind: StatementIntrospection, op: "QUERY_ROW", table: q.table},
			"SELECT @@GLOBAL.innodb_autoinc_lock_mode, @@version, "+
				"COALESCE((SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA = COALESCE(?, DATABASE()) AND TABLE_NAME = ?), ''), "+
				"COALESCE((SELECT EXTRA FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = COALESCE(?, DATABASE()) AND TABLE_NAME = ? AND COLUMN_NAME = ?), ''), "+
				"(SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE EVENT_OBJECT_SCHEMA = COALESCE(?, DATABASE()) AND EVENT_OBJECT_TABLE = ? AND EVENT_MANIPULATION = 'INSERT')",
			[]any{schema, q.table, schema, q.table, q.pk.Column, schema, q.table}).Scan(&lockMode, &version, &engine, &extra, &triggers)
		if err != nil {
			return false, err
		}
		v := strings.ToLower(version)
		ok := (lockMode == 0 || lockMode == 1) &&
			strings.EqualFold(engine, "InnoDB") &&
			strings.Contains(strings.ToLower(extra), "auto_increment") &&
			triggers == 0 &&
			!strings.Contains(v, "tidb") && !strings.Contains(v, "vitess")
		return ok, nil
	})
}

// createBatchMySQL sends each chunk of at least mysqlBatchMinRows rows as one
// multi-row INSERT and computes the keys; smaller chunks, and chunks the
// multi-row form could not complete, go row by row.
func (q *Query[T]) createBatchMySQL(entities []*T, columns []string, colIndexes []int, colTags []string) error {
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	rowsPerChunk := max(batchBindParamCeiling("mysql")/len(colIndexes), 1)
	for start := 0; start < len(entities); start += rowsPerChunk {
		chunk := entities[start:min(start+rowsPerChunk, len(entities))]
		if len(chunk) >= mysqlBatchMinRows {
			done, err := q.createBatchMySQLChunk(ctx, chunk, columns, colIndexes, colTags)
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		if err := q.backfillPerRow(ctx, chunk, columns, colIndexes, colTags); err != nil {
			return err
		}
	}
	return nil
}

// createBatchMySQLChunk inserts one chunk with one multi-row INSERT and sets
// each entity's key. It reports done=false, with no error and nothing left
// behind, when the chunk has to go row by row instead: the INSERT failed and
// was undone, or what came back cannot be mapped to the rows. The INSERT
// runs inside a transaction of its own — or inside a savepoint of the
// caller's transaction — precisely so that a failure is undone and the
// per-row form can run the chunk as if this attempt had never happened.
//
// It returns an error only when the outcome is not known (the COMMIT or the
// RELEASE failed) or when the caller's transaction is gone (a deadlock rolls
// back the whole transaction, and ROLLBACK TO SAVEPOINT then fails): running
// the rows again outside it would commit what the caller is about to see as
// rolled back.
func (q *Query[T]) createBatchMySQLChunk(ctx context.Context, chunk []*T, columns []string, colIndexes []int, colTags []string) (done bool, err error) {
	insertSQL, args := q.buildBatchInsert(chunk, columns, colIndexes, colTags, "")
	write := stmt{kind: StatementExec, op: "EXEC", table: q.table, write: true}
	read := stmt{kind: StatementIntrospection, op: "QUERY_ROW", table: q.table}

	var exec Executor
	var own *sql.Tx
	const savepoint = "quark_createbatch"
	if q.tx == nil {
		own, err = q.client.db.BeginTx(ctx, nil)
		if err != nil {
			return false, nil
		}
		exec = own
	} else {
		exec = q.tx.tx
		if _, err := q.client.execStmt(ctx, exec, stmt{kind: StatementSavepoint, op: "EXEC"}, "SAVEPOINT "+savepoint, nil); err != nil {
			return false, nil
		}
	}
	// undo puts the connection back where it was before the attempt, and
	// reports whether the chunk may now go row by row. In the caller's
	// transaction it may not when the savepoint is gone with the transaction:
	// then the error is the attempt's, or the rollback's when the attempt
	// failed without one.
	undo := func(cause error) (bool, error) {
		if own != nil {
			_ = own.Rollback()
			return false, nil
		}
		if _, err := q.client.execStmt(ctx, exec, stmt{kind: StatementSavepoint, op: "EXEC"}, "ROLLBACK TO SAVEPOINT "+savepoint, nil); err != nil {
			if cause == nil {
				cause = err
			}
			return false, cause
		}
		return false, nil
	}

	res, err := q.client.execStmt(ctx, exec, write, insertSQL, args)
	if err != nil {
		return undo(err)
	}
	firstID, err := res.LastInsertId()
	if err != nil {
		return undo(err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != int64(len(chunk)) || firstID <= 0 {
		return undo(err)
	}
	var step int64
	if err := q.client.queryRowStmt(ctx, exec, read, "SELECT @@SESSION.auto_increment_increment", nil).Scan(&step); err != nil || step < 1 {
		return undo(err)
	}

	if own != nil {
		if err := own.Commit(); err != nil {
			return false, wrapDBError(err)
		}
	} else if _, err := q.client.execStmt(ctx, exec, stmt{kind: StatementSavepoint, op: "EXEC"}, "RELEASE SAVEPOINT "+savepoint, nil); err != nil {
		return false, err
	}

	pks := make([]any, len(chunk))
	for k, entity := range chunk {
		id := firstID + int64(k)*step
		setPKValue(reflect.ValueOf(entity).Elem(), q.pk, id)
		pks[k] = id
	}
	// The INSERT dropped the table tag when it ran; after the commit, drop it
	// again with the row tags, so a read cached between the two cannot stay.
	q.invalidateBatchInsert(ctx, pks)
	return true, nil
}
