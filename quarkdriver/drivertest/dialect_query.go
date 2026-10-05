// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertest

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// The engine checks of the query half of the contract: what every read and
// write an application makes asks of the dialect.

func checkRegistration(t *testing.T, k *kit) {
	if k.c.DriverName == "" {
		t.Skip("DialectCase.DriverName is empty: an application passes this dialect with quark.WithDialect, so there is no resolution by name to check")
	}
	c, err := quark.NewWithDB(k.c.DriverName, k.db, quark.WithLogger(quiet))
	if err != nil {
		t.Fatalf("quark.NewWithDB(%q) with no WithDialect: %v — register the dialect with quarkdriver.RegisterDialect under the driver's name", k.c.DriverName, err)
	}
	defer c.Close()
	if got, want := c.Dialect().Name(), k.d.Name(); got != want {
		t.Errorf("quark.NewWithDB(%q) resolved the dialect %q, not %q: quarkdriver.RegisterDialect(%q, …) was not called, or another module replaced it", k.c.DriverName, got, want, k.c.DriverName)
	}
}

func seedRows(t *testing.T, k *kit, n int) []kitRow {
	t.Helper()
	k.fresh(t, &kitRow{})
	rows := make([]kitRow, n)
	for i := range rows {
		rows[i] = kitRow{Name: fmt.Sprintf("r%d", i+1), N: int64(i + 1)}
		if err := quark.For[kitRow](k.ctx, k.client).Create(&rows[i]); err != nil {
			t.Fatalf("setup: Create row %d: %v", i+1, err)
		}
	}
	return rows
}

func ns(rows []kitRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.N
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// checkPlaceholder binds many values in one statement, in WHERE, IN and SET,
// and asks for the rows only the right binding returns. A marker numbered
// wrong binds a value to the wrong comparison and the rows change.
func checkPlaceholder(t *testing.T, k *kit) {
	seedRows(t, k, 15)

	got, err := quark.For[kitRow](k.ctx, k.client).
		Where("n", ">", 3).Where("name", "=", "r7").Where("n", "<", 10).List()
	if err != nil {
		t.Fatalf("Dialect.Placeholder: three bound comparisons: %v", err)
	}
	if fmt.Sprint(ns(got)) != "[7]" {
		t.Errorf("Dialect.Placeholder: n > 3 AND name = 'r7' AND n < 10 returned n=%v, want [7]", ns(got))
	}

	// Eleven markers in an IN list, and one after it: the twelfth marker is
	// numbered past nine, where a marker built by concatenation goes wrong.
	in := []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	got, err = quark.For[kitRow](k.ctx, k.client).WhereIn("n", in).Where("name", "=", "r11").List()
	if err != nil {
		t.Fatalf("Dialect.Placeholder: twelve bound values: %v", err)
	}
	if fmt.Sprint(ns(got)) != "[11]" {
		t.Errorf("Dialect.Placeholder: n IN (1..11) AND name = 'r11' returned n=%v, want [11]", ns(got))
	}

	// SET and WHERE in one statement.
	if _, err := quark.For[kitRow](k.ctx, k.client).Where("n", "=", 5).UpdateMap(map[string]any{"name": "five", "n": 50}); err != nil {
		t.Fatalf("Dialect.Placeholder: UPDATE … SET … WHERE: %v", err)
	}
	got, err = quark.For[kitRow](k.ctx, k.client).Where("name", "=", "five").List()
	if err != nil || len(got) != 1 || got[0].N != 50 {
		t.Errorf("Dialect.Placeholder: after SET name='five', n=50 WHERE n=5 the row reads %+v (err %v)", got, err)
	}
}

// checkQuote writes and reads a table whose columns are words the engines
// reserve, and hands Quote identifiers that try to end the quoted name.
func checkQuote(t *testing.T, k *kit) {
	k.fresh(t, &kitQuote{})
	row := kitQuote{User: "u", Desc: "d", Check: 7, Key: "k", Level: 3}
	if err := quark.For[kitQuote](k.ctx, k.client).Create(&row); err != nil {
		t.Fatalf("Dialect.Quote: insert into columns named user, desc, check, key, level: %v", err)
	}
	got, err := quark.For[kitQuote](k.ctx, k.client).
		Where("desc", "=", "d").Where("check", "=", 7).OrderBy("level", "DESC").List()
	if err != nil {
		t.Fatalf("Dialect.Quote: select by columns named desc and check, ordered by level: %v", err)
	}
	if len(got) != 1 || got[0].User != "u" || got[0].Key != "k" || got[0].Level != 3 {
		t.Errorf("Dialect.Quote: read back %+v, want %+v", got, row)
	}
	if _, err := quark.For[kitQuote](k.ctx, k.client).Where("key", "=", "k").UpdateMap(map[string]any{"user": "v"}); err != nil {
		t.Errorf("Dialect.Quote: update a column named user where key = …: %v", err)
	}

	// An identifier that carries the engine's quote character must stay one
	// name: either the engine accepts it as a single column alias, or it
	// refuses the statement. A second column in the result means the
	// identifier ended the quoted name and wrote SQL of its own.
	for _, hostile := range []string{`kit", 2 AS "x`, "kit`, 2 AS `x", "kit], 2 AS [x"} {
		query := "SELECT " + k.q("id") + " AS " + k.q(hostile) + " FROM " + k.q(kitQuote{}.TableName())
		rows, err := k.db.QueryContext(k.ctx, query)
		if err != nil {
			continue // the engine refused it: nothing was injected
		}
		cols, _ := rows.Columns()
		_ = rows.Close()
		if len(cols) != 1 {
			t.Errorf("Dialect.Quote(%q) let the identifier end the quoted name: the statement %q returns %d columns %q", hostile, query, len(cols), cols)
		}
	}
}

// checkLimitOffset pages through five ordered rows.
func checkLimitOffset(t *testing.T, k *kit) {
	k.fresh(t, &kitLimit{})
	for i := 1; i <= 5; i++ {
		if err := quark.For[kitLimit](k.ctx, k.client).Create(&kitLimit{N: int64(i)}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	for _, c := range []struct {
		limit, offset int
		want          string
	}{
		{2, 0, "[1 2]"},
		{2, 1, "[2 3]"},
		{0, 3, "[4 5]"},
		{10, 4, "[5]"},
		{0, 5, "[]"},
		{1, 0, "[1]"},
	} {
		q := quark.For[kitLimit](k.ctx, k.client).OrderBy("n", "ASC")
		if c.limit > 0 {
			q = q.Limit(c.limit)
		}
		if c.offset > 0 {
			q = q.Offset(c.offset)
		}
		rows, err := q.List()
		if err != nil {
			t.Errorf("Dialect.LimitOffset(%d, %d): %v", c.limit, c.offset, err)
			continue
		}
		var got []int64
		for _, r := range rows {
			got = append(got, r.N)
		}
		if g := fmt.Sprint(got); g != c.want && !(c.want == "[]" && len(got) == 0) {
			t.Errorf("Dialect.LimitOffset(%d, %d) over n = 1..5 ordered ascending returned %s, want %s", c.limit, c.offset, g, c.want)
		}
	}
}

// checkReturning: the key the engine generated reaches the struct, by
// RETURNING or by the last-insert id, and it is the key of the row written.
func checkReturning(t *testing.T, k *kit) {
	how := "Dialect.SupportsReturning/Returning"
	if !k.d.SupportsReturning() {
		how = "Dialect.SupportsLastInsertID/LastInsertIDQuery"
	}
	k.fresh(t, &kitKeys{})
	seen := map[int64]bool{}
	for _, name := range []string{"a", "b", "c"} {
		row := kitKeys{Name: name}
		if err := quark.For[kitKeys](k.ctx, k.client).Create(&row); err != nil {
			t.Fatalf("%s: Create: %v", how, err)
		}
		if row.ID == 0 {
			t.Fatalf("%s: Create left the generated key at zero", how)
		}
		if seen[row.ID] {
			t.Errorf("%s: two inserts reported the same key %d", how, row.ID)
		}
		seen[row.ID] = true
		got, err := quark.For[kitKeys](k.ctx, k.client).Where("name", "=", name).First()
		if err != nil {
			t.Fatalf("read back %q: %v", name, err)
		}
		if got.ID != row.ID {
			t.Errorf("%s: Create reported key %d for %q, but the row's key is %d", how, row.ID, name, got.ID)
		}
	}

	batch := []*kitKeys{{Name: "x"}, {Name: "y"}}
	if err := quark.For[kitKeys](k.ctx, k.client).CreateBatch(batch); err != nil {
		t.Fatalf("%s: CreateBatch: %v", how, err)
	}
	for _, b := range batch {
		got, err := quark.For[kitKeys](k.ctx, k.client).Where("name", "=", b.Name).First()
		if err != nil {
			t.Fatalf("read back %q: %v", b.Name, err)
		}
		if b.ID != 0 && b.ID != got.ID {
			t.Errorf("%s: CreateBatch reported key %d for %q, but the row's key is %d", how, b.ID, b.Name, got.ID)
		}
	}
}

// checkCurrentTimestamp: a soft delete stamps deleted_at with
// Dialect.CurrentTimestamp(); the stamp must be a time near now. A day
// either side, because the engine's clock may be in another zone.
func checkCurrentTimestamp(t *testing.T, k *kit) {
	k.fresh(t, &kitTrash{})
	row := kitTrash{Name: "gone"}
	if err := quark.For[kitTrash](k.ctx, k.client).Create(&row); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := quark.For[kitTrash](k.ctx, k.client).Delete(&row); err != nil {
		t.Fatalf("Dialect.CurrentTimestamp: a soft delete (UPDATE … SET deleted_at = CurrentTimestamp()) failed: %v", err)
	}
	got, err := quark.For[kitTrash](k.ctx, k.client).OnlyTrashed().Where("name", "=", "gone").First()
	if err != nil {
		t.Fatalf("Dialect.CurrentTimestamp: the soft-deleted row is not in OnlyTrashed: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatal("Dialect.CurrentTimestamp: the soft delete left deleted_at NULL")
	}
	if d := time.Since(*got.DeletedAt); d > 24*time.Hour || d < -24*time.Hour {
		t.Errorf("Dialect.CurrentTimestamp: deleted_at is %v, %v away from now", got.DeletedAt, d)
	}
}

// checkJSONExtract: a nested value is found through WhereJSON, and a hostile
// path never reaches the engine.
func checkJSONExtract(t *testing.T, k *kit) {
	k.fresh(t, &kitDoc{})
	for _, doc := range []string{`{"user":{"name":"ana"},"n":"1"}`, `{"user":{"name":"bob"},"n":"2"}`} {
		if err := quark.For[kitDoc](k.ctx, k.client).Create(&kitDoc{Doc: doc}); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	got, err := quark.For[kitDoc](k.ctx, k.client).WhereJSON("doc", "user.name", "=", "bob").List()
	if err != nil {
		t.Fatalf("Dialect.JSONExtract: WhereJSON(\"doc\", \"user.name\", \"=\", \"bob\"): %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Doc, "bob") {
		t.Errorf("Dialect.JSONExtract: user.name = 'bob' matched %d rows %+v, want the one with bob", len(got), got)
	}
	for _, p := range hostileJSONPaths {
		if _, err := quark.For[kitDoc](k.ctx, k.client).WhereJSON("doc", p, "=", "x").List(); err == nil {
			t.Errorf("Dialect.JSONExtract: WhereJSON with the path %q ran", p)
		}
	}
}

// checkUpsertSQL: insert, update the conflicting row's chosen columns and
// nothing else, insert past a different key.
func checkUpsertSQL(t *testing.T, k *kit) {
	k.fresh(t, &kitUpsert{})
	read := func(code string) kitUpsert {
		t.Helper()
		got, err := quark.For[kitUpsert](k.ctx, k.client).Where("code", "=", code).List()
		if err != nil || len(got) != 1 {
			t.Fatalf("read %q: %d rows, %v", code, len(got), err)
		}
		return got[0]
	}

	first := kitUpsert{Code: "a", Name: "first", N: 1}
	if err := quark.For[kitUpsert](k.ctx, k.client).Upsert(&first, []string{"code"}, []string{"name", "n"}); err != nil {
		t.Fatalf("Dialect.UpsertSQL: upsert of a new key: %v", err)
	}
	if row := read("a"); first.ID != 0 && first.ID != row.ID {
		t.Errorf("Dialect.UpsertSQL: the upsert that inserted reported key %d, the row's key is %d", first.ID, row.ID)
	}

	if err := quark.For[kitUpsert](k.ctx, k.client).Upsert(&kitUpsert{Code: "a", Name: "second", N: 2}, []string{"code"}, []string{"name", "n"}); err != nil {
		t.Fatalf("Dialect.UpsertSQL: upsert on a conflicting key: %v", err)
	}
	if row := read("a"); row.Name != "second" || row.N != 2 {
		t.Errorf("Dialect.UpsertSQL: on conflict with updateCols [name n] the row is %+v, want name=second n=2", row)
	}

	if err := quark.For[kitUpsert](k.ctx, k.client).Upsert(&kitUpsert{Code: "a", Name: "third", N: 3}, []string{"code"}, []string{"name"}); err != nil {
		t.Fatalf("Dialect.UpsertSQL: upsert updating one column: %v", err)
	}
	if row := read("a"); row.Name != "third" || row.N != 2 {
		t.Errorf("Dialect.UpsertSQL: on conflict with updateCols [name] the row is %+v, want name=third and n left at 2", row)
	}

	if err := quark.For[kitUpsert](k.ctx, k.client).Upsert(&kitUpsert{Code: "b", Name: "other", N: 9}, []string{"code"}, []string{"name", "n"}); err != nil {
		t.Fatalf("Dialect.UpsertSQL: upsert of a second key: %v", err)
	}
	if n, err := quark.For[kitUpsert](k.ctx, k.client).Count(); err != nil || n != 2 {
		t.Errorf("Dialect.UpsertSQL: two keys upserted, the table has %d rows (%v)", n, err)
	}
}

// lockModes are the lock requests a query can make, in Quark's words.
var lockModes = []struct {
	name string
	opts quarkdriver.LockOptions
	set  func(*quark.Query[kitLock]) *quark.Query[kitLock]
}{
	{"ForUpdate", quarkdriver.LockOptions{Mode: quarkdriver.LockForUpdate}, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForUpdate() }},
	{"ForUpdate+SkipLocked", quarkdriver.LockOptions{Mode: quarkdriver.LockForUpdate, SkipLocked: true}, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForUpdate().SkipLocked() }},
	{"ForUpdate+NoWait", quarkdriver.LockOptions{Mode: quarkdriver.LockForUpdate, NoWait: true}, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForUpdate().NoWait() }},
	{"ForShare", quarkdriver.LockOptions{Mode: quarkdriver.LockForShare}, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForShare() }},
	{"ForShare+SkipLocked", quarkdriver.LockOptions{Mode: quarkdriver.LockForShare, SkipLocked: true}, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForShare().SkipLocked() }},
	{"ForShare+NoWait", quarkdriver.LockOptions{Mode: quarkdriver.LockForShare, NoWait: true}, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForShare().NoWait() }},
}

// checkLockSuffix: each lock is either refused with ErrUnsupportedFeature or
// accepted by the engine; and the locks it accepts hold — a second
// transaction skips the locked row, fails at once on it, or waits for it.
func checkLockSuffix(t *testing.T, k *kit) {
	k.fresh(t, &kitLock{})
	ids := make([]int64, 3)
	for i := range ids {
		row := kitLock{Name: fmt.Sprintf("l%d", i)}
		if err := quark.For[kitLock](k.ctx, k.client).Create(&row); err != nil {
			t.Fatalf("setup: %v", err)
		}
		ids[i] = row.ID
	}

	supported := map[string]bool{}
	for _, m := range lockModes {
		tx, err := k.client.BeginTx(k.ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		got, err := m.set(quark.ForTx[kitLock](k.ctx, tx).Where("id", "=", ids[0])).List()
		_ = tx.Rollback()
		switch {
		case isUnsupported(err):
			t.Logf("%s: refused with ErrUnsupportedFeature", m.name)
		case err != nil:
			t.Errorf("Dialect.LockSuffix(%+v) wrote a lock the engine rejects: %v — return ErrUnsupportedFeature for what the engine cannot do", m.opts, err)
		case len(got) != 1:
			t.Errorf("Dialect.LockSuffix(%+v): the locked read of one row returned %d rows", m.opts, len(got))
		default:
			supported[m.name] = true
		}
	}
	if !supported["ForUpdate"] {
		if supported["ForShare"] || supported["ForUpdate+SkipLocked"] || supported["ForUpdate+NoWait"] {
			t.Error("Dialect.LockSuffix refuses ForUpdate but accepts a lock built on it")
		}
		t.Skip("the dialect refuses row locks with ErrUnsupportedFeature; there is no lock to hold")
	}

	// One transaction holds the first row.
	holder, err := k.client.BeginTx(k.ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = holder.Rollback() }()
	if _, err := quark.ForTx[kitLock](k.ctx, holder).Where("id", "=", ids[0]).ForUpdate().List(); err != nil {
		t.Fatalf("lock the first row: %v", err)
	}

	other := func(ctx context.Context, f func(*quark.Query[kitLock]) *quark.Query[kitLock]) ([]kitLock, error) {
		tx, err := k.client.BeginTx(k.ctx, nil)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		return f(quark.ForTx[kitLock](ctx, tx)).List()
	}

	checked := false
	if supported["ForUpdate+SkipLocked"] {
		checked = true
		ctx, cancel := k.within(10 * time.Second)
		got, err := other(ctx, func(q *quark.Query[kitLock]) *quark.Query[kitLock] { return q.ForUpdate().SkipLocked() })
		cancel()
		if err != nil {
			t.Errorf("Dialect.LockSuffix(ForUpdate+SkipLocked) while another transaction holds a row: %v", err)
		} else {
			var gotIDs []int64
			for _, r := range got {
				gotIDs = append(gotIDs, r.ID)
			}
			sort.Slice(gotIDs, func(i, j int) bool { return gotIDs[i] < gotIDs[j] })
			if want := []int64{ids[1], ids[2]}; !reflect.DeepEqual(gotIDs, want) {
				t.Errorf("Dialect.LockSuffix(ForUpdate+SkipLocked) returned keys %v while %d is held, want %v: SKIP LOCKED must pass over the held row and only it", gotIDs, ids[0], want)
			}
		}
	}
	if supported["ForUpdate+NoWait"] {
		checked = true
		ctx, cancel := k.within(10 * time.Second)
		start := time.Now()
		got, err := other(ctx, func(q *quark.Query[kitLock]) *quark.Query[kitLock] {
			return q.Where("id", "=", ids[0]).ForUpdate().NoWait()
		})
		cancel()
		switch {
		case err == nil:
			t.Errorf("Dialect.LockSuffix(ForUpdate+NoWait) read the held row (%d rows): the lock was not taken, or NOWAIT was not written", len(got))
		case time.Since(start) > 8*time.Second:
			t.Errorf("Dialect.LockSuffix(ForUpdate+NoWait) waited %v for the held row before failing", time.Since(start))
		}
	}
	if !checked {
		ctx, cancel := k.within(2 * time.Second)
		got, err := other(ctx, func(q *quark.Query[kitLock]) *quark.Query[kitLock] {
			return q.Where("id", "=", ids[0]).ForUpdate()
		})
		cancel()
		if err == nil {
			t.Errorf("Dialect.LockSuffix(ForUpdate): a second transaction read the held row at once (%d rows): the first lock was not taken", len(got))
		}
	}
}

// checkSavepoints: what a rollback to a savepoint undoes, and what a
// released savepoint keeps. Through SavepointDialect when the dialect has
// one, the SQL standard's statements otherwise.
func checkSavepoints(t *testing.T, k *kit) {
	if _, ok := k.d.(quarkdriver.SavepointDialect); !ok {
		t.Log("the dialect does not implement quarkdriver.SavepointDialect: the SQL standard's SAVEPOINT, ROLLBACK TO SAVEPOINT and RELEASE SAVEPOINT are what is checked")
	}
	k.fresh(t, &kitSave{})
	create := func(tx *quark.Tx, name string) {
		t.Helper()
		if err := quark.ForTx[kitSave](k.ctx, tx).Create(&kitSave{Name: name}); err != nil {
			t.Fatalf("insert %q in the transaction: %v", name, err)
		}
	}
	sp, sp2 := "qk_kit_sp_"+k.run, "qk_kit_sp2_"+k.run
	tx, err := k.client.BeginTx(k.ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	create(tx, "before")
	if err := tx.Savepoint(sp); err != nil {
		t.Fatalf("SavepointDialect.SavepointStmt: %v", err)
	}
	create(tx, "undone")
	if err := tx.RollbackTo(sp); err != nil {
		t.Fatalf("SavepointDialect.RollbackToSavepointStmt: %v", err)
	}
	create(tx, "after")
	if err := tx.Savepoint(sp2); err != nil {
		t.Fatalf("SavepointDialect.SavepointStmt (second): %v", err)
	}
	create(tx, "released")
	if err := tx.ReleaseSavepoint(sp2); err != nil {
		t.Fatalf("SavepointDialect.ReleaseSavepointStmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rows, err := quark.For[kitSave](k.ctx, k.client).OrderBy("id", "ASC").List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "before,after,released" {
		t.Errorf("SavepointDialect: after rollback to the first savepoint and release of the second the table holds [%s], want [before,after,released]", got)
	}
}

// checkRoutines runs the case's function and procedure.
func checkRoutines(t *testing.T, k *kit) {
	if k.c.Routines == nil {
		t.Skip("DialectCase.Routines is nil: the kit cannot create a routine on an engine whose DDL it does not know; give it a function and a procedure to check BuildRoutineQuery and BuildProcedureCall")
	}
	type value struct {
		Value int64 `db:"value"`
	}
	if fn := k.c.Routines.Function; fn != "" {
		rows, err := quark.NewRoutine[value](k.ctx, k.client, fn, 3).List()
		if err != nil {
			t.Errorf("Dialect.BuildRoutineQuery(%q, 1): %v", fn, err)
		} else if len(rows) != 3 || rows[0].Value+rows[1].Value+rows[2].Value != 6 {
			t.Errorf("Dialect.BuildRoutineQuery(%q, 1) with n=3 returned %+v, want the values 1, 2, 3", fn, rows)
		}
	}
	if proc := k.c.Routines.Procedure; proc != "" {
		if err := quark.Call(k.ctx, k.client, proc, 1); err != nil {
			t.Errorf("Dialect.BuildProcedureCall(%q, 1): %v", proc, err)
		}
	}
}
