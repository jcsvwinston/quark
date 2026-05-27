package sqlcbench

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jcsvwinston/quark/benchmarks/internal/model"
	"github.com/jcsvwinston/quark/benchmarks/sqlc/sqlcdb"

	_ "modernc.org/sqlite"
)

// The sqlc benchmarks exercise sqlc's generated typed query methods. sqlc
// does not own a runtime: the generated code is thin wrappers over
// database/sql, so this sits just above the raw floor and is the leanest
// codegen-tier point in the comparison.

var dbCounter atomic.Int64

func uniqueMemDSN() string {
	return fmt.Sprintf("file:sqlc_%d?mode=memory&cache=shared", dbCounter.Add(1))
}

func newSqlcDB(b *testing.B) (*sql.DB, *sqlcdb.Queries) {
	b.Helper()
	db, err := sql.Open("sqlite", uniqueMemDSN())
	if err != nil {
		b.Fatalf("open sqlc db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(model.RawCreateTableSQL); err != nil {
		b.Fatalf("create table: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db, sqlcdb.New(db)
}

func seedSqlc(b *testing.B, db *sql.DB, q *sqlcdb.Queries) {
	b.Helper()
	ctx := context.Background()
	for i := 0; i < model.SeedRows; i++ {
		u := model.MakeUser(i)
		if err := q.InsertUser(ctx, sqlcdb.InsertUserParams{
			Name: u.Name, Email: u.Email, Age: int64(u.Age), Active: u.Active,
		}); err != nil {
			b.Fatalf("seed sqlc: %v", err)
		}
	}
}

func BenchmarkSqlc_InsertOne(b *testing.B) {
	_, q := newSqlcDB(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := model.MakeUser(i)
		if err := q.InsertUser(ctx, sqlcdb.InsertUserParams{
			Name: u.Name, Email: u.Email, Age: int64(u.Age), Active: u.Active,
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSqlc_InsertBatch loops single-row inserts inside one transaction.
// sqlc emits no variadic multi-row INSERT for SQLite (its :copyfrom/:batch
// helpers are pgx-only), so a transaction-wrapped loop is the idiomatic sqlc
// batch. This is a genuine API asymmetry vs the raw/Quark/GORM multi-row
// VALUES batch — read the sqlc batch number with that in mind.
func BenchmarkSqlc_InsertBatch(b *testing.B) {
	db, q := newSqlcDB(b)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			b.Fatal(err)
		}
		qtx := q.WithTx(tx)
		for j := 0; j < model.BatchSize; j++ {
			u := model.MakeUser(i*model.BatchSize + j)
			if err := qtx.InsertUser(ctx, sqlcdb.InsertUserParams{
				Name: u.Name, Email: u.Email, Age: int64(u.Age), Active: u.Active,
			}); err != nil {
				_ = tx.Rollback()
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSqlc_FindByPK(b *testing.B) {
	db, q := newSqlcDB(b)
	seedSqlc(b, db, q)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		u, err := q.GetUser(ctx, id)
		if err != nil {
			b.Fatal(err)
		}
		_ = u
	}
}

func BenchmarkSqlc_ListWhere(b *testing.B) {
	db, q := newSqlcDB(b)
	seedSqlc(b, db, q)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := q.ListUsersByAge(ctx, sqlcdb.ListUsersByAgeParams{
			Age: model.MinAge, Limit: model.ListLimit,
		})
		if err != nil {
			b.Fatal(err)
		}
		_ = out
	}
}

func BenchmarkSqlc_Update(b *testing.B) {
	db, q := newSqlcDB(b)
	seedSqlc(b, db, q)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		if err := q.UpdateUser(ctx, sqlcdb.UpdateUserParams{
			Name: "updated", Email: "updated@example.com", Age: 99, Active: true, ID: id,
		}); err != nil {
			b.Fatal(err)
		}
	}
}
