package ent

// The ent benchmarks are the codegen-tier reference point: ent generates a
// fully typed client from a schema (no runtime reflection on the hot path),
// the same tier Quark's own generated scanners/binders (F6-2/F6-3) target.
//
// They live in their own package — and therefore their own test binary —
// for the same reason GORM does: this package links modernc.org/sqlite to
// register the database/sql "sqlite" name, and importing it alongside the
// GORM binary's glebarez driver would panic with "Register called twice".
// `go test ./...` builds each package independently, so neither sees the
// other's driver. Every implementation runs on the same modernc engine, so
// the comparison stays fair.
//
// The benchmark is an internal test of the generated `ent` package so it can
// use the typed client directly; it pulls the shared fixtures from
// internal/model without importing the Quark core.

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/jcsvwinston/quark/benchmarks/ent/benchuser"
	"github.com/jcsvwinston/quark/benchmarks/internal/model"

	_ "modernc.org/sqlite"
)

var dbCounter atomic.Int64

// uniqueMemDSN returns a distinct shared-cache in-memory SQLite DSN. The
// _pragma=foreign_keys(1) clause (modernc's pragma syntax) satisfies ent's
// migration engine, which refuses to run with foreign keys off.
func uniqueMemDSN() string {
	return fmt.Sprintf("file:ent_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", dbCounter.Add(1))
}

func newEntClient(b *testing.B) *Client {
	b.Helper()
	db, err := sql.Open("sqlite", uniqueMemDSN())
	if err != nil {
		b.Fatalf("open ent db: %v", err)
	}
	db.SetMaxOpenConns(1)
	drv := entsql.OpenDB(dialect.SQLite, db)
	client := NewClient(Driver(drv))
	if err := client.Schema.Create(context.Background()); err != nil {
		b.Fatalf("ent schema create: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })
	return client
}

func seedEnt(b *testing.B, client *Client) {
	b.Helper()
	ctx := context.Background()
	builders := make([]*BenchUserCreate, 0, model.BatchSize)
	for i := 0; i < model.SeedRows; i++ {
		u := model.MakeUser(i)
		builders = append(builders, client.BenchUser.Create().
			SetName(u.Name).SetEmail(u.Email).SetAge(u.Age).SetActive(u.Active))
		if len(builders) == model.BatchSize {
			if _, err := client.BenchUser.CreateBulk(builders...).Save(ctx); err != nil {
				b.Fatalf("seed ent: %v", err)
			}
			builders = builders[:0]
		}
	}
	if len(builders) > 0 {
		if _, err := client.BenchUser.CreateBulk(builders...).Save(ctx); err != nil {
			b.Fatalf("seed ent: %v", err)
		}
	}
}

func BenchmarkEnt_InsertOne(b *testing.B) {
	client := newEntClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := model.MakeUser(i)
		if _, err := client.BenchUser.Create().
			SetName(u.Name).SetEmail(u.Email).SetAge(u.Age).SetActive(u.Active).
			Save(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEnt_InsertBatch(b *testing.B) {
	client := newEntClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		builders := make([]*BenchUserCreate, model.BatchSize)
		for j := range builders {
			u := model.MakeUser(i*model.BatchSize + j)
			builders[j] = client.BenchUser.Create().
				SetName(u.Name).SetEmail(u.Email).SetAge(u.Age).SetActive(u.Active)
		}
		if _, err := client.BenchUser.CreateBulk(builders...).Save(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEnt_FindByPK(b *testing.B) {
	client := newEntClient(b)
	seedEnt(b, client)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := i%model.SeedRows + 1
		u, err := client.BenchUser.Get(ctx, id)
		if err != nil {
			b.Fatal(err)
		}
		_ = u
	}
}

func BenchmarkEnt_ListWhere(b *testing.B) {
	client := newEntClient(b)
	seedEnt(b, client)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := client.BenchUser.Query().
			Where(benchuser.AgeGTE(model.MinAge)).
			Order(benchuser.ByID()).
			Limit(model.ListLimit).
			All(ctx)
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkEnt_Update(b *testing.B) {
	client := newEntClient(b)
	seedEnt(b, client)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := i%model.SeedRows + 1
		if _, err := client.BenchUser.UpdateOneID(id).
			SetName("updated").SetEmail("updated@example.com").SetAge(99).SetActive(true).
			Save(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
