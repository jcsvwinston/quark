package quark_test

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSuitePostgres(t *testing.T) {
	dsn := os.Getenv("QUARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New(db,
		quark.WithDialect(quark.PostgreSQL()),
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	SharedSuite(t, client)
}
