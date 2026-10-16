// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// QK-24. GroupBy without Select or SelectExpr leaves the projection at
// SELECT *, which only SQLite and MySQL in its permissive mode accept: the
// non-aggregated columns are not functionally dependent on the grouping key.
// Since quark#392 buildSelect says so in a WARN that names the fix and runs
// the query anyway. Quark 2.0 refuses the query with ErrInvalidQuery
// (DEP-2026-008); this test pins the v1 warning so the change at the major is
// a diff to it.
func TestGroupByWithoutSelectWarnsAndRunsInV1(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	c, err := New("sqlite", "file:qk24_groupby?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
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
	const warning = "GROUP BY without Select() or SelectExpr() emits SELECT *"

	// The permissive engine runs the SELECT * … GROUP BY, and the builder
	// warns once, naming the table and the grouped columns.
	logs.Reset()
	rows, err := For[ubvAccount](ctx, c).GroupBy("owner").List()
	if err != nil {
		t.Fatalf("GroupBy without Select on SQLite = %v, want nil (v1 behaviour: a warning, not an error)", err)
	}
	if len(rows) != 2 {
		t.Fatalf("GroupBy without Select answered %d rows, want 2 groups", len(rows))
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, warning) ||
		!strings.Contains(out, "table=ubv_accounts") || !strings.Contains(out, "group_by=owner") {
		t.Fatalf("GroupBy without Select did not log the v1 warning naming the fix; log:\n%s", out)
	}

	// With the projection named, the query is right and nothing is logged.
	logs.Reset()
	if _, err := For[ubvAccount](ctx, c).Select("owner").GroupBy("owner").List(); err != nil {
		t.Fatalf("GroupBy with Select = %v, want nil", err)
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
}
