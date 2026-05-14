// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"testing"
	"time"
)

// keyFor builds a BaseQuery with the given isolation fields and returns
// the cache key for sqlStr + args. dialect is fixed to SQLite — only its
// Name() participates in the key.
func keyFor(tenantID, schema, sqlStr string, args ...any) string {
	q := &BaseQuery{dialect: SQLite(), tenantID: tenantID, schema: schema}
	return q.generateCacheKey(sqlStr, args)
}

// TestGenerateCacheKey_Deterministic pins the headline contract: the same
// query + args produce the same key every time. A regression here breaks
// every cache hit.
func TestGenerateCacheKey_Deterministic(t *testing.T) {
	a := keyFor("t1", "public", "SELECT * FROM users WHERE id = ?", int64(1))
	b := keyFor("t1", "public", "SELECT * FROM users WHERE id = ?", int64(1))
	if a != b {
		t.Errorf("same query+args must yield the same key:\n  %s\n  %s", a, b)
	}
	if len(a) != 64 { // sha256 hex
		t.Errorf("key should be a 64-char sha256 hex digest, got %d chars", len(a))
	}
}

// TestGenerateCacheKey_TypeCollision is the F4-4 headline fix: the old
// fmt.Sprintf("%v", arg) encoding rendered int64(1) and string("1")
// identically, so a parameterised SELECT could serve a result bound for
// the wrong type. They must now produce distinct keys.
func TestGenerateCacheKey_TypeCollision(t *testing.T) {
	const sql = "SELECT * FROM t WHERE c = ?"
	cases := []struct {
		name string
		a, b any
	}{
		{"int64 vs string", int64(1), "1"},
		{"int64 vs uint64", int64(1), uint64(1)},
		{"int64 vs float64", int64(1), float64(1)},
		{"bool true vs string", true, "true"},
		{"nil vs empty string", nil, ""},
		{"int64 vs bool", int64(1), true},
		{"float64 1.0 vs string", float64(1.0), "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ka := keyFor("", "", sql, tc.a)
			kb := keyFor("", "", sql, tc.b)
			if ka == kb {
				t.Errorf("%T(%v) and %T(%v) must not collide — both keyed as %s",
					tc.a, tc.a, tc.b, tc.b, ka)
			}
		})
	}
}

// TestGenerateCacheKey_BoundaryCollision pins that field boundaries are
// length-prefixed: without separators, "ab"+"" hashed the same stream as
// "a"+"b", and tenant "my"+schema "sql" the same as tenant "mysql"+schema "".
func TestGenerateCacheKey_BoundaryCollision(t *testing.T) {
	const sql = "SELECT 1"

	t.Run("args boundary", func(t *testing.T) {
		ab := keyFor("", "", sql, "ab", "")
		split := keyFor("", "", sql, "a", "b")
		if ab == split {
			t.Errorf(`["ab",""] and ["a","b"] must not collide — both %s`, ab)
		}
	})

	t.Run("isolation field boundary", func(t *testing.T) {
		x := keyFor("my", "sql", sql)
		y := keyFor("mysql", "", sql)
		if x == y {
			t.Errorf(`tenant/schema boundary collision — both %s`, x)
		}
	})

	t.Run("arg count matters", func(t *testing.T) {
		one := keyFor("", "", sql, "")
		none := keyFor("", "", sql)
		if one == none {
			t.Errorf("a single empty-string arg must differ from no args — both %s", one)
		}
	})
}

// TestGenerateCacheKey_Time pins the time.Time contract: the same instant
// in different zones is the SAME key (a legitimate cache hit, not a
// collision), and distinct instants never share a key.
func TestGenerateCacheKey_Time(t *testing.T) {
	const sql = "SELECT * FROM events WHERE at = ?"
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	instant := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	t.Run("same instant different zones is one key", func(t *testing.T) {
		utcKey := keyFor("", "", sql, instant)
		madridKey := keyFor("", "", sql, instant.In(madrid))
		if utcKey != madridKey {
			t.Errorf("same instant in UTC vs Madrid must share a key:\n  %s\n  %s", utcKey, madridKey)
		}
	})

	t.Run("distinct instants differ", func(t *testing.T) {
		a := keyFor("", "", sql, instant)
		b := keyFor("", "", sql, instant.Add(time.Second))
		if a == b {
			t.Errorf("instants one second apart must not collide — both %s", a)
		}
	})
}

// TestGenerateCacheKey_QueryDiscriminants pins that the SQL string and the
// isolation fields all participate — a different query or a different
// tenant must never reuse another's cache entry.
func TestGenerateCacheKey_QueryDiscriminants(t *testing.T) {
	base := keyFor("t1", "public", "SELECT * FROM users", int64(1))

	if base == keyFor("t1", "public", "SELECT * FROM orders", int64(1)) {
		t.Error("different sqlStr must yield a different key")
	}
	if base == keyFor("t2", "public", "SELECT * FROM users", int64(1)) {
		t.Error("different tenantID must yield a different key")
	}
	if base == keyFor("t1", "private", "SELECT * FROM users", int64(1)) {
		t.Error("different schema must yield a different key")
	}
	if base == keyFor("t1", "public", "SELECT * FROM users", int64(2)) {
		t.Error("different arg value must yield a different key")
	}
}
