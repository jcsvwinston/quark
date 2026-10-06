// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"
)

// The whole SharedSuite again, with WithStatementCache on (QK-37,
// ADR-0027), on the engines the cache acts on — MySQL, MariaDB, SQL Server
// and SQLite; PostgreSQL and Oracle ignore the option: every read and write
// the query builder sends, through cached statements. The cache is small on
// purpose, so statements are evicted all through the run and not only kept.
// Each runs in its engine's lane next to the suite without the cache.

func runSuiteWithStatementCache(t *testing.T, driver, dsn, conformance string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client, err := quark.New(driver, dsn,
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
		quark.WithStatementCache(16),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	runConformance(t, client, conformance)
	SharedSuite(t, client)
	// SharedSuite's cache cases run on a client WithOptions derives, which
	// does not keep the statement cache: run them again with it (QK-66).
	t.Run("CacheWritePathsWithStatementCache", func(t *testing.T) {
		testCacheWritePaths(context.Background(), t, client, quark.WithStatementCache(16))
	})
}

func TestSuiteMySQLStatementCache(t *testing.T) {
	dsn := resolveMySQLDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_MYSQL_DSN not set (rebuild with -tags=integration to spin up a container)")
	}
	runSuiteWithStatementCache(t, "mysql", dsn, "mysql")
}

func TestSuiteMariaDBStatementCache(t *testing.T) {
	dsn := resolveMariaDBDSN(t)
	if dsn == "" {
		t.Skip("QUARK_TEST_MARIADB_DSN not set (rebuild with -tags=integration to spin up a container)")
	}
	runSuiteWithStatementCache(t, "mysql", dsn, "mysql")
}

func TestSuiteMSSQLStatementCache(t *testing.T) {
	runSuiteWithStatementCache(t, "sqlserver", mssqlSuiteDSN(t), "sqlserver")
}

func TestSuiteSQLiteStatementCache(t *testing.T) {
	runSuiteWithStatementCache(t, "sqlite", "file:suitesqlitestmtcache?mode=memory&cache=shared", "sqlite")
}
