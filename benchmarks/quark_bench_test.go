package quarkbench

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/benchmarks/internal/model"
)

// The Quark benchmarks exercise the current reflect-based path
// (scanRow / buildInsert / buildUpdate). They are the pre-codegen
// baseline: the gap between these and the raw numbers is the overhead the
// generated typed scanners/binders (F6-2/F6-3) are meant to close, and the
// delta the ADR-0002 v1.0 gate is measured against.

func BenchmarkQuark_InsertOne(b *testing.B) {
	client := newQuarkClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := model.MakeUser(i)
		if err := quark.For[model.BenchUser](ctx, client).Create(&u); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuark_InsertBatch(b *testing.B) {
	client := newQuarkClient(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		users := make([]*model.BenchUser, model.BatchSize)
		for j := range users {
			u := model.MakeUser(i*model.BatchSize + j)
			users[j] = &u
		}
		if err := quark.For[model.BenchUser](ctx, client).CreateBatch(users); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuark_FindByPK(b *testing.B) {
	client := newQuarkClient(b)
	seedQuark(b, client)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		u, err := quark.For[model.BenchUser](ctx, client).Find(id)
		if err != nil {
			b.Fatal(err)
		}
		_ = u
	}
}

func BenchmarkQuark_ListWhere(b *testing.B) {
	client := newQuarkClient(b)
	seedQuark(b, client)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := quark.For[model.BenchUser](ctx, client).
			Where("age", ">=", model.MinAge).
			OrderBy("id", "ASC").
			Limit(model.ListLimit).
			List()
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkQuark_Update(b *testing.B) {
	client := newQuarkClient(b)
	seedQuark(b, client)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		u := model.BenchUser{ID: id, Name: "updated", Email: "updated@example.com", Age: 99, Active: true}
		if _, err := quark.For[model.BenchUser](ctx, client).Update(&u); err != nil {
			b.Fatal(err)
		}
	}
}
