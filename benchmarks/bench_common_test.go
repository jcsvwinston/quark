package quarkbench

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/benchmarks/internal/model"

	// Registers the pure-Go "sqlite" driver used by both the raw baseline
	// and Quark. Quark itself also imports this package (db_errors.go), so
	// the registration happens once regardless; the blank import here makes
	// the dependency explicit.
	_ "modernc.org/sqlite"
)

// quietLogger discards Quark's structured logs so they do not interleave
// with benchmark output. No query observer is attached, so there is no
// per-query logging on the hot path either.
var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

var dbCounter atomic.Int64

// uniqueMemDSN returns a distinct shared-cache in-memory SQLite DSN so each
// benchmark gets an isolated database. Shared cache keeps the in-memory
// database alive across the (single) pooled connection.
func uniqueMemDSN(prefix string) string {
	return fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", prefix, dbCounter.Add(1))
}

// --- raw database/sql ---

func newRawDB(b *testing.B) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", uniqueMemDSN("raw"))
	if err != nil {
		b.Fatalf("open raw db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(model.RawCreateTableSQL); err != nil {
		b.Fatalf("create table: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	return db
}

func seedRawDB(b *testing.B, db *sql.DB) {
	b.Helper()
	for i := 0; i < model.SeedRows; i++ {
		u := model.MakeUser(i)
		if _, err := db.Exec(
			`INSERT INTO bench_users (name, email, age, active) VALUES (?, ?, ?, ?)`,
			u.Name, u.Email, u.Age, u.Active,
		); err != nil {
			b.Fatalf("seed raw: %v", err)
		}
	}
}

// --- Quark ---

func newQuarkClient(b *testing.B) *quark.Client {
	b.Helper()
	client, err := quark.New("sqlite", uniqueMemDSN("quark"),
		quark.WithMaxOpenConns(1),
		quark.WithLogger(quietLogger),
	)
	if err != nil {
		b.Fatalf("open quark client: %v", err)
	}
	if err := client.Migrate(context.Background(), &model.BenchUser{}); err != nil {
		b.Fatalf("quark migrate: %v", err)
	}
	b.Cleanup(func() { _ = client.Close() })
	return client
}

func seedQuark(b *testing.B, client *quark.Client) {
	b.Helper()
	ctx := context.Background()
	users := make([]*model.BenchUser, model.SeedRows)
	for i := range users {
		u := model.MakeUser(i)
		users[i] = &u
	}
	if err := quark.For[model.BenchUser](ctx, client).CreateBatch(users); err != nil {
		b.Fatalf("seed quark: %v", err)
	}
}
