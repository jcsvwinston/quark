// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-39. The write paths rendered the caller's conditions themselves, as
// `col OP ?` joined by AND: WhereNot lost its NOT, so
// WhereNot("status", "=", "active").DeleteBy() deleted the active rows — the
// ones the caller excluded — and UpdateMap and UpdateFields wrote the rows
// they were told to leave alone. Or groups, IN, BETWEEN, IS NULL and WhereExpr
// failed there. The tests below hold every write path to one property: the
// rows a write touches are the rows a SELECT with the same conditions
// returns, and those are the rows a Go predicate over the fixture names —
// the predicate is what catches a renderer that is wrong for reads and
// writes alike. internal/enginesuite (WriteWhereParity) runs the same
// catalogue on the six engines.

type wwRow struct {
	ID     int64   `db:"id" pk:"true"`
	Name   string  `db:"name"`
	Status string  `db:"status"`
	Rank   int64   `db:"rank"`
	Note   *string `db:"note"`
	Hits   int64   `db:"hits"`
}

func (wwRow) TableName() string { return "ww_rows" }

func wwNote(s string) *string { return &s }

// wwFixture is the same six rows every case starts from. A name carries a
// literal "%", so the escaped LIKE has a row to tell apart from a wildcard.
func wwFixture() []wwRow {
	return []wwRow{
		{ID: 1, Name: "alpha", Status: "active", Rank: 1},
		{ID: 2, Name: "beta", Status: "active", Rank: 2, Note: wwNote("x")},
		{ID: 3, Name: "gamma", Status: "archived", Rank: 3},
		{ID: 4, Name: "100% off", Status: "archived", Rank: 4, Note: wwNote("y")},
		{ID: 5, Name: "delta", Status: "pending", Rank: 5},
		{ID: 6, Name: "epsilon", Status: "active", Rank: 6, Note: wwNote("z")},
	}
}

type wwCase struct {
	name  string
	apply func(*Query[wwRow]) *Query[wwRow]
	want  func(wwRow) bool
}

func wwCases() []wwCase {
	return []wwCase{
		{"Where =",
			func(q *Query[wwRow]) *Query[wwRow] { return q.Where("status", "=", "active") },
			func(r wwRow) bool { return r.Status == "active" }},
		{"WhereNot =",
			func(q *Query[wwRow]) *Query[wwRow] { return q.WhereNot("status", "=", "active") },
			func(r wwRow) bool { return r.Status != "active" }},
		{"WhereNot after Where",
			func(q *Query[wwRow]) *Query[wwRow] {
				return q.Where("rank", ">", 1).WhereNot("status", "=", "archived")
			},
			func(r wwRow) bool { return r.Rank > 1 && r.Status != "archived" }},
		{"Or group",
			func(q *Query[wwRow]) *Query[wwRow] {
				return q.Where("rank", ">", 4).Or(func(g *Query[wwRow]) *Query[wwRow] { return g.Where("name", "=", "alpha") })
			},
			func(r wwRow) bool { return r.Rank > 4 || r.Name == "alpha" }},
		{"WhereNot then an Or group of two",
			func(q *Query[wwRow]) *Query[wwRow] {
				return q.WhereNot("status", "=", "active").Or(func(g *Query[wwRow]) *Query[wwRow] {
					return g.Where("status", "=", "active").Where("rank", "<", 2)
				})
			},
			func(r wwRow) bool { return r.Status != "active" || (r.Status == "active" && r.Rank < 2) }},
		{"IN",
			func(q *Query[wwRow]) *Query[wwRow] { return q.WhereIn("rank", []any{1, 3, 5}) },
			func(r wwRow) bool { return r.Rank == 1 || r.Rank == 3 || r.Rank == 5 }},
		{"NOT IN",
			func(q *Query[wwRow]) *Query[wwRow] { return q.Where("rank", "NOT IN", []any{1, 2}) },
			func(r wwRow) bool { return r.Rank != 1 && r.Rank != 2 }},
		{"WhereNot IN",
			func(q *Query[wwRow]) *Query[wwRow] { return q.WhereNot("status", "IN", []any{"active", "pending"}) },
			func(r wwRow) bool { return r.Status != "active" && r.Status != "pending" }},
		{"BETWEEN",
			func(q *Query[wwRow]) *Query[wwRow] { return q.WhereBetween("rank", 2, 4) },
			func(r wwRow) bool { return r.Rank >= 2 && r.Rank <= 4 }},
		{"NOT BETWEEN",
			func(q *Query[wwRow]) *Query[wwRow] { return q.Where("rank", "NOT BETWEEN", []any{2, 4}) },
			func(r wwRow) bool { return r.Rank < 2 || r.Rank > 4 }},
		{"IS NULL",
			func(q *Query[wwRow]) *Query[wwRow] { return q.Where("note", "IS NULL", nil) },
			func(r wwRow) bool { return r.Note == nil }},
		{"IS NOT NULL",
			func(q *Query[wwRow]) *Query[wwRow] { return q.Where("note", "IS NOT NULL", nil) },
			func(r wwRow) bool { return r.Note != nil }},
		{"LIKE escaped",
			func(q *Query[wwRow]) *Query[wwRow] { return q.WhereContains("name", "%") },
			func(r wwRow) bool { return strings.Contains(r.Name, "%") }},
		{"NOT LIKE escaped",
			func(q *Query[wwRow]) *Query[wwRow] { return q.WhereNotLike("name", "%"+EscapeLike("%")+"%") },
			func(r wwRow) bool { return !strings.Contains(r.Name, "%") }},
		{"WhereExpr",
			func(q *Query[wwRow]) *Query[wwRow] {
				return q.WhereExpr(Or(Eq(Col("status"), Lit("pending")), Gt(Col("rank"), Lit(5))))
			},
			func(r wwRow) bool { return r.Status == "pending" || r.Rank > 5 }},
		{"WhereNot of a WhereExpr-free Where, then WhereExpr",
			func(q *Query[wwRow]) *Query[wwRow] {
				return q.WhereNot("rank", "<", 3).WhereExpr(Not(Eq(Col("status"), Lit("pending"))))
			},
			func(r wwRow) bool { return r.Rank >= 3 && r.Status != "pending" }},
	}
}

func wwClient(t *testing.T, name string, opts ...any) (*Client, *txStatementRecorder) {
	t.Helper()
	rec := &txStatementRecorder{}
	opts = append([]any{
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithQueryObserver(rec),
	}, opts...)
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared", opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &wwRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, r := range wwFixture() {
		r := r
		if err := For[wwRow](ctx, c).Create(&r); err != nil {
			t.Fatalf("seed %d: %v", r.ID, err)
		}
	}
	rec.reset()
	return c, rec
}

func wwIDs(rows []wwRow, keep func(wwRow) bool) []int64 {
	ids := []int64{}
	for _, r := range rows {
		if keep(r) {
			ids = append(ids, r.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// wwSelected checks the read half of the property — the SELECT returns the
// rows the predicate names — and returns them.
func wwSelected(t *testing.T, c *Client, tc wwCase) []int64 {
	t.Helper()
	rows, err := tc.apply(For[wwRow](context.Background(), c)).Limit(100).List()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	got := wwIDs(rows, func(wwRow) bool { return true })
	want := wwIDs(wwFixture(), tc.want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SELECT returned %v, the predicate names %v", got, want)
	}
	return got
}

func wwAll(t *testing.T, c *Client) []wwRow {
	t.Helper()
	rows, err := For[wwRow](context.Background(), c).Limit(100).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return rows
}

func TestDeleteByRemovesWhatListReturns(t *testing.T) {
	for i, tc := range wwCases() {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := wwClient(t, fmt.Sprintf("qk39_delete_%d", i))
			selected := wwSelected(t, c, tc)
			rec.reset()
			n, err := tc.apply(For[wwRow](context.Background(), c)).DeleteBy()
			if err != nil {
				t.Fatalf("DeleteBy: %v (%v)", err, rec.all)
			}
			gone := wwIDs(wwFixture(), func(r wwRow) bool {
				for _, left := range wwAll(t, c) {
					if left.ID == r.ID {
						return false
					}
				}
				return true
			})
			if !reflect.DeepEqual(gone, selected) || n != int64(len(selected)) {
				t.Errorf("DeleteBy removed %v (n=%d), the SELECT returned %v\n%v", gone, n, selected, rec.all)
			}
		})
	}
}

func TestUpdateMapWritesWhatListReturns(t *testing.T) {
	for i, tc := range wwCases() {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := wwClient(t, fmt.Sprintf("qk39_updatemap_%d", i))
			selected := wwSelected(t, c, tc)
			rec.reset()
			n, err := tc.apply(For[wwRow](context.Background(), c)).UpdateMap(map[string]any{"hits": 1})
			if err != nil {
				t.Fatalf("UpdateMap: %v (%v)", err, rec.all)
			}
			written := wwIDs(wwAll(t, c), func(r wwRow) bool { return r.Hits == 1 })
			if !reflect.DeepEqual(written, selected) || n != int64(len(selected)) {
				t.Errorf("UpdateMap wrote %v (n=%d), the SELECT returned %v\n%v", written, n, selected, rec.all)
			}
		})
	}
}

// UpdateFields is anchored to one entity's key, and the caller's conditions
// narrow it: each row is written exactly when the SELECT returns it.
func TestUpdateFieldsWritesARowOnlyWhenListReturnsIt(t *testing.T) {
	for i, tc := range wwCases() {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := wwClient(t, fmt.Sprintf("qk39_updatefields_%d", i))
			selected := wwSelected(t, c, tc)
			rec.reset()
			for _, r := range wwFixture() {
				e := r
				e.Hits = 1
				if _, err := tc.apply(For[wwRow](context.Background(), c)).UpdateFields(&e, "hits"); err != nil {
					t.Fatalf("UpdateFields(%d): %v (%v)", r.ID, err, rec.all)
				}
			}
			written := wwIDs(wwAll(t, c), func(r wwRow) bool { return r.Hits == 1 })
			if !reflect.DeepEqual(written, selected) {
				t.Errorf("UpdateFields wrote %v, the SELECT returned %v\n%v", written, selected, rec.all)
			}
		})
	}
}

// The reported case, by name: the rows WhereNot excludes survive DeleteBy.
func TestWhereNotDeleteByKeepsTheExcludedRows(t *testing.T) {
	c, rec := wwClient(t, "qk39_reported")
	n, err := For[wwRow](context.Background(), c).WhereNot("status", "=", "active").DeleteBy()
	if err != nil {
		t.Fatalf("DeleteBy: %v", err)
	}
	left := wwIDs(wwAll(t, c), func(wwRow) bool { return true })
	if n != 3 || !reflect.DeepEqual(left, []int64{1, 2, 6}) {
		t.Fatalf("WhereNot(status = active).DeleteBy() removed %d rows and left %v; want 3 removed and the active rows 1, 2, 6 left\n%v", n, left, rec.all)
	}
}

// The placeholders of the WHERE continue after the SET arguments and the key
// predicate on the engines that number them; the Or group stays inside the
// parentheses that AND it with the key.
func TestWriteWherePlaceholdersContinueAfterSet(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		dialect    Dialect
		updateMap  string
		fieldsTail string
	}{
		{PostgreSQL(),
			`UPDATE "ww_rows" SET "hits" = $1, "name" = $2 WHERE (NOT "status" IN ($3, $4) OR ("rank" BETWEEN $5 AND $6))`,
			`WHERE "id" = $2 AND (NOT "status" IN ($3, $4) OR ("rank" BETWEEN $5 AND $6))`},
		{MSSQL(),
			`UPDATE [ww_rows] SET [hits] = @p1, [name] = @p2 WHERE (NOT [status] IN (@p3, @p4) OR ([rank] BETWEEN @p5 AND @p6))`,
			`WHERE [id] = @p2 AND (NOT [status] IN (@p3, @p4) OR ([rank] BETWEEN @p5 AND @p6))`},
		{Oracle(),
			`UPDATE "WW_ROWS" SET "HITS" = :1, "NAME" = :2 WHERE (NOT "STATUS" IN (:3, :4) OR ("RANK" BETWEEN :5 AND :6))`,
			`WHERE "ID" = :2 AND (NOT "STATUS" IN (:3, :4) OR ("RANK" BETWEEN :5 AND :6))`},
	} {
		t.Run(tc.dialect.Name(), func(t *testing.T) {
			c, err := New("sqlite", "file:qk39_placeholders_"+tc.dialect.Name()+"?mode=memory&cache=shared", WithDialect(tc.dialect))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer c.Close()
			q := For[wwRow](ctx, c).WhereNot("status", "IN", []any{"active", "pending"}).
				Or(func(g *Query[wwRow]) *Query[wwRow] { return g.WhereBetween("rank", 2, 4) })

			sqlStr, args, err := q.buildUpdateMap(map[string]any{"hits": 1, "name": "n"})
			if err != nil {
				t.Fatalf("buildUpdateMap: %v", err)
			}
			if sqlStr != tc.updateMap || len(args) != 6 {
				t.Errorf("UpdateMap statement:\n got %s (%d args)\nwant %s (6 args)", sqlStr, len(args), tc.updateMap)
			}

			// UpdateFields: SET hits = 1, key = 2, then the conditions.
			w, wargs, err := q.whereForWrite(3)
			if err != nil {
				t.Fatalf("whereForWrite: %v", err)
			}
			got := q.dialect.Quote("id") + " = " + q.dialect.Placeholder(2) + " AND " + w
			if !strings.HasSuffix(tc.fieldsTail, got) || len(wargs) != 4 {
				t.Errorf("key-anchored WHERE:\n got WHERE %s (%d args)\nwant %s (4 args)", got, len(wargs), tc.fieldsTail)
			}
		})
	}
}

// The tenant predicate RowLevelSecurityClient injects is one of the
// conditions, and it stays in the write — rendered next to the caller's own,
// and ANDed with an Or group instead of being escaped by it.
func TestWriteWhereKeepsTheTenantPredicate(t *testing.T) {
	c, rec := wwClient(t, "qk39_tenant")
	ctx := context.Background()
	if _, err := c.Raw().Exec(`ALTER TABLE ww_rows ADD COLUMN tenant_id TEXT`); err != nil {
		t.Fatalf("add tenant column: %v", err)
	}
	if _, err := c.Raw().Exec(`UPDATE ww_rows SET tenant_id = CASE WHEN id <= 3 THEN 'ta' ELSE 'tb' END`); err != nil {
		t.Fatalf("assign tenants: %v", err)
	}
	cfg := DefaultTenantConfig()
	cfg.Strategy = RowLevelSecurityClient
	cfg.BaseClient = c
	type tenantKey struct{}
	router := NewTenantRouter(cfg, func(ctx context.Context) string {
		tenant, _ := ctx.Value(tenantKey{}).(string)
		return tenant
	}, nil)
	ta := context.WithValue(ctx, tenantKey{}, "ta")

	rec.reset()
	n, err := For[wwRow](ta, router).WhereNot("status", "=", "active").
		Or(func(g *Query[wwRow]) *Query[wwRow] { return g.Where("rank", ">", 0) }).
		DeleteBy()
	if err != nil {
		t.Fatalf("DeleteBy under the tenant router: %v", err)
	}
	// Tenant ta owns 1, 2, 3; the Or group matches all of them and nothing of tb.
	left := wwIDs(wwAll(t, c), func(wwRow) bool { return true })
	if n != 3 || !reflect.DeepEqual(left, []int64{4, 5, 6}) {
		t.Fatalf("DeleteBy under tenant ta removed %d rows and left %v; want ta's three rows removed and tb's 4, 5, 6 left\n%v", n, left, rec.all)
	}
	if !rec.any(`"tenant_id" = ?`) {
		t.Errorf("the statement carries no tenant predicate: %v", rec.all)
	}
}

// PreloadWhere's filters go through the same renderer: IN and BETWEEN load the
// children they name instead of failing to bind, and a lower-case `is null`
// keeps meaning IS NULL as it always did in this loader.
type wwParent struct {
	ID       int64     `db:"id" pk:"true"`
	Children []wwChild `rel:"has_many" join:"parent_id"`
}

func (wwParent) TableName() string { return "ww_parents" }

type wwChild struct {
	ID       int64   `db:"id" pk:"true"`
	ParentID int64   `db:"parent_id"`
	Rank     int64   `db:"rank"`
	Note     *string `db:"note"`
}

func (wwChild) TableName() string { return "ww_children" }

func TestPreloadWhereRendersLikeWhere(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:qk39_preload?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()
	if err := c.Migrate(ctx, &wwParent{}, &wwChild{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := For[wwParent](ctx, c).Create(&wwParent{ID: 1}); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	for i := int64(1); i <= 5; i++ {
		ch := wwChild{ID: i, ParentID: 1, Rank: i}
		if i%2 == 0 {
			ch.Note = wwNote("n")
		}
		if err := For[wwChild](ctx, c).Create(&ch); err != nil {
			t.Fatalf("seed child: %v", err)
		}
	}
	for _, tc := range []struct {
		name  string
		op    string
		value any
		want  []int64
	}{
		{"IN", "IN", []any{1, 4}, []int64{1, 4}},
		{"BETWEEN", "BETWEEN", []any{2, 3}, []int64{2, 3}},
		{"=", "=", 5, []int64{5}},
		{"is null", "is null", nil, []int64{1, 3, 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			col := "rank"
			if strings.Contains(tc.op, "null") {
				col = "note"
			}
			parents, err := For[wwParent](ctx, c).PreloadWhere("Children", col, tc.op, tc.value).List()
			if err != nil {
				t.Fatalf("PreloadWhere %s: %v", tc.op, err)
			}
			if len(parents) != 1 {
				t.Fatalf("parents = %d, want 1", len(parents))
			}
			var got []int64
			for _, ch := range parents[0].Children {
				got = append(got, ch.ID)
			}
			sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("children loaded = %v, want %v", got, tc.want)
			}
		})
	}
}
