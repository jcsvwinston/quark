// Package model holds the shared benchmark model and fixtures. It imports
// no ORM and no SQLite driver, so it can be used from both the Quark/raw
// test binary (which links modernc.org/sqlite) and the GORM test binary
// (which links the glebarez driver) without a duplicate driver
// registration. See ../README.md for why the two live in separate binaries.
package model

import "fmt"

// BenchUser is the single model shared by every implementation.
//
//   - the db / pk tags drive Quark,
//   - the gorm tags drive GORM,
//   - the columns map 1:1 to the hand-written SQL in the raw baseline.
//
// One struct keeps the field set, types, and column names identical across
// implementations, so the comparison measures mapping overhead rather than
// schema differences. Struct tags are inert strings, so carrying both tag
// dialects here costs no imports.
type BenchUser struct {
	ID     int64  `db:"id" pk:"true" gorm:"primaryKey;column:id"`
	Name   string `db:"name" gorm:"column:name"`
	Email  string `db:"email" gorm:"column:email"`
	Age    int    `db:"age" gorm:"column:age"`
	Active bool   `db:"active" gorm:"column:active"`
}

// TableName pins the table to bench_users for both Quark (via its
// TableNamer interface) and GORM, so every implementation hits a table of
// the same name and shape.
func (BenchUser) TableName() string { return "bench_users" }

const (
	// SeedRows is the number of rows pre-loaded before read benchmarks.
	SeedRows = 1000
	// BatchSize is the number of rows inserted per batch-insert iteration.
	BatchSize = 100
	// ListLimit is the number of rows returned by the ListWhere benchmark.
	ListLimit = 50
	// MinAge is the ListWhere predicate floor: WHERE age >= MinAge.
	MinAge = 18
)

// RawCreateTableSQL mirrors the idiomatic SQLite schema the ORMs migrate
// to: an integer rowid primary key plus four typed columns.
const RawCreateTableSQL = `CREATE TABLE bench_users (
	id     INTEGER PRIMARY KEY,
	name   TEXT    NOT NULL,
	email  TEXT    NOT NULL,
	age    INTEGER NOT NULL,
	active INTEGER NOT NULL
)`

// MakeUser builds a deterministic row for index i.
func MakeUser(i int) BenchUser {
	return BenchUser{
		Name:   fmt.Sprintf("user%06d", i),
		Email:  fmt.Sprintf("user%06d@example.com", i),
		Age:    MinAge + i%50,
		Active: i%2 == 0,
	}
}
