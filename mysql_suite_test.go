package quark_test

import (
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/go-sql-driver/mysql"
)

// TestSuiteMySQL runs the full SharedSuite against MySQL. DSN precedence
// follows the F0-8 pattern: env var first, then container fallback under
// `-tags=integration` (see containers_test.go).
func TestSuiteMySQL(t *testing.T) {
	dsn := resolveMySQLDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_MYSQL_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New("mysql", dsn,
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	SharedSuite(t, client)
}
