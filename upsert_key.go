// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// The key an upsert writes into the entity (QK-63, QK-64).
//
// When Upsert returns nil, the entity's key is the key of the row the
// statement inserted or updated, read from that same statement; where the
// engine does not say which row that was, the key is left as it was. It is
// never the key of another row.
//
//   - PostgreSQL, SQLite, MariaDB: RETURNING, as before.
//   - MySQL: the key comes from the INSERT's own result, sql.Result's
//     LastInsertId. It used to come from SELECT LAST_INSERT_ID(), a second
//     statement on whichever connection the pool handed out (QK-63).
//     LAST_INSERT_ID() belongs to a connection, and on the update branch it
//     is that connection's last generated key, not the conflicting row's:
//     an upsert that met row 1 set the entity's key to 2. The result reports
//     the conflicting row's key only when the update branch says so, so the
//     clause ends with the documented idiom `<pk> = LAST_INSERT_ID(<pk>)`,
//     which leaves the key as it is and makes it the statement's insert id.
//     It changes no value, so the rows affected keep their meaning: 1
//     inserted, 2 updated, 0 left as it was.
//   - SQL Server: MERGE … OUTPUT INSERTED.<pk> INTO a table variable, read
//     back by a SELECT in the same batch; an OUTPUT without INTO is refused
//     on a table with a trigger (Msg 334).
//   - Oracle: MERGE … RETURNING <pk> BULK COLLECT INTO, in a PL/SQL block.
//     The MERGE quark writes needs Oracle 23ai, which takes the clause.
//
// On SQL Server and Oracle the key is an identity column, which the MERGE's
// insert branch does not write: an entity that carried a non-zero key got a
// row with the key the engine assigned and kept its own (QK-64). The key is
// read back whatever the entity carried now.
//
// All of it applies to a single integer key, the identity or AUTO_INCREMENT
// column quark migrates. Other keys are written as the entity carries them
// and not read back, as before.

// upsertIntegerKey reports whether the model's key is one integer column,
// the key an upsert reads back from its statement.
func (q *BaseQuery) upsertIntegerKey() bool {
	return q.pk.Column != "" && (q.meta == nil || !q.meta.HasCompositePK) && isIntegerKind(q.pk.Kind)
}

const onDuplicateKeyUpdate = " ON DUPLICATE KEY UPDATE "

// duplicateKeyKeyAssignment returns the assignment that makes an INSERT …
// ON DUPLICATE KEY UPDATE report the key of the row its update branch met,
// to be appended after clause, and its bind. It is the last assignment, so
// it sees the key an updateCols that names the key column wrote. Under
// RowLevelSecurityClient it reports the key of a row of the resolved tenant
// only: for a row of another tenant the statement reports no key, which is
// how that row is told apart from the tenant's own. argIndex is the
// placeholder index of the bind. "" when clause is not an ON DUPLICATE KEY
// UPDATE clause or the model has no single integer key.
func (q *BaseQuery) duplicateKeyKeyAssignment(clause string, argIndex int) (string, []any) {
	if q.upsertFamily() != "duplicate_key" || !q.upsertIntegerKey() || !strings.HasPrefix(clause, onDuplicateKeyUpdate) {
		return "", nil
	}
	pk := q.dialect.Quote(q.pk.Column)
	if q.tenantGuarded() {
		return fmt.Sprintf(", %s = IF(%s = %s, LAST_INSERT_ID(%s), %s)",
			pk, q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(argIndex), pk, pk), []any{q.tenantID}
	}
	return fmt.Sprintf(", %s = LAST_INSERT_ID(%s)", pk, pk), nil
}

// duplicateKeyRowKey reads the key of the row an INSERT … ON DUPLICATE KEY
// UPDATE that ends with duplicateKeyKeyAssignment inserted or met. found is
// false when the statement reports no key: under RowLevelSecurityClient,
// the row it met belongs to another tenant. carried says whether the entity
// carried a key; an insert then wrote that key, so there is nothing to read
// (write is false) — the insert id would name the table's AUTO_INCREMENT
// column, which need not be the key the entity carried.
func duplicateKeyRowKey(res sql.Result, carried bool) (key int64, found, write bool) {
	key, err := res.LastInsertId()
	if err != nil || key <= 0 {
		return 0, false, false
	}
	if n, _ := res.RowsAffected(); carried && n == 1 {
		return key, true, false
	}
	return key, true, true
}

// mergeRowKeys runs a MERGE built by buildMerge and reads back the keys of
// the rows it inserted or updated: n is how many, key the one key when n is
// 1. n is 0 when the MERGE wrote no row — it met a row and has no update
// branch, or, under RowLevelSecurityClient, met a row of another tenant —
// and more than 1 when the conflict columns matched several rows; the key
// is then not known.
func (q *BaseQuery) mergeRowKeys(ctx context.Context, mergeSQL string, args []any) (n, key int64, err error) {
	merge := strings.TrimSuffix(mergeSQL, ";")
	pk := q.dialect.Quote(q.pk.Column)
	if q.dialect.Name() == "oracle" {
		// RETURNING INTO one variable fails when the MERGE writes more than
		// one row; a collection takes any number, and the block hands back
		// the count and the key when there is exactly one.
		block := "DECLARE\nTYPE quark_key_list IS TABLE OF NUMBER;\nquark_keys quark_key_list;\nquark_one NUMBER := 0;\nBEGIN\n" +
			merge + "\nRETURNING " + pk + " BULK COLLECT INTO quark_keys;\n" +
			"IF quark_keys.COUNT = 1 THEN quark_one := quark_keys(1); END IF;\n" +
			":quark_n := quark_keys.COUNT;\n:quark_key := quark_one;\nEND;"
		// A copy, so the caller's slice is not appended to.
		binds := append(append([]any(nil), args...),
			sql.Named("quark_n", sql.Out{Dest: &n}), sql.Named("quark_key", sql.Out{Dest: &key}))
		if _, err := q.executeExec(ctx, block, binds); err != nil {
			return 0, 0, err
		}
		if n == 1 {
			q.invalidateInsert(ctx, key)
		}
		return n, key, nil
	}

	// SQL Server. OUTPUT … INTO a table variable, not to the client: an
	// OUTPUT without INTO is refused on a table with an enabled trigger
	// (Msg 334). A MERGE that fails raises its error before the SELECT's
	// rows, so the query returns it.
	var b strings.Builder
	b.WriteString("SET NOCOUNT ON;\nDECLARE @quark_keys TABLE (quark_key BIGINT NOT NULL);\n")
	b.WriteString(merge)
	b.WriteString("\nOUTPUT INSERTED.")
	b.WriteString(pk)
	b.WriteString(" INTO @quark_keys (quark_key);\nSELECT quark_key FROM @quark_keys;")
	// A write that reads rows back: on the primary, never a replica. The
	// seam drops nothing from the cache for it; invalidateInsert does.
	rows, err := q.executeQueryPrimary(ctx, b.String(), args)
	if err != nil {
		return 0, 0, wrapDBError(err)
	}
	for rows.Next() {
		if err := rows.Scan(&key); err != nil {
			_ = rows.Close()
			return 0, 0, wrapDBError(err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, 0, wrapDBError(err)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, wrapDBError(err)
	}
	switch {
	case n == 1:
		q.invalidateInsert(ctx, key)
	case n > 1:
		q.invalidateInsert(ctx, nil)
		key = 0
	}
	return n, key, nil
}
