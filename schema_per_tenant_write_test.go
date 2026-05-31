// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

type sptTag struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type sptPost struct {
	ID       int64    `db:"id" pk:"true"`
	TenantID string   `db:"tenant_id"`
	Title    string   `db:"title"`
	Tags     []sptTag `rel:"many_to_many" m2m:"spt_post_tags:post_id:tag_id"`
}

// sqlCapture records the SQL of every executed statement.
type sqlCapture struct {
	mu   sync.Mutex
	stmt []string
}

func (c *sqlCapture) ObserveQuery(e quark.QueryEvent) {
	c.mu.Lock()
	c.stmt = append(c.stmt, e.SQL)
	c.mu.Unlock()
}

func (c *sqlCapture) firstWith(sub string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.stmt {
		if strings.Contains(strings.ToUpper(s), strings.ToUpper(sub)) {
			return s
		}
	}
	return ""
}

// TestSchemaPerTenantWritesAreSchemaQualified is the BB-8 regression. Under
// SchemaPerTenant the save paths used to build their INSERT/UPDATE from a
// BaseQuery that dropped the resolved schema, so writes hit the default schema
// while reads honoured the tenant schema (rows "vanished", and tenants
// co-mingled). It covers the three write paths the fix touches: entity insert
// (saveAny), m2m link insert (linkM2M), and batch update (UpdateBatch).
//
// It routes with tenant "main" — SQLite's real default schema — so the queries
// actually execute and we can assert both the emitted SQL (schema-qualified)
// and the functional round-trip.
func TestSchemaPerTenantWritesAreSchemaQualified(t *testing.T) {
	ctx := context.Background()
	cap := &sqlCapture{}
	client, err := quark.New("sqlite", ":memory:", quark.WithQueryObserver(cap))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Migrate(ctx, &sptTag{}, &sptPost{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.SchemaPerTenant
	cfg.BaseClient = client
	type ctxKey string
	const k ctxKey = "tenant_id"
	router := quark.NewTenantRouter(cfg,
		func(c context.Context) string {
			if v, ok := c.Value(k).(string); ok {
				return v
			}
			return ""
		}, nil)
	tctx := context.WithValue(ctx, k, "main") // SQLite's default schema

	tag1 := &sptTag{Name: "go"}
	tag2 := &sptTag{Name: "orm"}
	if err := quark.For[sptTag](tctx, router).Create(tag1); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if err := quark.For[sptTag](tctx, router).Create(tag2); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	// Create with an m2m relation exercises both saveAny (post insert) and
	// linkM2M (join-table inserts).
	post := &sptPost{TenantID: "main", Title: "hello", Tags: []sptTag{*tag1, *tag2}}
	if err := quark.For[sptPost](tctx, router).Create(post); err != nil {
		t.Fatalf("create post: %v", err)
	}

	// Emitted SQL must be schema-qualified for the entity and the join table.
	if ins := cap.firstWith(`INSERT INTO "main"."spt_posts"`); ins == "" {
		t.Errorf("entity INSERT not schema-qualified (BB-8); statements:\n%s", strings.Join(cap.stmt, "\n"))
	}
	if link := cap.firstWith(`INSERT INTO "main"."spt_post_tags"`); link == "" {
		t.Errorf("m2m link INSERT not schema-qualified (BB-8); statements:\n%s", strings.Join(cap.stmt, "\n"))
	}

	// Functional round-trip: the link rows must be readable back through the
	// schema (they would be invisible if they landed in the wrong schema).
	loaded, err := quark.For[sptPost](tctx, router).Preload("Tags").Find(post.ID)
	if err != nil {
		t.Fatalf("find with preload: %v", err)
	}
	if len(loaded.Tags) != 2 {
		t.Errorf("loaded %d m2m tags, want 2 (link rows landed in wrong schema?)", len(loaded.Tags))
	}

	// UpdateBatch must also stay in the tenant schema.
	post.Title = "updated"
	if err := quark.For[sptPost](tctx, router).UpdateBatch([]*sptPost{post}); err != nil {
		t.Fatalf("update batch: %v", err)
	}
	if upd := cap.firstWith(`UPDATE "main"."spt_posts"`); upd == "" {
		t.Errorf("UpdateBatch UPDATE not schema-qualified (BB-8); statements:\n%s", strings.Join(cap.stmt, "\n"))
	}
	reloaded, err := quark.For[sptPost](tctx, router).Find(post.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Title != "updated" {
		t.Errorf("UpdateBatch did not persist in-schema: title=%q want %q", reloaded.Title, "updated")
	}
}
