package quark_test

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/sijms/go-ora/v2"
)

func TestSuiteOracle(t *testing.T) {
	dsn := os.Getenv("QUARK_TEST_ORACLE_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_ORACLE_DSN not set")
	}

	db, err := sql.Open("oracle", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New(db,
		quark.WithDialect(quark.Oracle()),
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	SharedSuite(t, client)
}
