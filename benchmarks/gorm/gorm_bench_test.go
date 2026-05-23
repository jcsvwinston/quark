package gormbench

import (
	"fmt"
	"sync/atomic"
	"testing"

	gormsqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/jcsvwinston/quark/benchmarks/internal/model"
)

// The GORM benchmarks are the reflect-ORM reference point (ADR-0002 names
// GORM as Quark's closest peer: both compute model metadata at runtime).
//
// They live in their own package — and therefore their own test binary —
// because the GORM pure-Go SQLite driver (glebarez/go-sqlite) and the
// driver Quark links (modernc.org/sqlite) both register the database/sql
// "sqlite" name; importing both into one binary panics. glebarez/go-sqlite
// is a fork of modernc, so the underlying engine is the same family. Run
// the whole module with `go test ./...` and both binaries build cleanly.

var dbCounter atomic.Int64

func uniqueMemDSN() string {
	return fmt.Sprintf("file:gorm_%d?mode=memory&cache=shared", dbCounter.Add(1))
}

func newGormDB(b *testing.B) *gorm.DB {
	b.Helper()
	gdb, err := gorm.Open(gormsqlite.Open(uniqueMemDSN()), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		b.Fatalf("open gorm db: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		b.Fatalf("gorm underlying db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := gdb.AutoMigrate(&model.BenchUser{}); err != nil {
		b.Fatalf("gorm automigrate: %v", err)
	}
	b.Cleanup(func() { _ = sqlDB.Close() })
	return gdb
}

func seedGorm(b *testing.B, gdb *gorm.DB) {
	b.Helper()
	users := make([]model.BenchUser, model.SeedRows)
	for i := range users {
		users[i] = model.MakeUser(i)
	}
	if err := gdb.CreateInBatches(users, model.BatchSize).Error; err != nil {
		b.Fatalf("seed gorm: %v", err)
	}
}

func BenchmarkGORM_InsertOne(b *testing.B) {
	gdb := newGormDB(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := model.MakeUser(i)
		if err := gdb.Create(&u).Error; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGORM_InsertBatch(b *testing.B) {
	gdb := newGormDB(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		users := make([]model.BenchUser, model.BatchSize)
		for j := range users {
			users[j] = model.MakeUser(i*model.BatchSize + j)
		}
		if err := gdb.CreateInBatches(users, model.BatchSize).Error; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGORM_FindByPK(b *testing.B) {
	gdb := newGormDB(b)
	seedGorm(b, gdb)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		var u model.BenchUser
		if err := gdb.First(&u, id).Error; err != nil {
			b.Fatal(err)
		}
		_ = u
	}
}

func BenchmarkGORM_ListWhere(b *testing.B) {
	gdb := newGormDB(b)
	seedGorm(b, gdb)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out []model.BenchUser
		if err := gdb.Where("age >= ?", model.MinAge).Order("id ASC").Limit(model.ListLimit).Find(&out).Error; err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkGORM_Update(b *testing.B) {
	gdb := newGormDB(b)
	seedGorm(b, gdb)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		u := model.BenchUser{ID: id, Name: "updated", Email: "updated@example.com", Age: 99, Active: true}
		if err := gdb.Save(&u).Error; err != nil {
			b.Fatal(err)
		}
	}
}
