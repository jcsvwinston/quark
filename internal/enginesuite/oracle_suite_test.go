package enginesuite

import (
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"
)

func TestSuiteOracle(t *testing.T) {
	dsn := resolveOracleDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_ORACLE_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New("oracle", dsn,
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	SharedSuite(t, client)
}
