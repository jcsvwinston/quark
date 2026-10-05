// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build integration
// +build integration

package enginesuite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"

	// lib/pq, registered as "postgres": the driver the installation guide
	// names. It is v1.10.9 here on purpose — the release that binds no Go
	// slice — so the test shows the array goes as text any driver binds.
	_ "github.com/lib/pq"
)

// QK-35. On PostgreSQL a preload selects the related rows with
// `col = ANY($1)` and binds the keys as the text of an array, where it used
// to send `col IN ($1, …, $n)`. These tests pin, on the real engine and
// through both drivers Quark names for PostgreSQL, that every key shape
// loads exactly the rows it loaded before: int64 keys into bigint, int32
// keys into integer, string keys into text — quotes, backslashes, commas,
// braces and the word NULL included — and into uuid; and that the
// placeholders after the array (a PreloadWhere, the tenant column) are
// numbered from it. A key the driver binds through driver.Valuer keeps the
// IN list, and so does every other engine.

type paParent struct {
	ID       int64     `db:"id" pk:"true"`
	TenantID string    `db:"tenant_id"`
	Name     string    `db:"name"`
	Kids     []paKid   `rel:"has_many" join:"parent_id"`
	Notes    []paNote  `rel:"polymorphic" polymorphic:"owner_type:parent" join:"owner_id"`
	Tags     []paTag   `rel:"many_to_many" m2m:"pa_parent_tags:parent_id:tag_id"`
	Favorite *paKid    `rel:"has_one" join:"parent_id"`
	Siblings []paChild `rel:"has_many" join:"parent_id"`
}

func (paParent) TableName() string { return "pa_parents" }

type paKid struct {
	ID       int64     `db:"id" pk:"true"`
	TenantID string    `db:"tenant_id"`
	ParentID int64     `db:"parent_id"`
	Name     string    `db:"name"`
	Parent   *paParent `rel:"belongs_to" join:"parent_id"`
}

func (paKid) TableName() string { return "pa_kids" }

// paChild maps the same rows as paKid, for a second has_many on the parent.
type paChild struct {
	ID       int64  `db:"id" pk:"true"`
	ParentID int64  `db:"parent_id"`
	Name     string `db:"name"`
}

func (paChild) TableName() string { return "pa_kids" }

type paNote struct {
	ID        int64  `db:"id" pk:"true"`
	OwnerType string `db:"owner_type"`
	OwnerID   int64  `db:"owner_id"`
	Body      string `db:"body"`
}

func (paNote) TableName() string { return "pa_notes" }

type paTag struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (paTag) TableName() string { return "pa_tags" }

type paSmall struct {
	ID   int32        `db:"id" pk:"true"`
	Kids []paSmallKid `rel:"has_many" join:"small_id"`
}

func (paSmall) TableName() string { return "pa_smalls" }

type paSmallKid struct {
	ID      int32 `db:"id" pk:"true"`
	SmallID int32 `db:"small_id"`
}

func (paSmallKid) TableName() string { return "pa_small_kids" }

type paCode struct {
	Code  string   `db:"code" pk:"true"`
	Items []paItem `rel:"has_many" join:"code"`
}

func (paCode) TableName() string { return "pa_codes" }

type paItem struct {
	ID   int64  `db:"id" pk:"true"`
	Code string `db:"code"`
}

func (paItem) TableName() string { return "pa_items" }

type paDoc struct {
	ID    string   `db:"id" pk:"true"`
	Pages []paPage `rel:"has_many" join:"doc_id"`
}

func (paDoc) TableName() string { return "pa_docs" }

type paPage struct {
	ID    int64  `db:"id" pk:"true"`
	DocID string `db:"doc_id"`
}

func (paPage) TableName() string { return "pa_pages" }

// paRef is a key the driver binds through driver.Valuer: Quark cannot know
// what Value returns, so it keeps the IN list for it.
type paRef string

func (r paRef) Value() (driver.Value, error) { return string(r), nil }

type paBox struct {
	ID     paRef     `db:"id" pk:"true"`
	Things []paThing `rel:"has_many" join:"box_id"`
}

func (paBox) TableName() string { return "pa_boxes" }

type paThing struct {
	ID    int64 `db:"id" pk:"true"`
	BoxID paRef `db:"box_id"`
}

func (paThing) TableName() string { return "pa_things" }

const paSchema = `
DROP TABLE IF EXISTS pa_parent_tags, pa_tags, pa_notes, pa_kids, pa_parents, pa_small_kids, pa_smalls, pa_items, pa_codes, pa_pages, pa_docs, pa_things, pa_boxes;
CREATE TABLE pa_parents (id BIGSERIAL PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL);
CREATE TABLE pa_kids (id BIGSERIAL PRIMARY KEY, tenant_id TEXT NOT NULL, parent_id BIGINT NOT NULL, name TEXT NOT NULL);
CREATE TABLE pa_notes (id BIGSERIAL PRIMARY KEY, owner_type TEXT NOT NULL, owner_id BIGINT NOT NULL, body TEXT NOT NULL);
CREATE TABLE pa_tags (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE pa_parent_tags (parent_id BIGINT NOT NULL, tag_id BIGINT NOT NULL);
CREATE TABLE pa_smalls (id INTEGER PRIMARY KEY);
CREATE TABLE pa_small_kids (id INTEGER PRIMARY KEY, small_id INTEGER NOT NULL);
CREATE TABLE pa_codes (code TEXT PRIMARY KEY);
CREATE TABLE pa_items (id BIGSERIAL PRIMARY KEY, code TEXT NOT NULL);
CREATE TABLE pa_docs (id UUID PRIMARY KEY);
CREATE TABLE pa_pages (id BIGSERIAL PRIMARY KEY, doc_id UUID NOT NULL);
CREATE TABLE pa_boxes (id TEXT PRIMARY KEY);
CREATE TABLE pa_things (id BIGSERIAL PRIMARY KEY, box_id TEXT NOT NULL);
INSERT INTO pa_parents (id, tenant_id, name) VALUES (1, 'ta', 'p1'), (2, 'ta', 'p2'), (3, 'tb', 'p3');
INSERT INTO pa_kids (tenant_id, parent_id, name) VALUES
  ('ta', 1, 'k1a'), ('ta', 1, 'k1b'), ('ta', 1, 'skip'), ('ta', 2, 'k2a'), ('tb', 1, 'foreign'), ('tb', 3, 'k3a');
INSERT INTO pa_notes (owner_type, owner_id, body) VALUES ('parent', 1, 'n1'), ('parent', 2, 'n2'), ('other', 1, 'not mine');
INSERT INTO pa_tags (id, name) VALUES (10, 't10'), (20, 't20');
INSERT INTO pa_parent_tags (parent_id, tag_id) VALUES (1, 10), (1, 20), (2, 20);
INSERT INTO pa_smalls (id) VALUES (1), (2);
INSERT INTO pa_small_kids (id, small_id) VALUES (1, 1), (2, 1), (3, 2);
INSERT INTO pa_codes (code) VALUES ('a"b'), ('c\d'), ('e,f'), ('{g}'), ('NULL'), ('plain');
INSERT INTO pa_items (code) VALUES ('a"b'), ('c\d'), ('c\d'), ('e,f'), ('{g}'), ('NULL'), ('plain'), ('orphan');
INSERT INTO pa_docs (id) VALUES ('6f1c3c1e-8a9b-4a52-9d3f-1c2b3a4d5e6f'), ('0e8f2a7b-1c3d-4e5f-8a9b-0c1d2e3f4a5b');
INSERT INTO pa_pages (doc_id) VALUES ('6f1c3c1e-8a9b-4a52-9d3f-1c2b3a4d5e6f'), ('6f1c3c1e-8a9b-4a52-9d3f-1c2b3a4d5e6f'), ('0e8f2a7b-1c3d-4e5f-8a9b-0c1d2e3f4a5b');
INSERT INTO pa_boxes (id) VALUES ('b1'), ('b2');
INSERT INTO pa_things (box_id) VALUES ('b1'), ('b2'), ('b2');
`

// paStatements records the preload statements a client sends.
type paStatements struct {
	quark.BaseMiddleware
	mu   sync.Mutex
	sqls []string
	args [][]any
}

func (m *paStatements) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, s string, a []any) (*sql.Rows, error) {
		m.mu.Lock()
		m.sqls = append(m.sqls, s)
		m.args = append(m.args, append([]any(nil), a...))
		m.mu.Unlock()
		return next(ctx, exec, s, a)
	}
}

func (m *paStatements) take() ([]string, [][]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, a := m.sqls, m.args
	m.sqls, m.args = nil, nil
	return s, a
}

// on returns the statements that read table.
func (m *paStatements) on(table string) []string {
	sqls, _ := m.take()
	var out []string
	for _, s := range sqls {
		if strings.Contains(s, `FROM "`+table+`"`) {
			out = append(out, s)
		}
	}
	return out
}

func TestPostgresPreloadAnyArray(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(paSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP TABLE IF EXISTS pa_parent_tags, pa_tags, pa_notes, pa_kids, pa_parents, pa_small_kids, pa_smalls, pa_items, pa_codes, pa_pages, pa_docs, pa_things, pa_boxes`)
	})

	// lib/pq takes the same URL; it needs sslmode spelled out like pgx does.
	for _, driverName := range []string{"pgx", "postgres"} {
		t.Run(driverName, func(t *testing.T) {
			rec := &paStatements{}
			client, err := quark.New(driverName, dsn,
				quark.WithMiddleware(rec),
				quark.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
			if err != nil {
				t.Fatalf("quark.New(%s): %v", driverName, err)
			}
			defer client.Close()
			ctx := context.Background()
			testPreloadAnyArray(ctx, t, client, rec)
		})
	}
}

func testPreloadAnyArray(ctx context.Context, t *testing.T, client *quark.Client, rec *paStatements) {
	wantAny := func(t *testing.T, table string) {
		t.Helper()
		sqls := rec.on(table)
		if len(sqls) == 0 {
			t.Fatalf("no statement read %s", table)
		}
		for _, s := range sqls {
			if !strings.Contains(s, "= ANY($") || strings.Contains(s, " IN (") {
				t.Errorf("the preload of %s sent %q, want = ANY($n) and no IN list", table, s)
			}
		}
	}

	t.Run("Int64KeysHasMany", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paParent](ctx, client).OrderBy("id", "ASC").Limit(10).Preload("Kids").List()
		if err != nil {
			t.Fatal(err)
		}
		wantAny(t, "pa_kids")
		want := map[int64][]string{1: {"k1a", "k1b", "skip", "foreign"}, 2: {"k2a"}, 3: {"k3a"}}
		for _, p := range got {
			if g := kidNames(p.Kids); !sameStrings(g, want[p.ID]) {
				t.Errorf("parent %d kids = %v, want %v", p.ID, g, want[p.ID])
			}
		}
	})

	t.Run("BelongsToWithRepeatedKeys", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paKid](ctx, client).OrderBy("id", "ASC").Limit(10).Preload("Parent").List()
		if err != nil {
			t.Fatal(err)
		}
		wantAny(t, "pa_parents")
		for _, k := range got {
			if k.Parent == nil || k.Parent.ID != k.ParentID {
				t.Errorf("kid %d (parent_id %d) loaded parent %+v", k.ID, k.ParentID, k.Parent)
			}
		}
	})

	t.Run("HasOneAndSecondHasMany", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paParent](ctx, client).Where("id", "=", int64(2)).Preload("Favorite").Preload("Siblings").List()
		if err != nil {
			t.Fatal(err)
		}
		wantAny(t, "pa_kids")
		if len(got) != 1 || got[0].Favorite == nil || got[0].Favorite.Name != "k2a" || len(got[0].Siblings) != 1 || got[0].Siblings[0].Name != "k2a" {
			t.Errorf("parent 2 loaded %+v", got)
		}
	})

	t.Run("PreloadWhereIsNumberedAfterTheArray", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paParent](ctx, client).Where("id", "=", int64(1)).PreloadWhere("Kids", "name", "<>", "skip").List()
		if err != nil {
			t.Fatal(err)
		}
		sqls := rec.on("pa_kids")
		if len(sqls) != 1 || !strings.Contains(sqls[0], `"parent_id" = ANY($1)`) || !strings.Contains(sqls[0], "$2") {
			t.Errorf("the filtered preload sent %q, want the array at $1 and the filter at $2", sqls)
		}
		if len(got) != 1 || !sameStrings(kidNames(got[0].Kids), []string{"k1a", "k1b", "foreign"}) {
			t.Errorf("filtered kids of parent 1 = %+v", got)
		}
	})

	t.Run("TenantColumnIsNumberedAfterTheArray", func(t *testing.T) {
		cfg := quark.DefaultTenantConfig()
		cfg.Strategy = quark.RowLevelSecurityClient
		cfg.BaseClient = client
		router := quark.NewTenantRouter(cfg, func(context.Context) string { return "ta" }, nil)
		rec.take()
		got, err := quark.For[paParent](ctx, router).OrderBy("id", "ASC").Limit(10).Preload("Kids").List()
		if err != nil {
			t.Fatal(err)
		}
		sqls := rec.on("pa_kids")
		if len(sqls) != 1 || !strings.Contains(sqls[0], `"parent_id" = ANY($1)`) || !strings.Contains(sqls[0], `"tenant_id" = $2`) {
			t.Errorf("the tenant's preload sent %q, want the array at $1 and the tenant at $2", sqls)
		}
		want := map[int64][]string{1: {"k1a", "k1b", "skip"}, 2: {"k2a"}}
		if len(got) != 2 {
			t.Fatalf("tenant ta sees %d parents, want 2: %+v", len(got), got)
		}
		for _, p := range got {
			if g := kidNames(p.Kids); !sameStrings(g, want[p.ID]) {
				t.Errorf("tenant ta, parent %d kids = %v, want %v", p.ID, g, want[p.ID])
			}
		}
	})

	t.Run("Polymorphic", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paParent](ctx, client).Where("id", "<=", int64(2)).OrderBy("id", "ASC").Preload("Notes").List()
		if err != nil {
			t.Fatal(err)
		}
		sqls := rec.on("pa_notes")
		if len(sqls) != 1 || !strings.Contains(sqls[0], `"owner_id" = ANY($2)`) {
			t.Errorf("the polymorphic preload sent %q, want the type at $1 and the array at $2", sqls)
		}
		if len(got) != 2 || len(got[0].Notes) != 1 || got[0].Notes[0].Body != "n1" || len(got[1].Notes) != 1 || got[1].Notes[0].Body != "n2" {
			t.Errorf("notes = %+v", got)
		}
	})

	t.Run("ManyToMany", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paParent](ctx, client).Where("id", "<=", int64(2)).OrderBy("id", "ASC").Preload("Tags").List()
		if err != nil {
			t.Fatal(err)
		}
		sqls, _ := rec.take()
		anyCount := 0
		for _, s := range sqls {
			if strings.Contains(s, " IN (") {
				t.Errorf("the m2m preload sent an IN list: %q", s)
			}
			if strings.Contains(s, "= ANY($1)") {
				anyCount++
			}
		}
		if anyCount != 2 {
			t.Errorf("the m2m preload sent %d = ANY statements, want 2 (join table, related table): %q", anyCount, sqls)
		}
		var names [][]string
		for _, p := range got {
			var n []string
			for _, tg := range p.Tags {
				n = append(n, tg.Name)
			}
			sort.Strings(n)
			names = append(names, n)
		}
		if fmt.Sprint(names) != "[[t10 t20] [t20]]" {
			t.Errorf("tags = %v, want [[t10 t20] [t20]]", names)
		}
	})

	t.Run("Int32KeysIntoInteger", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paSmall](ctx, client).OrderBy("id", "ASC").Limit(10).Preload("Kids").List()
		if err != nil {
			t.Fatal(err)
		}
		wantAny(t, "pa_small_kids")
		if len(got) != 2 || len(got[0].Kids) != 2 || len(got[1].Kids) != 1 {
			t.Errorf("smalls = %+v", got)
		}
	})

	t.Run("StringKeysThatNeedQuoting", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paCode](ctx, client).OrderBy("code", "ASC").Limit(10).Preload("Items").List()
		if err != nil {
			t.Fatal(err)
		}
		wantAny(t, "pa_items")
		want := map[string]int{`a"b`: 1, `c\d`: 2, "e,f": 1, "{g}": 1, "NULL": 1, "plain": 1}
		if len(got) != len(want) {
			t.Fatalf("codes = %+v", got)
		}
		for _, c := range got {
			if len(c.Items) != want[c.Code] {
				t.Errorf("code %q loaded %d items, want %d", c.Code, len(c.Items), want[c.Code])
			}
			for _, it := range c.Items {
				if it.Code != c.Code {
					t.Errorf("code %q received item of code %q", c.Code, it.Code)
				}
			}
		}
	})

	t.Run("StringKeysIntoUUID", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paDoc](ctx, client).OrderBy("id", "ASC").Limit(10).Preload("Pages").List()
		if err != nil {
			t.Fatal(err)
		}
		wantAny(t, "pa_pages")
		counts := map[string]int{}
		for _, d := range got {
			counts[d.ID] = len(d.Pages)
		}
		if counts["6f1c3c1e-8a9b-4a52-9d3f-1c2b3a4d5e6f"] != 2 || counts["0e8f2a7b-1c3d-4e5f-8a9b-0c1d2e3f4a5b"] != 1 {
			t.Errorf("pages per doc = %v", counts)
		}
	})

	t.Run("ValuerKeysKeepTheINList", func(t *testing.T) {
		rec.take()
		got, err := quark.For[paBox](ctx, client).OrderBy("id", "ASC").Limit(10).Preload("Things").List()
		if err != nil {
			t.Fatal(err)
		}
		sqls := rec.on("pa_things")
		if len(sqls) != 1 || !strings.Contains(sqls[0], `"box_id" IN ($1, $2)`) {
			t.Errorf("the preload of Valuer keys sent %q, want the IN list", sqls)
		}
		if len(got) != 2 || len(got[0].Things) != 1 || len(got[1].Things) != 2 {
			t.Errorf("boxes = %+v", got)
		}
	})
}

func kidNames(ks []paKid) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.Name
	}
	return out
}

// sameStrings compares in order: the rows of one parent arrive in the order
// the engine returned them, which for these unordered heap scans is the
// insertion order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
