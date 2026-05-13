package quark_test

import (
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/microsoft/go-mssqldb"
)

func TestSuiteMSSQL(t *testing.T) {
	dsn := resolveMSSQLDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_MSSQL_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	// Create database if not exists
	tempDB, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tempDB.Exec("IF NOT EXISTS (SELECT * FROM sys.databases WHERE name = 'quark_test') CREATE DATABASE quark_test")
	tempDB.Close()

	// Reconnect to the test database
	if !strings.Contains(dsn, "database=") {
		if strings.Contains(dsn, "?") {
			dsn += "&database=quark_test"
		} else {
			dsn += ";database=quark_test"
		}
	} else {
		dsn = strings.Replace(dsn, "database=master", "database=quark_test", 1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New("sqlserver", dsn,
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	SharedSuite(t, client)
}
