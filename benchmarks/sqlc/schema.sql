-- Mirrors benchmarks/internal/model.BenchUser and the raw baseline schema:
-- the same bench_users table with id/name/email/age/active. active is
-- declared BOOLEAN (NUMERIC affinity in SQLite, identical storage to the
-- raw baseline's INTEGER) so sqlc emits a typed Go bool field, matching the
-- mapping the other implementations do. sqlc maps SQLite INTEGER columns to
-- Go int64, so the generated id/age fields are int64 (the benchmark converts
-- the shared model's int age accordingly).
CREATE TABLE bench_users (
    id     INTEGER PRIMARY KEY,
    name   TEXT    NOT NULL,
    email  TEXT    NOT NULL,
    age    INTEGER NOT NULL,
    active BOOLEAN NOT NULL
);
