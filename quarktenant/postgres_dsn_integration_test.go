// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build integration
// +build integration

package quarktenant_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// postgresDSN under -tags=integration prefers QUARK_TEST_POSTGRES_DSN
// when set, falling back to a freshly-booted postgres:16-alpine
// container managed by testcontainers-go. The container is
// auto-cleaned via t.Cleanup; subtests share the parent test's
// container.
//
// This file mirrors the testcontainers helper in the root package
// (containers_test.go). Duplicating ~30 lines is the price of staying
// inside the quarktenant_test package — exporting a shared helper
// would require either a global util package or relaxing the test
// package boundary.
func postgresDSN(t *testing.T) string {
	if dsn := os.Getenv("QUARK_TEST_POSTGRES_DSN"); dsn != "" {
		return dsn
	}
	return setupPostgresContainer(t)
}

func setupPostgresContainer(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
