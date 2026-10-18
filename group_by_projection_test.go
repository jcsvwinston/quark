// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// QK-24. GroupBy without Select or SelectExpr would leave the projection at
// SELECT *, which only SQLite and MySQL in its permissive mode accept: the
// non-aggregated columns are not functionally dependent on the grouping key.
// The builder refuses the query with ErrInvalidQuery before any engine sees
// it, SQLite included, and logs nothing: the fix is named in the error. With
// the projection named the query runs with nothing logged.
func TestGroupByWithoutSelectIsRefused(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:qk24_groupby?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
		WithQueryObserver(rec))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(ctx, &ubvAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i, owner := range []string{"alice", "alice", "bob"} {
		id := int64(i + 1)
		if err := For[ubvAccount](ctx, c).Create(&ubvAccount{ID: id, Owner: owner, Balance: id * 10, Version: 1}); err != nil {
			t.Fatalf("seed %d: %v", id, err)
		}
	}
	const warning = "GROUP BY without Select() or SelectExpr()"

	// The unprojected query is refused on every path, before the engine,
	// with an error that names the table and the grouped columns; the
	// permissive engine never gets to answer an arbitrary row per group.
	logs.Reset()
	for name, run := range map[string]func() error{
		"List": func() error {
			_, err := For[ubvAccount](ctx, c).GroupBy("owner").List()
			return err
		},
		"First": func() error {
			_, err := For[ubvAccount](ctx, c).GroupBy("owner").First()
			return err
		},
		"Cursor": func() error {
			cur, err := For[ubvAccount](ctx, c).GroupBy("owner").Cursor()
			if cur != nil {
				_ = cur.Close()
			}
			return err
		},
		"Paginate": func() error {
			_, err := For[ubvAccount](ctx, c).GroupBy("owner").OrderBy("owner", "ASC").Paginate(1, 10)
			return err
		},
	} {
		rec.reset()
		err := run()
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("%s: GroupBy without Select = %v, want ErrInvalidQuery", name, err)
		}
		if !strings.Contains(err.Error(), "table=ubv_accounts") || !strings.Contains(err.Error(), "group_by=owner") {
			t.Fatalf("%s: the error does not name the table and the grouped columns: %v", name, err)
		}
		// Paginate counts first, with its own projection; the grouped
		// statement itself never reaches the engine.
		if rec.any("GROUP BY") {
			t.Fatalf("%s: the unprojected statement reached the engine: %v", name, rec.all)
		}
	}
	if out := logs.String(); strings.Contains(out, warning) {
		t.Fatalf("GroupBy without Select logged a warning; the refusal is the error, not a log line:\n%s", out)
	}

	// With the projection named, the query is right and nothing is logged.
	logs.Reset()
	rows, err := For[ubvAccount](ctx, c).Select("owner").GroupBy("owner").List()
	if err != nil {
		t.Fatalf("GroupBy with Select = %v, want nil", err)
	}
	if len(rows) != 2 {
		t.Fatalf("GroupBy with Select answered %d rows, want 2 groups", len(rows))
	}
	if strings.Contains(logs.String(), warning) {
		t.Fatalf("GroupBy with Select logged the warning:\n%s", logs.String())
	}
	logs.Reset()
	if _, err := For[ubvAccount](ctx, c).GroupBy("owner").SelectExpr("owner", Col("owner")).List(); err != nil {
		t.Fatalf("GroupBy with SelectExpr = %v, want nil", err)
	}
	if strings.Contains(logs.String(), warning) {
		t.Fatalf("GroupBy with SelectExpr logged the warning:\n%s", logs.String())
	}

	// Count renders its own projection: a GroupBy with no Select on a Count
	// is not the unprojected statement and is not refused.
	if _, err := For[ubvAccount](ctx, c).GroupBy("owner").Count(); err != nil {
		t.Fatalf("GroupBy on Count = %v, want nil", err)
	}
}
