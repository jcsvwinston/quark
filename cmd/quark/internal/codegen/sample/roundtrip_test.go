// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package sample_test

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/codegen/sample"

	_ "modernc.org/sqlite"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// reflectAccount mirrors sample.Account field-for-field but has no generated
// scanner, so every query takes the reflection path. Comparing a row read
// through Account (generated scanner, registered by sample's quark_gen.go)
// against the same row read through reflectAccount proves the generated read
// path returns identical results — the F6-2 round-trip guarantee. Because the
// generated scanner routes every field through quark.ScanTarget (the same
// helper the reflection path uses), this equivalence holds on every engine,
// not just SQLite; SQLite is the cheap CI proof.
type reflectAccount struct {
	ID        int64                  `db:"id" pk:"true"`
	Email     string                 `db:"email"`
	Age       int                    `db:"age"`
	Balance   float64                `db:"balance"`
	Active    bool                   `db:"active"`
	Settings  quark.JSON[string]     `db:"settings"`
	Nickname  quark.Nullable[string] `db:"nickname"`
	CreatedAt time.Time              `db:"created_at"`
	UpdatedAt *time.Time             `db:"updated_at"`
}

func (reflectAccount) TableName() string { return "reflect_accounts" }

func newClient(t *testing.T) *quark.Client {
	t.Helper()
	c, err := quark.New("sqlite", "file:f6_2_roundtrip?mode=memory&cache=shared",
		quark.WithMaxOpenConns(1), quark.WithLogger(quietLogger))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestF6_2_GeneratedScannerRoundTrip(t *testing.T) {
	// Guard: the generated scanner must actually be registered, otherwise
	// this test would silently exercise reflection on both sides.
	if _, has := quark.CheckGeneratedDrift(reflect.TypeOf(sample.Account{})); !has {
		t.Fatal("sample.Account has no generated scanner registered; the v2 codegen did not run")
	}

	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &sample.Account{}, &reflectAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 5, 23, 10, 30, 0, 0, time.UTC)
	updated := now.Add(time.Hour)
	g := sample.Account{
		Email: "a@example.com", Age: 41, Balance: 12.5, Active: true,
		Settings:  quark.JSON[string]{V: "cfg"},
		Nickname:  quark.Nullable[string]{V: "nick", Valid: true},
		CreatedAt: now, UpdatedAt: &updated,
	}
	r := reflectAccount{
		Email: "a@example.com", Age: 41, Balance: 12.5, Active: true,
		Settings:  quark.JSON[string]{V: "cfg"},
		Nickname:  quark.Nullable[string]{V: "nick", Valid: true},
		CreatedAt: now, UpdatedAt: &updated,
	}
	// Guard: the generated binder must be registered too, so Create exercises
	// the generated write path and not reflection on both sides.
	if !quark.GeneratedBinderRegistered(reflect.TypeOf(sample.Account{})) {
		t.Fatal("sample.Account has no generated binder registered; the v3 codegen did not run")
	}
	if err := quark.For[sample.Account](ctx, c).Create(&g); err != nil {
		t.Fatalf("create generated: %v", err)
	}
	if err := quark.For[reflectAccount](ctx, c).Create(&r); err != nil {
		t.Fatalf("create reflect: %v", err)
	}

	// Find (single row) — generated scanner vs reflection.
	gotGen, err := quark.For[sample.Account](ctx, c).Find(g.ID)
	if err != nil {
		t.Fatalf("find generated: %v", err)
	}
	gotRef, err := quark.For[reflectAccount](ctx, c).Find(r.ID)
	if err != nil {
		t.Fatalf("find reflect: %v", err)
	}
	assertEqual(t, "Find", gotGen, gotRef)

	// List (multi-row path) routes through the same scanRow.
	listGen, err := quark.For[sample.Account](ctx, c).Where("id", "=", g.ID).List()
	if err != nil || len(listGen) != 1 {
		t.Fatalf("list generated: %v (n=%d)", err, len(listGen))
	}
	assertEqual(t, "List", listGen[0], gotRef)
}

func assertEqual(t *testing.T, where string, g sample.Account, r reflectAccount) {
	t.Helper()
	if g.ID != r.ID || g.Email != r.Email || g.Age != r.Age || g.Balance != r.Balance || g.Active != r.Active {
		t.Errorf("%s: scalar mismatch generated=%+v reflect=%+v", where, g, r)
	}
	if g.Settings != r.Settings {
		t.Errorf("%s: Settings mismatch generated=%+v reflect=%+v", where, g.Settings, r.Settings)
	}
	if g.Nickname != r.Nickname {
		t.Errorf("%s: Nickname mismatch generated=%+v reflect=%+v", where, g.Nickname, r.Nickname)
	}
	if !g.CreatedAt.Equal(r.CreatedAt) {
		t.Errorf("%s: CreatedAt mismatch generated=%v reflect=%v", where, g.CreatedAt, r.CreatedAt)
	}
	switch {
	case g.UpdatedAt == nil && r.UpdatedAt == nil:
	case g.UpdatedAt == nil || r.UpdatedAt == nil:
		t.Errorf("%s: UpdatedAt nilness mismatch generated=%v reflect=%v", where, g.UpdatedAt, r.UpdatedAt)
	case !g.UpdatedAt.Equal(*r.UpdatedAt):
		t.Errorf("%s: UpdatedAt mismatch generated=%v reflect=%v", where, *g.UpdatedAt, *r.UpdatedAt)
	}
}

// BenchmarkScanGenerated and BenchmarkScanReflect query the same single row
// through the generated scanner and the reflection path respectively. The
// SQL, row, and data are identical, so the delta is the scan path. (The
// shared-cache in-memory DB is seeded once; each iteration is a Find.)
func BenchmarkScanGenerated(b *testing.B) {
	ctx := context.Background()
	c := benchClient(b, "file:f6_2_bench_gen?mode=memory&cache=shared")
	id := benchSeed(b, c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := quark.For[sample.Account](ctx, c).Find(id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanReflect(b *testing.B) {
	ctx := context.Background()
	c := benchClient(b, "file:f6_2_bench_ref?mode=memory&cache=shared")
	id := benchSeedReflect(b, c)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := quark.For[reflectAccount](ctx, c).Find(id); err != nil {
			b.Fatal(err)
		}
	}
}

// The List benchmarks scan many rows per iteration, so the per-row scan path
// (generated switch vs reflection field lookup) dominates more of the
// measured time than in the single-row Find benchmarks above.
const benchListRows = 200

func BenchmarkScanGeneratedList(b *testing.B) {
	ctx := context.Background()
	c := benchClient(b, "file:f6_2_bench_glist?mode=memory&cache=shared")
	benchSeedN(b, c, &sample.Account{}, benchListRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := quark.For[sample.Account](ctx, c).Limit(benchListRows).List()
		if err != nil || len(out) != benchListRows {
			b.Fatalf("list: %v (n=%d)", err, len(out))
		}
	}
}

func BenchmarkScanReflectList(b *testing.B) {
	ctx := context.Background()
	c := benchClient(b, "file:f6_2_bench_rlist?mode=memory&cache=shared")
	benchSeedN(b, c, &reflectAccount{}, benchListRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := quark.For[reflectAccount](ctx, c).Limit(benchListRows).List()
		if err != nil || len(out) != benchListRows {
			b.Fatalf("list: %v (n=%d)", err, len(out))
		}
	}
}

func benchSeedN(b *testing.B, c *quark.Client, model any, n int) {
	b.Helper()
	ctx := context.Background()
	if err := c.Migrate(ctx, model); err != nil {
		b.Fatal(err)
	}
	switch model.(type) {
	case *sample.Account:
		for i := 0; i < n; i++ {
			a := sample.Account{Email: "x", Age: i, Balance: float64(i), Active: true, CreatedAt: time.Now()}
			if err := quark.For[sample.Account](ctx, c).Create(&a); err != nil {
				b.Fatal(err)
			}
		}
	case *reflectAccount:
		for i := 0; i < n; i++ {
			a := reflectAccount{Email: "x", Age: i, Balance: float64(i), Active: true, CreatedAt: time.Now()}
			if err := quark.For[reflectAccount](ctx, c).Create(&a); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// The Create benchmarks measure the write path: the generated INSERT binder
// (Account) vs the reflection bind loop (reflectAccount), same columns and
// data. This is the F6-3a write-path data point for the ADR-0002 gate. Both
// insert into a growing table, so absolute latency drifts up over b.N; the
// gen-vs-reflect comparison stays valid because both grow identically.
func BenchmarkCreateGenerated(b *testing.B) {
	ctx := context.Background()
	c := benchClient(b, "file:f6_3_bench_gen?mode=memory&cache=shared")
	if err := c.Migrate(ctx, &sample.Account{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := sample.Account{Email: "x", Age: i, Balance: 1, Active: true, CreatedAt: time.Now()}
		if err := quark.For[sample.Account](ctx, c).Create(&a); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCreateReflect(b *testing.B) {
	ctx := context.Background()
	c := benchClient(b, "file:f6_3_bench_ref?mode=memory&cache=shared")
	if err := c.Migrate(ctx, &reflectAccount{}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := reflectAccount{Email: "x", Age: i, Balance: 1, Active: true, CreatedAt: time.Now()}
		if err := quark.For[reflectAccount](ctx, c).Create(&a); err != nil {
			b.Fatal(err)
		}
	}
}

func benchClient(b *testing.B, dsn string) *quark.Client {
	b.Helper()
	c, err := quark.New("sqlite", dsn, quark.WithMaxOpenConns(1), quark.WithLogger(quietLogger))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = c.Close() })
	return c
}

func benchSeed(b *testing.B, c *quark.Client) int64 {
	b.Helper()
	ctx := context.Background()
	if err := c.Migrate(ctx, &sample.Account{}); err != nil {
		b.Fatal(err)
	}
	a := sample.Account{Email: "b@example.com", Age: 30, Balance: 1, Active: true, CreatedAt: time.Now()}
	if err := quark.For[sample.Account](ctx, c).Create(&a); err != nil {
		b.Fatal(err)
	}
	return a.ID
}

func benchSeedReflect(b *testing.B, c *quark.Client) int64 {
	b.Helper()
	ctx := context.Background()
	if err := c.Migrate(ctx, &reflectAccount{}); err != nil {
		b.Fatal(err)
	}
	a := reflectAccount{Email: "b@example.com", Age: 30, Balance: 1, Active: true, CreatedAt: time.Now()}
	if err := quark.For[reflectAccount](ctx, c).Create(&a); err != nil {
		b.Fatal(err)
	}
	return a.ID
}
