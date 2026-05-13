package quark_test

import (
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// resolvePostgresDSN returns a DSN for the Postgres suite. Precedence:
//  1. QUARK_TEST_POSTGRES_DSN env var (CI matrix uses this against a
//     pre-provisioned engine when available).
//  2. testcontainers fallback (only compiled under `-tags=integration`;
//     see containers_test.go).
//
// Default `go test -short` builds neither path → the suite skips.
func TestSuitePostgres(t *testing.T) {
	dsn := resolvePostgresDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
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
