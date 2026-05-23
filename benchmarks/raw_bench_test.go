package quarkbench

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/benchmarks/internal/model"
)

// The raw database/sql benchmarks are the performance floor: hand-written
// SQL with manual Scan/Exec and no reflection. They represent the best a
// developer can do by hand, and the target the generated code path
// (F6-2/F6-3) aims to approach.

// buildBatchInsertSQL returns a single multi-row INSERT for n rows, the
// idiomatic hand-written batch insert.
func buildBatchInsertSQL(n int) string {
	var sb strings.Builder
	sb.WriteString("INSERT INTO bench_users (name, email, age, active) VALUES ")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString("(?, ?, ?, ?)")
	}
	return sb.String()
}

func BenchmarkRaw_InsertOne(b *testing.B) {
	db := newRawDB(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u := model.MakeUser(i)
		if _, err := db.Exec(
			`INSERT INTO bench_users (name, email, age, active) VALUES (?, ?, ?, ?)`,
			u.Name, u.Email, u.Age, u.Active,
		); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRaw_InsertBatch(b *testing.B) {
	db := newRawDB(b)
	query := buildBatchInsertSQL(model.BatchSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		args := make([]any, 0, model.BatchSize*4)
		for j := 0; j < model.BatchSize; j++ {
			u := model.MakeUser(i*model.BatchSize + j)
			args = append(args, u.Name, u.Email, u.Age, u.Active)
		}
		if _, err := db.Exec(query, args...); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRaw_FindByPK(b *testing.B) {
	db := newRawDB(b)
	seedRawDB(b, db)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		var u model.BenchUser
		if err := db.QueryRow(
			`SELECT id, name, email, age, active FROM bench_users WHERE id = ?`, id,
		).Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active); err != nil {
			b.Fatal(err)
		}
		_ = u
	}
}

func BenchmarkRaw_ListWhere(b *testing.B) {
	db := newRawDB(b)
	seedRawDB(b, db)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.Query(
			`SELECT id, name, email, age, active FROM bench_users WHERE age >= ? ORDER BY id LIMIT ?`,
			model.MinAge, model.ListLimit,
		)
		if err != nil {
			b.Fatal(err)
		}
		out := make([]model.BenchUser, 0, model.ListLimit)
		for rows.Next() {
			var u model.BenchUser
			if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active); err != nil {
				_ = rows.Close()
				b.Fatal(err)
			}
			out = append(out, u)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			b.Fatal(err)
		}
		_ = rows.Close()
		_ = out
	}
}

func BenchmarkRaw_Update(b *testing.B) {
	db := newRawDB(b)
	seedRawDB(b, db)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := int64(i%model.SeedRows) + 1
		if _, err := db.Exec(
			`UPDATE bench_users SET name=?, email=?, age=?, active=? WHERE id=?`,
			"updated", "updated@example.com", 99, true, id,
		); err != nil {
			b.Fatal(err)
		}
	}
}
