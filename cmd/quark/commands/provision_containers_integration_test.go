// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build integration
// +build integration

// Container fallback for the tenant provision integration test, mirroring the
// root package's containers_test.go: prefer QUARK_TEST_POSTGRES_DSN, else
// boot a throwaway postgres via testcontainers-go. Compiled only under
// `-tags=integration` so the default build stays container-free.

package commands

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func provisionTestPostgresDSN(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("QUARK_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
