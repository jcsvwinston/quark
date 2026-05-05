# Blog API — Quark End-to-End Example

A minimal REST API for a blog, demonstrating Quark ORM integrated with the Go standard library (`net/http`). Uses **SQLite** by default — no external database or Docker required.

---

## What It Shows

- Struct-based models with Quark tags (`pk`, `quark:"not_null"`, `quark:"unique"`)
- Versioned migrations via `github.com/jcsvwinston/quark/migrate`
- Type-safe CRUD: `Create`, `Find`, `Update`, `HardDelete`
- Query builder: `Where`, `OrderBy`, `List`
- HTTP handlers using Go 1.22+ `net/http` routing with path parameters (`{id}`)
- Soft-delete via `deleted_at` field on `Post`

---

## Run

From the repository root:

```bash
go run ./examples/blog-api
```

The server starts on `http://localhost:8080` and creates `blog.db` in the current directory.

---

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/authors` | List all authors |
| `POST` | `/authors` | Create an author |
| `GET` | `/posts` | List posts (`?published=true` to filter) |
| `POST` | `/posts` | Create a post |
| `GET` | `/posts/{id}` | Get a single post |
| `PUT` | `/posts/{id}` | Update a post (partial — zero-value fields skipped) |
| `DELETE` | `/posts/{id}` | Hard-delete a post |

---

## Quick curl Session

```bash
# Create an author
curl -s -X POST http://localhost:8080/authors \
  -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"alice@example.com","bio":"Go enthusiast"}' | jq .

# Create a post
curl -s -X POST http://localhost:8080/posts \
  -H "Content-Type: application/json" \
  -d '{"title":"Hello Quark","body":"Type-safe ORM for Go.","author_id":1,"published":true}' | jq .

# List published posts
curl -s "http://localhost:8080/posts?published=true" | jq .

# Get a specific post
curl -s http://localhost:8080/posts/1 | jq .

# Delete a post
curl -s -X DELETE http://localhost:8080/posts/1
```

---

## Tests

```bash
go test ./examples/blog-api/... -v
```

All tests use an in-memory SQLite database — no setup needed.

---

## Switch to PostgreSQL

Change two lines in `main.go`:

```go
// main.go
import _ "github.com/jackc/pgx/v5/stdlib"

srv, err := newServer("postgres://user:pass@localhost:5432/blog?sslmode=disable")
// Then change quark.SQLite() → quark.PostgreSQL() inside newServer()
```

No query code changes required.
