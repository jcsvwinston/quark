// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-70. CreateBatch and UpsertBatch looked at the FIRST entity to decide
// whether the key column went into the INSERT, and applied that decision to
// every row: a batch whose first entity carried no key silently dropped the
// key a later entity carried, and a batch whose first entity carried one
// inserted id=0 for an entity that carried none. The key column is now
// decided per run of consecutive entities that agree, in the batch's order:
// a run without keys leaves the column out so the engine assigns them, a
// run with keys writes them. A batch whose entities all agree is one run
// and the one statement it always was.

type keyRunRow struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code" quark:"unique"`
	Name string `db:"name"`
}

func (keyRunRow) TableName() string { return "qk70_rows" }

func keyRunClient(t *testing.T, name string) (*Client, *txStatementRecorder) {
	t.Helper()
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), WithQueryObserver(rec))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(context.Background(), &keyRunRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rec.reset()
	return c, rec
}

// keyRunInserts returns the INSERT statements into the batch table recorded
// since the last reset, in order.
func (r *txStatementRecorder) keyRunInserts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.all {
		if strings.HasPrefix(s, `INSERT INTO "qk70_rows"`) {
			out = append(out, s)
		}
	}
	return out
}

// keyRunIDsByCode reads every row as code → id.
func keyRunIDsByCode(t *testing.T, c *Client) map[string]int64 {
	t.Helper()
	rows, err := For[keyRunRow](context.Background(), c).OrderBy("id", "ASC").List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := make(map[string]int64, len(rows))
	for _, r := range rows {
		got[r.Code] = r.ID
	}
	return got
}

func keyRunWantIDs(t *testing.T, what string, got, want map[string]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: rows %v, want %v", what, got, want)
	}
	for code, id := range want {
		if got[code] != id {
			t.Errorf("%s: row %q has id %d, want %d (all: %v)", what, code, got[code], id, got)
		}
	}
}

const (
	keyRunWithoutKey = `INSERT INTO "qk70_rows" ("code", "name") VALUES`
	keyRunWithKey    = `INSERT INTO "qk70_rows" ("id", "code", "name") VALUES`
)

func keyRunWantStatements(t *testing.T, got []string, prefixes ...string) {
	t.Helper()
	if len(got) != len(prefixes) {
		t.Fatalf("%d INSERT statements, want %d:\n%s", len(got), len(prefixes), strings.Join(got, "\n"))
	}
	for i, p := range prefixes {
		if !strings.HasPrefix(got[i], p) {
			t.Errorf("statement %d = %q, want it to start with %q", i, got[i], p)
		}
	}
}

func TestCreateBatchDecidesTheKeyColumnPerRun(t *testing.T) {
	ctx := context.Background()

	t.Run("a key after an entity without one is written", func(t *testing.T) {
		c, rec := keyRunClient(t, "qk70_create_later_key")
		batch := []*keyRunRow{{Code: "a"}, {ID: 10, Code: "b"}, {Code: "c"}}
		if err := For[keyRunRow](ctx, c).CreateBatch(batch); err != nil {
			t.Fatalf("CreateBatch: %v", err)
		}
		// Three runs, in the batch's order: the engine assigns 1 to a, b
		// writes 10, and c gets the next key after it.
		keyRunWantStatements(t, rec.keyRunInserts(), keyRunWithoutKey, keyRunWithKey, keyRunWithoutKey)
		keyRunWantIDs(t, "rows", keyRunIDsByCode(t, c), map[string]int64{"a": 1, "b": 10, "c": 11})
		for i, want := range []int64{1, 10, 11} {
			if batch[i].ID != want {
				t.Errorf("entity %d has id %d, want %d", i, batch[i].ID, want)
			}
		}
	})

	t.Run("an entity without a key after one with it gets an assigned key", func(t *testing.T) {
		c, rec := keyRunClient(t, "qk70_create_later_nokey")
		batch := []*keyRunRow{{ID: 5, Code: "a"}, {Code: "b"}, {Code: "c"}}
		if err := For[keyRunRow](ctx, c).CreateBatch(batch); err != nil {
			t.Fatalf("CreateBatch: %v", err)
		}
		keyRunWantStatements(t, rec.keyRunInserts(), keyRunWithKey, keyRunWithoutKey)
		// Not id=0: b and c take the keys after the one a wrote.
		keyRunWantIDs(t, "rows", keyRunIDsByCode(t, c), map[string]int64{"a": 5, "b": 6, "c": 7})
		if batch[1].ID != 6 || batch[2].ID != 7 {
			t.Errorf("entities got ids %d, %d, want 6, 7", batch[1].ID, batch[2].ID)
		}
	})

	t.Run("a batch that agrees is still one statement", func(t *testing.T) {
		c, rec := keyRunClient(t, "qk70_create_agree")
		if err := For[keyRunRow](ctx, c).CreateBatch([]*keyRunRow{{Code: "a"}, {Code: "b"}}); err != nil {
			t.Fatalf("CreateBatch without keys: %v", err)
		}
		keyRunWantStatements(t, rec.keyRunInserts(), keyRunWithoutKey)
		rec.reset()
		if err := For[keyRunRow](ctx, c).CreateBatch([]*keyRunRow{{ID: 20, Code: "c"}, {ID: 21, Code: "d"}}); err != nil {
			t.Fatalf("CreateBatch with keys: %v", err)
		}
		keyRunWantStatements(t, rec.keyRunInserts(), keyRunWithKey)
		keyRunWantIDs(t, "rows", keyRunIDsByCode(t, c), map[string]int64{"a": 1, "b": 2, "c": 20, "d": 21})
	})
}

func TestUpsertBatchDecidesTheKeyColumnPerRun(t *testing.T) {
	ctx := context.Background()
	c, rec := keyRunClient(t, "qk70_upsert")
	conflict, update := []string{"code"}, []string{"name"}

	// Insert pass: four runs, each with the columns its entities need.
	batch := []*keyRunRow{{Code: "a", Name: "v1"}, {ID: 10, Code: "b", Name: "v1"}, {Code: "c", Name: "v1"}, {ID: 20, Code: "d", Name: "v1"}, {ID: 21, Code: "e", Name: "v1"}}
	if err := For[keyRunRow](ctx, c).UpsertBatch(batch, conflict, update); err != nil {
		t.Fatalf("UpsertBatch insert pass: %v", err)
	}
	keyRunWantStatements(t, rec.keyRunInserts(), keyRunWithoutKey, keyRunWithKey, keyRunWithoutKey, keyRunWithKey)
	keyRunWantIDs(t, "insert pass", keyRunIDsByCode(t, c), map[string]int64{"a": 1, "b": 10, "c": 11, "d": 20, "e": 21})

	// Update pass over the same codes, mixed the other way round: every
	// row takes the update branch, none is duplicated, and the keys stay.
	rec.reset()
	again := []*keyRunRow{{ID: 10, Code: "b", Name: "v2"}, {Code: "a", Name: "v2"}, {Code: "c", Name: "v2"}, {ID: 20, Code: "d", Name: "v2"}}
	if err := For[keyRunRow](ctx, c).UpsertBatch(again, conflict, update); err != nil {
		t.Fatalf("UpsertBatch update pass: %v", err)
	}
	keyRunWantStatements(t, rec.keyRunInserts(), keyRunWithKey, keyRunWithoutKey, keyRunWithKey)
	keyRunWantIDs(t, "update pass", keyRunIDsByCode(t, c), map[string]int64{"a": 1, "b": 10, "c": 11, "d": 20, "e": 21})
	rows, err := For[keyRunRow](ctx, c).Where("name", "=", "v2").Count()
	if err != nil {
		t.Fatal(err)
	}
	if rows != 4 {
		t.Errorf("%d rows updated, want 4", rows)
	}
}

// batchKeyRuns on its own: the runs are consecutive, in order, and a model
// with a composite key is one run that writes every column.
func TestBatchKeyRunsSplitOnTheKey(t *testing.T) {
	c, _ := keyRunClient(t, "qk70_runs")
	q := For[keyRunRow](context.Background(), c)
	batch := []*keyRunRow{{}, {}, {ID: 1}, {}, {ID: 2}, {ID: 3}}
	runs := q.batchKeyRuns(batch)
	want := []struct {
		n       int
		withKey bool
	}{{2, false}, {1, true}, {1, false}, {2, true}}
	if len(runs) != len(want) {
		t.Fatalf("%d runs, want %d", len(runs), len(want))
	}
	next := 0
	for i, r := range runs {
		if len(r.entities) != want[i].n || r.withKey != want[i].withKey {
			t.Errorf("run %d: %d entities withKey=%v, want %d withKey=%v", i, len(r.entities), r.withKey, want[i].n, want[i].withKey)
		}
		if len(r.entities) > 0 && r.entities[0] != batch[next] {
			t.Errorf("run %d does not start at entity %d", i, next)
		}
		next += len(r.entities)
	}

	composite := For[mergeEdgePair](context.Background(), c).batchKeyRuns([]*mergeEdgePair{{}, {}})
	if len(composite) != 1 || !composite[0].withKey || len(composite[0].entities) != 2 {
		t.Errorf("composite key: runs %+v, want one run of two that writes the key", composite)
	}
}
