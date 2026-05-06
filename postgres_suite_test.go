package quark_test

import (
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

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New("pgx", dsn,
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	SharedSuite(t, client)
}
