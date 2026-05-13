// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build integration
// +build integration

// Container-backed integration helpers for the per-engine suite tests.
// Compiled only with `go test -tags=integration`; the default `go test
// -short` path stays SQLite-only and does not pull testcontainers-go
// into the binary.
//
// Each `setup<Engine>Container` returns a ready-to-use DSN string. The
// container is auto-cleaned through `testcontainers.CleanupContainer(t, …)`,
// which fires from `t.Cleanup` and works correctly even when subtests
// fan out.

package quark_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"
	"github.com/testcontainers/testcontainers-go/modules/mssql"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// --- DSN resolvers (env var → container fallback) ---
//
// Under `-tags=integration`, every `resolve<Engine>DSN` prefers an
// explicit env var when present so a CI matrix or developer that
// already has a database can short-circuit the container boot. When
// the env var is empty, the resolver spins up the container via the
// `setup<Engine>Container` helper and returns the freshly-minted DSN.

func resolvePostgresDSN(t *testing.T) string {
	if dsn := os.Getenv("QUARK_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return setupPostgresContainer(t)
}

func resolveMySQLDSN(t *testing.T) string {
	if dsn := os.Getenv("QUARK_TEST_MYSQL_DSN"); dsn != "" {
		return dsn
	}
	return setupMySQLContainer(t)
}

func resolveMariaDBDSN(t *testing.T) string {
	if dsn := os.Getenv("QUARK_TEST_MARIADB_DSN"); dsn != "" {
		return dsn
	}
	return setupMariaDBContainer(t)
}

func resolveMSSQLDSN(t *testing.T) string {
	if dsn := os.Getenv("QUARK_TEST_MSSQL_DSN"); dsn != "" {
		return dsn
	}
	return setupMSSQLContainer(t)
}

func resolveOracleDSN(t *testing.T) string {
	if dsn := os.Getenv("QUARK_TEST_ORACLE_DSN"); dsn != "" {
		return dsn
	}
	return setupOracleContainer(t)
}

// containerCtx returns a context with the standard startup timeout the
// integration helpers share. 5 minutes covers the worst case (Oracle
// pulling + initialising) on a cold runner.
func containerCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// setupPostgresContainer boots a PostgreSQL container and returns a DSN
// the pgx driver consumes. Uses the upstream `postgres:16-alpine` image
// — pinned tag, not `latest`, so CI runs are reproducible.
func setupPostgresContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := containerCtx()
	defer cancel()

	c, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("quark_test"),
		postgres.WithUsername("quark"),
		postgres.WithPassword("quark"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("postgres container: %v", err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	return dsn
}

// setupMySQLContainer boots a MySQL 8 container; returns a DSN the
// `go-sql-driver/mysql` driver consumes (no scheme).
func setupMySQLContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := containerCtx()
	defer cancel()

	c, err := mysql.Run(ctx,
		"mysql:8.0",
		mysql.WithDatabase("quark_test"),
		mysql.WithUsername("quark"),
		mysql.WithPassword("quark"),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("mysql container: %v", err)
	}
	dsn, err := c.ConnectionString(ctx, "parseTime=true", "multiStatements=true")
	if err != nil {
		t.Fatalf("mysql dsn: %v", err)
	}
	return dsn
}

// setupMariaDBContainer boots a MariaDB 11 container.
func setupMariaDBContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := containerCtx()
	defer cancel()

	c, err := mariadb.Run(ctx,
		"mariadb:11.4",
		mariadb.WithDatabase("quark_test"),
		mariadb.WithUsername("quark"),
		mariadb.WithPassword("quark"),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("mariadb container: %v", err)
	}
	dsn, err := c.ConnectionString(ctx, "parseTime=true", "multiStatements=true")
	if err != nil {
		t.Fatalf("mariadb dsn: %v", err)
	}
	return dsn
}

// setupMSSQLContainer boots a SQL Server 2022 container.
//
// The Microsoft image accepts the EULA only via env var, hence the
// explicit `WithAcceptEULA()`. The container starts with no extra
// database — `quark_test` is created on first use by the suite.
func setupMSSQLContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := containerCtx()
	defer cancel()

	c, err := mssql.Run(ctx,
		"mcr.microsoft.com/mssql/server:2022-latest",
		mssql.WithAcceptEULA(),
		mssql.WithPassword("Quark!2026"),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("mssql container: %v", err)
	}
	dsn, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mssql dsn: %v", err)
	}
	return dsn
}

// setupOracleContainer boots an Oracle Database 23 Free container (the
// successor to Oracle XE — same licensing model, smaller image). No
// dedicated testcontainers module exists for Oracle, so this uses the
// generic container runner with the gvenzl image.
//
// First-boot is slow (~90 s) because Oracle initialises the SYS schema
// from scratch. CI matrix should budget at least 4 minutes for the
// Oracle job.
func setupOracleContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := containerCtx()
	defer cancel()

	req := testcontainers.ContainerRequest{
		Image:        "gvenzl/oracle-free:23-slim-faststart",
		ExposedPorts: []string{"1521/tcp"},
		Env: map[string]string{
			"ORACLE_PASSWORD":        "quark",
			"APP_USER":               "quark",
			"APP_USER_PASSWORD":      "quark",
			"ORACLE_RANDOM_PASSWORD": "no",
		},
		WaitingFor: wait.ForLog("DATABASE IS READY TO USE!").
			WithStartupTimeout(4 * time.Minute),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("oracle container: %v", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("oracle host: %v", err)
	}
	port, err := c.MappedPort(ctx, "1521")
	if err != nil {
		t.Fatalf("oracle port: %v", err)
	}
	// go-ora DSN: oracle://user:pass@host:port/service
	return fmt.Sprintf("oracle://quark:quark@%s:%s/FREEPDB1", host, port.Port())
}
