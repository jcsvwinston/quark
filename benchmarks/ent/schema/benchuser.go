// Package schema holds the ent schema for the benchmark model. ent
// generates its typed client from this definition; the generated code lives
// in the parent package (entbench). Only the fields ent does not add itself
// are declared here — ent injects the integer "id" primary key.
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// BenchUser mirrors benchmarks/internal/model.BenchUser: the same five
// columns (id, name, email, age, active) on the same bench_users table, so
// the ent benchmark measures ent's mapping overhead on an identical schema.
type BenchUser struct {
	ent.Schema
}

// Annotations pins the table name to bench_users so every implementation
// hits a table of the same name and shape.
func (BenchUser) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "bench_users"},
	}
}

// Fields declares the non-PK columns; ent adds the "id" primary key.
func (BenchUser) Fields() []ent.Field {
	return []ent.Field{
		field.String("name"),
		field.String("email"),
		field.Int("age"),
		field.Bool("active"),
	}
}
