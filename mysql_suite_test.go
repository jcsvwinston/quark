package quark_test

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/go-sql-driver/mysql"
)

func TestSuiteMySQL(t *testing.T) {
	dsn := os.Getenv("QUARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_MYSQL_DSN not set")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New(db,
		quark.WithDialect(quark.MySQL()),
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	SharedSuite(t, client)
}
