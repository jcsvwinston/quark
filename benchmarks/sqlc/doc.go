// Package sqlcbench holds the sqlc comparison benchmarks. sqlc is a
// codegen tool that turns annotated SQL (schema.sql + query.sql) into typed
// Go methods over database/sql, with no runtime reflection — the same
// codegen tier as ent and as Quark's own generated path (F6-2/F6-3).
//
// It is a separate package (and test binary) from the Quark/raw and GORM
// benchmarks so the modernc.org/sqlite driver it links does not collide with
// GORM's glebarez driver registration in a single binary. `go test ./...`
// builds each package independently.
//
// Regenerate the typed code with `sqlc generate` from this directory
// (config in sqlc.yaml); the generated package lives in ./sqlcdb and is
// committed so CI does not need the sqlc binary.
package sqlcbench
