// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// QK-63 and QK-64, as the statements render, without an engine.
// internal/enginesuite (UpsertKey) runs them on the six engines.

type ukUnitCode struct {
	Code string `db:"code" pk:"true"`
	Name string `db:"name"`
}

func (ukUnitCode) TableName() string { return "uk_unit_codes" }

// QK-63: on MySQL and MariaDB the clause ends with the assignment that makes
// the statement report the key of the row its update branch met, confined
// to the tenant under RowLevelSecurityClient; nothing for a model without a
// single integer key or for an engine with another clause.
func TestUpsertDuplicateKeyKeyAssignment(t *testing.T) {
	const clause = " ON DUPLICATE KEY UPDATE `name` = VALUES(`name`)"
	for _, d := range []Dialect{MySQL(), MariaDB()} {
		t.Run(d.Name(), func(t *testing.T) {
			c, err := New("sqlite", "file:qk63_assign_"+d.Name()+"?mode=memory&cache=shared",
				WithDialect(d), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer c.Close()
			ctx := context.Background()

			got, args := For[utRow](ctx, c).duplicateKeyKeyAssignment(clause, 4)
			if want := ", `id` = LAST_INSERT_ID(`id`)"; got != want || len(args) != 0 {
				t.Errorf("unguarded: %q %v, want %q and no bind", got, args, want)
			}

			router, ta := wgRouter(c)
			got, args = For[utRow](ta, router).duplicateKeyKeyAssignment(clause, 4)
			if want := ", `id` = IF(`tenant_id` = ?, LAST_INSERT_ID(`id`), `id`)"; got != want || !reflect.DeepEqual(args, []any{"ta"}) {
				t.Errorf("guarded: %q %v, want %q and the tenant bound", got, args, want)
			}

			if got, _ := For[ukUnitCode](ctx, c).duplicateKeyKeyAssignment(clause, 4); got != "" {
				t.Errorf("a string key: %q, want none", got)
			}
			if got, _ := For[mergeEdgePair](ctx, c).duplicateKeyKeyAssignment(clause, 4); got != "" {
				t.Errorf("a composite key: %q, want none", got)
			}
			if got, _ := For[utRow](ctx, c).duplicateKeyKeyAssignment(" ON CONFLICT (`code`) DO NOTHING", 4); got != "" {
				t.Errorf("another clause: %q, want none", got)
			}
		})
	}

	c, err := New("sqlite", "file:qk63_assign_pg?mode=memory&cache=shared",
		WithDialect(PostgreSQL()), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()
	if got, _ := For[utRow](context.Background(), c).duplicateKeyKeyAssignment(clause, 4); got != "" {
		t.Errorf("postgres: %q, want none", got)
	}
}

type ukResult struct {
	id, n int64
	err   error
}

func (r ukResult) LastInsertId() (int64, error) { return r.id, r.err }
func (r ukResult) RowsAffected() (int64, error) { return r.n, nil }

// QK-63: which key the result of an INSERT … ON DUPLICATE KEY UPDATE that
// ends with the key assignment gives the entity. The values are the ones
// MySQL 8.0 and MariaDB 11.4 answer, measured.
func TestDuplicateKeyRowKey(t *testing.T) {
	for _, tc := range []struct {
		name         string
		res          ukResult
		carried      bool
		key          int64
		found, write bool
	}{
		{"inserted, the key generated", ukResult{id: 3, n: 1}, false, 3, true, true},
		{"inserted with the key the entity carried", ukResult{id: 424242, n: 1}, true, 424242, true, false},
		{"updated", ukResult{id: 1, n: 2}, false, 1, true, true},
		{"updated, the entity carried another key", ukResult{id: 1, n: 2}, true, 1, true, true},
		{"left as it was", ukResult{id: 1, n: 0}, false, 1, true, true},
		{"left as it was, the entity carried another key", ukResult{id: 1, n: 0}, true, 1, true, true},
		{"a row of another tenant: no key reported", ukResult{id: 0, n: 0}, false, 0, false, false},
		{"no insert id", ukResult{err: errors.New("no insert id")}, false, 0, false, false},
	} {
		key, found, write := duplicateKeyRowKey(tc.res, tc.carried)
		if found != tc.found || write != tc.write || (tc.write && key != tc.key) {
			t.Errorf("%s: key %d found %v write %v, want %d %v %v", tc.name, key, found, write, tc.key, tc.found, tc.write)
		}
	}
}

// QK-63, QK-64: the statement each engine sends reads the key back from
// itself. The SQL is recorded as sent; SQLite underneath does not run it.
func TestUpsertReadsTheKeyFromItsOwnStatement(t *testing.T) {
	for _, tc := range []struct {
		dialect Dialect
		want    []string
		batch   string
	}{
		{MySQL(), []string{"ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `id` = LAST_INSERT_ID(`id`)"}, ""},
		{MSSQL(), []string{"DECLARE @quark_keys TABLE (quark_key BIGINT NOT NULL);", "OUTPUT INSERTED.[id] INTO @quark_keys (quark_key);\nSELECT quark_key FROM @quark_keys;"},
			"OUTPUT src.quark_ord, INSERTED.[id] INTO @quark_keys (quark_ord, quark_key);"},
		{Oracle(), []string{`RETURNING "ID" BULK COLLECT INTO quark_keys;`, ":quark_n := quark_keys.COUNT;"},
			`RETURNING "ID" BULK COLLECT INTO quark_keys;`},
	} {
		t.Run(tc.dialect.Name(), func(t *testing.T) {
			rec := &txStatementRecorder{}
			c, err := New("sqlite", "file:qk63_stmt_"+tc.dialect.Name()+"?mode=memory&cache=shared",
				WithDialect(tc.dialect), WithQueryObserver(rec), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer c.Close()
			ctx := context.Background()
			for _, carried := range []int64{0, 424242} {
				rec.reset()
				_ = For[utRow](ctx, c).Upsert(&utRow{ID: carried, Code: "x", Name: "n"}, []string{"code"}, []string{"name"})
				for _, w := range tc.want {
					if !rec.any(w) {
						t.Errorf("Upsert with key %d sent no statement with %q; sent %q", carried, w, rec.all)
					}
				}
				if rec.any("SELECT LAST_INSERT_ID()") {
					t.Errorf("Upsert with key %d read the key with a second statement: %q", carried, rec.all)
				}
			}
			if tc.batch == "" {
				return
			}
			rec.reset()
			_ = For[utRow](ctx, c).UpsertBatch([]*utRow{{Code: "x", Name: "n"}, {Code: "y", Name: "n"}}, []string{"code"}, []string{"name"})
			if !rec.any(tc.batch) {
				t.Errorf("UpsertBatch sent no statement with %q; sent %q", tc.batch, rec.all)
			}
			if tc.dialect.Name() == "mssql" && !rec.any("USING (VALUES (0, @p1, @p2, @p3), (1, @p4, @p5, @p6)) AS src (quark_ord, ") {
				t.Errorf("UpsertBatch does not number its source rows: %q", rec.all)
			}
		})
	}

	// A model whose key is not a single integer is upserted as before.
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:qk63_stmt_string?mode=memory&cache=shared",
		WithDialect(MSSQL()), WithQueryObserver(rec), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()
	_ = For[ukUnitCode](context.Background(), c).Upsert(&ukUnitCode{Code: "x", Name: "n"}, []string{"code"}, []string{"name"})
	if rec.any("OUTPUT") || !rec.any("MERGE INTO") {
		t.Errorf("a string key: %q, want the MERGE without OUTPUT", rec.all)
	}
	if strings.Contains(strings.Join(rec.all, "\n"), "quark_keys") {
		t.Errorf("a string key reads keys back: %q", rec.all)
	}
}
