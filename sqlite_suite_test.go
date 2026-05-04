package quark_test

import (
	"database/sql"
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "modernc.org/sqlite"
)

func TestSuiteSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", "file:suitesqlite?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New(db,
		quark.WithDialect(quark.SQLite()),
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	SharedSuite(t, client)
}
