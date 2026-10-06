package enginesuite

import (
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"
)

func TestSuiteMSSQL(t *testing.T) {
	dsn := mssqlSuiteDSN(t)

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New("sqlserver", dsn,
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runConformance(t, client, "sqlserver")
	SharedSuite(t, client)
}

// mssqlSuiteDSN resolves the SQL Server DSN, creates the quark_test database
// when it is missing, and returns the DSN pointed at it — or skips.
func mssqlSuiteDSN(t *testing.T) string {
	t.Helper()
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
	return dsn
}
