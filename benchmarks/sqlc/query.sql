-- name: InsertUser :exec
INSERT INTO bench_users (name, email, age, active) VALUES (?, ?, ?, ?);

-- name: GetUser :one
SELECT id, name, email, age, active FROM bench_users WHERE id = ?;

-- name: ListUsersByAge :many
SELECT id, name, email, age, active FROM bench_users WHERE age >= ? ORDER BY id LIMIT ?;

-- name: UpdateUser :exec
UPDATE bench_users SET name = ?, email = ?, age = ?, active = ? WHERE id = ?;
