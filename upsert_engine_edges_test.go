// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

// QK-61 and QK-62, as the statements render, without an engine.
// internal/enginesuite (UpsertEngineEdges) runs them on the six engines.

// QK-61: without updateCols the MySQL and MariaDB clause assigns the first
// conflict column to itself. `= VALUES(<col>)` wrote the incoming value when
// the duplicate was on another unique key.
func TestUpsertDuplicateKeyNoOpAssignsTheColumnToItself(t *testing.T) {
	for _, d := range []Dialect{MySQL(), MariaDB()} {
		for _, update := range [][]string{nil, {}} {
			if got, want := d.UpsertSQL([]string{"code", "n"}, update, 1), " ON DUPLICATE KEY UPDATE `code` = `code`"; got != want {
				t.Errorf("%s UpsertSQL with updateCols %#v = %q, want %q", d.Name(), update, got, want)
			}
		}
	}

	// The tenant-guarded clause: the same no-op, with nothing to bind.
	for _, d := range []Dialect{MySQL(), MariaDB()} {
		c, err := New("sqlite", "file:qk61_guarded_"+d.Name()+"?mode=memory&cache=shared",
			WithDialect(d), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		router, ta := wgRouter(c)
		clause, args, hasUpdate, err := For[utRow](ta, router).guardedConflictClause([]string{"code"}, nil, 4)
		if err != nil || hasUpdate || len(args) != 0 {
			t.Errorf("%s guardedConflictClause without updateCols: %v, update branch %v, args %v; want no update branch and no binds", d.Name(), err, hasUpdate, args)
		}
		if want := " ON DUPLICATE KEY UPDATE `code` = `code`"; clause != want {
			t.Errorf("%s guardedConflictClause without updateCols = %q, want %q", d.Name(), clause, want)
		}
		_ = c.Close()
	}
}

type mergeEdgeRow struct {
	ID        int64     `db:"id" pk:"true"`
	Code      string    `db:"code" quark:"unique"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (mergeEdgeRow) TableName() string { return "merge_edge_rows" }

type mergeEdgePair struct {
	A    int64  `db:"a" pk:"true"`
	B    int64  `db:"b" pk:"true"`
	Code string `db:"code" quark:"unique"`
	Name string `db:"name"`
}

func (mergeEdgePair) TableName() string { return "merge_edge_pairs" }

// mergeSets returns the columns the WHEN MATCHED branch of stmt assigns.
func mergeSets(t *testing.T, d Dialect, stmt string) []string {
	t.Helper()
	const marker = "WHEN MATCHED THEN UPDATE SET "
	i := strings.Index(stmt, marker)
	if i < 0 {
		return nil
	}
	line := stmt[i+len(marker):]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	var cols []string
	for _, part := range strings.Split(line, ", ") {
		lhs := strings.TrimPrefix(strings.SplitN(part, " = ", 2)[0], "target.")
		cols = append(cols, strings.ToLower(strings.Trim(lhs, `[]"`)))
	}
	return cols
}

// QK-62: without updateCols the MERGE's update set leaves out the conflict
// columns, the primary key and created_at; updated_at stays. An explicit
// updateCols is written as given.
func TestMergeInferredUpdateSetLeavesTheKeyAndCreatedAt(t *testing.T) {
	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, d := range []Dialect{MSSQL(), Oracle()} {
		t.Run(d.Name(), func(t *testing.T) {
			c, err := New("sqlite", "file:qk62_merge_"+d.Name()+"?mode=memory&cache=shared",
				WithDialect(d), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer c.Close()
			ctx := context.Background()

			for _, id := range []int64{0, 7} {
				e := mergeEdgeRow{ID: id, Code: "a", Name: "n", CreatedAt: stamp, UpdatedAt: stamp}
				stmt, _, hasUpdate, err := For[mergeEdgeRow](ctx, c).buildMerge(reflect.ValueOf(&e).Elem(), []string{"code"}, nil)
				if err != nil || !hasUpdate {
					t.Fatalf("buildMerge with key %d: %v (update branch %v)", id, err, hasUpdate)
				}
				if got, want := mergeSets(t, d, stmt), []string{"name", "updated_at"}; !reflect.DeepEqual(got, want) {
					t.Errorf("key %d: the inferred update set is %v, want %v\n%s", id, got, want, stmt)
				}
			}

			e := mergeEdgeRow{ID: 7, Code: "a", Name: "n", CreatedAt: stamp, UpdatedAt: stamp}
			stmt, _, _, err := For[mergeEdgeRow](ctx, c).buildMerge(reflect.ValueOf(&e).Elem(), []string{"code"}, []string{"name", "created_at"})
			if err != nil {
				t.Fatal(err)
			}
			if got, want := mergeSets(t, d, stmt), []string{"name", "created_at"}; !reflect.DeepEqual(got, want) {
				t.Errorf("an explicit updateCols is written as given: got %v, want %v", got, want)
			}

			p := mergeEdgePair{A: 1, B: 2, Code: "a", Name: "n"}
			stmt, _, _, err = For[mergeEdgePair](ctx, c).buildMerge(reflect.ValueOf(&p).Elem(), []string{"code"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := mergeSets(t, d, stmt), []string{"name"}; !reflect.DeepEqual(got, want) {
				t.Errorf("a composite key: the inferred update set is %v, want %v\n%s", got, want, stmt)
			}
		})
	}
}
