// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

type shUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (shUser) TableName() string { return "sh_users" }

var shQuiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newShard(t *testing.T, dsn string) *quark.Client {
	t.Helper()
	c, err := quark.New("sqlite", dsn, quark.WithMaxOpenConns(1), quark.WithLogger(shQuiet))
	if err != nil {
		t.Fatalf("open shard %s: %v", dsn, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(context.Background(), &shUser{}); err != nil {
		t.Fatalf("migrate shard %s: %v", dsn, err)
	}
	return c
}

// TestShardRouterRouting proves F6-7: a write/read routes to the shard that
// owns its shard key, and the row never appears on another shard.
func TestShardRouterRouting(t *testing.T) {
	ctx := context.Background()
	shards := map[string]*quark.Client{
		"a": newShard(t, "file:sh_route_a?mode=memory&cache=shared"),
		"b": newShard(t, "file:sh_route_b?mode=memory&cache=shared"),
	}
	shardFor := quark.HashShardFunc([]string{"a", "b"})
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, shardFor)
	if err != nil {
		t.Fatalf("NewShardRouter: %v", err)
	}

	used := map[string]bool{}
	// These six keys split across both shards under FNV-1a mod 2; the
	// used["a"] && used["b"] assertion at the end fails loudly if a key change
	// ever collapses them onto one shard (so this test can't silently stop
	// exercising the other shard).
	keys := []string{"user-1", "user-2", "user-3", "user-4", "user-5", "user-6"}
	for _, k := range keys {
		want := shardFor(k) // the shard this key is supposed to land on
		used[want] = true

		u := shUser{Name: k}
		if err := quark.For[shUser](quark.WithShardKey(ctx, k), router).Create(&u); err != nil {
			t.Fatalf("create key %q: %v", k, err)
		}

		// Reading with the same shard key routes to the same shard and finds it.
		got, err := quark.For[shUser](quark.WithShardKey(ctx, k), router).Where("name", "=", k).List()
		if err != nil || len(got) != 1 {
			t.Fatalf("read key %q: %v (n=%d)", k, err, len(got))
		}

		// The row must live ONLY on the mapped shard, not the other.
		other := "a"
		if want == "a" {
			other = "b"
		}
		leak, err := quark.For[shUser](ctx, shards[other]).Where("name", "=", k).List()
		if err != nil {
			t.Fatalf("read other shard for %q: %v", k, err)
		}
		if len(leak) != 0 {
			t.Errorf("key %q (shard %q) leaked into shard %q", k, want, other)
		}
	}
	if !used["a"] || !used["b"] {
		t.Fatalf("test did not exercise both shards (used=%v); pick keys that distribute", used)
	}
}

// TestShardRouterMissingKey: a query with no shard key in context errors —
// there is no implicit cross-shard fan-out.
func TestShardRouterMissingKey(t *testing.T) {
	ctx := context.Background()
	shards := map[string]*quark.Client{
		"a": newShard(t, "file:sh_miss_a?mode=memory&cache=shared"),
		"b": newShard(t, "file:sh_miss_b?mode=memory&cache=shared"),
	}
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, quark.HashShardFunc([]string{"a", "b"}))
	if err != nil {
		t.Fatalf("NewShardRouter: %v", err)
	}
	if _, err := quark.For[shUser](ctx, router).List(); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Fatalf("read without shard key: err = %v, want ErrInvalidQuery", err)
	}
}

// TestShardRouterConstruction covers the setup-time validation.
func TestShardRouterConstruction(t *testing.T) {
	c := newShard(t, "file:sh_ctor?mode=memory&cache=shared")
	one := map[string]*quark.Client{"a": c}

	if _, err := quark.NewShardRouter(nil, quark.DefaultShardResolver, quark.HashShardFunc([]string{"a"})); err == nil {
		t.Error("empty shards should error")
	}
	if _, err := quark.NewShardRouter(one, nil, quark.HashShardFunc([]string{"a"})); err == nil {
		t.Error("nil resolver should error")
	}
	if _, err := quark.NewShardRouter(one, quark.DefaultShardResolver, nil); err == nil {
		t.Error("nil shardFor should error")
	}
	if _, err := quark.NewShardRouter(map[string]*quark.Client{"a": nil}, quark.DefaultShardResolver, quark.HashShardFunc([]string{"a"})); err == nil {
		t.Error("nil shard client should error")
	}
	if _, err := quark.NewShardRouter(one, quark.DefaultShardResolver, quark.HashShardFunc([]string{"a"})); err != nil {
		t.Errorf("valid construction errored: %v", err)
	}

	// An unknown mapped shard surfaces as an error at GetClient time.
	bad, _ := quark.NewShardRouter(one, quark.DefaultShardResolver, func(string) string { return "nonexistent" })
	if _, err := quark.For[shUser](quark.WithShardKey(context.Background(), "k"), bad).List(); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Errorf("unknown shard: err = %v, want ErrInvalidQuery", err)
	}
}

// shKeyedUser owns its shard key (the model hook of ADR-0016): ShardKey reports
// the partition the row belongs to. The key field is unexported, so it is not a
// persisted column — it only drives routing via WithShardKeyOf. The row itself
// maps to the same sh_users table as shUser (id, name).
type shKeyedUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	key  string // shard key only; not persisted
}

func (shKeyedUser) TableName() string  { return "sh_users" }
func (u shKeyedUser) ShardKey() string { return u.key }

var _ quark.ShardKeyer = shKeyedUser{}

// TestWithShardKeyOfRoutesByEntity: WithShardKeyOf(ctx, entity) routes a write to
// the shard that owns entity.ShardKey() — entity-based routing equivalent to
// WithShardKey(ctx, entity.ShardKey()), with the key-deriving logic on the model.
func TestWithShardKeyOfRoutesByEntity(t *testing.T) {
	ctx := context.Background()
	shards := map[string]*quark.Client{
		"a": newShard(t, "file:sh_keyer_a?mode=memory&cache=shared"),
		"b": newShard(t, "file:sh_keyer_b?mode=memory&cache=shared"),
	}
	shardFor := quark.HashShardFunc([]string{"a", "b"})
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, shardFor)
	if err != nil {
		t.Fatalf("NewShardRouter: %v", err)
	}

	used := map[string]bool{}
	keys := []string{"user-1", "user-2", "user-3", "user-4", "user-5", "user-6"}
	for _, k := range keys {
		want := shardFor(k)
		used[want] = true

		// Route the Create off the entity's own ShardKey(), not a manual key.
		u := shKeyedUser{Name: k, key: k}
		if err := quark.For[shKeyedUser](quark.WithShardKeyOf(ctx, u), router).Create(&u); err != nil {
			t.Fatalf("create keyed %q: %v", k, err)
		}

		// It must land on the mapped shard and nowhere else.
		got, err := quark.For[shUser](ctx, shards[want]).Where("name", "=", k).List()
		if err != nil || len(got) != 1 {
			t.Fatalf("read mapped shard for %q: %v (n=%d)", k, err, len(got))
		}
		other := "a"
		if want == "a" {
			other = "b"
		}
		leak, err := quark.For[shUser](ctx, shards[other]).Where("name", "=", k).List()
		if err != nil {
			t.Fatalf("read other shard for %q: %v", k, err)
		}
		if len(leak) != 0 {
			t.Errorf("entity %q (shard %q) leaked into shard %q", k, want, other)
		}
	}
	if !used["a"] || !used["b"] {
		t.Fatalf("test did not exercise both shards (used=%v)", used)
	}
}

// TestWithShardKeyOfEmptyKey: an entity whose ShardKey() is "" routes nowhere —
// it fails like a missing WithShardKey, never a silent cross-shard fan-out.
func TestWithShardKeyOfEmptyKey(t *testing.T) {
	ctx := context.Background()
	shards := map[string]*quark.Client{
		"a": newShard(t, "file:sh_keyer_empty_a?mode=memory&cache=shared"),
		"b": newShard(t, "file:sh_keyer_empty_b?mode=memory&cache=shared"),
	}
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, quark.HashShardFunc([]string{"a", "b"}))
	if err != nil {
		t.Fatalf("NewShardRouter: %v", err)
	}
	u := shKeyedUser{Name: "no-key"} // key == ""
	if err := quark.For[shKeyedUser](quark.WithShardKeyOf(ctx, u), router).Create(&u); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Fatalf("empty ShardKey: err = %v, want ErrInvalidQuery", err)
	}
}

// TestHashShardFuncDeterministic: same key → same shard, every time.
func TestHashShardFuncDeterministic(t *testing.T) {
	sf := quark.HashShardFunc([]string{"a", "b", "c"})
	for _, k := range []string{"x", "user-42", "región-eu"} {
		first := sf(k)
		for i := 0; i < 100; i++ {
			if sf(k) != first {
				t.Fatalf("HashShardFunc not stable for %q", k)
			}
		}
		if first != "a" && first != "b" && first != "c" {
			t.Errorf("HashShardFunc returned out-of-set shard %q", first)
		}
	}
	// With no shard names, it maps everything to "" (GetClient then errors).
	if quark.HashShardFunc(nil)("k") != "" {
		t.Error("HashShardFunc with no names should map to empty string")
	}
}
