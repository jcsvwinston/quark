// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// engine is one database the bench measures on, the environment variable that
// hands it a DSN, how to build its data set, and its operations.
type engine struct {
	name    string
	env     string
	driver  string // the database/sql driver name the bench's own connection uses
	version string // the query that names the server's version, for the table
	setup   func(tb testing.TB, dsn string)
	ops     func() []*operation
}

func engines() []engine {
	return []engine{
		{"postgres", "QUARK_BENCH_POSTGRES_DSN", "pgx", "SHOW server_version", setupPostgres, pgOperations},
		{"mysql", "QUARK_BENCH_MYSQL_DSN", "mysql", "SELECT VERSION()", setupMySQL, myOperations},
	}
}

// allOperations is every operation of every engine, for the catalogue checks
// that need no database.
func allOperations() []*operation {
	var out []*operation
	for _, e := range engines() {
		out = append(out, e.ops()...)
	}
	return out
}

// dsnFor returns the engine's DSN, or skips. QUARK_BENCH_REQUIRE names the
// engines that MUST be present — run.sh and the CI lane set it, so an engine
// that failed to start fails the run instead of leaving its controls skipped
// and the lane green with nothing measured.
func dsnFor(tb testing.TB, e engine) string {
	tb.Helper()
	dsn := os.Getenv(e.env)
	if dsn != "" {
		return dsn
	}
	for _, req := range strings.Split(os.Getenv("QUARK_BENCH_REQUIRE"), ",") {
		if strings.TrimSpace(req) == e.name {
			tb.Fatalf("%s is required (QUARK_BENCH_REQUIRE) but %s is not set", e.name, e.env)
		}
	}
	tb.Skipf("%s not set: the %s controls need a real engine. Run benchmarks/engines/run.sh, which starts one", e.env, e.name)
	return ""
}

// machine names what the bench ran on, for the table: a ratio is portable
// across machines only within the tolerances, so the reader needs to know
// which machine a published run came from.
func machine() string {
	desc := fmt.Sprintf("%s/%s, %d CPUs, %s", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version())
	if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
				return strings.TrimSpace(v) + ", " + desc
			}
		}
	}
	return desc
}

func mustOpen(tb testing.TB, driver, dsn string) *sql.DB {
	tb.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		tb.Fatalf("sql.Open(%s): %v", driver, err)
	}
	return db
}

// waitReady pings until the engine answers. run.sh already waits for both
// engines on TCP, so this is for a DSN handed in by hand, at a server that is
// still starting.
func waitReady(tb testing.TB, db *sql.DB, name string) {
	tb.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := db.PingContext(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("%s did not answer within two minutes: %v", name, err)
		}
		time.Sleep(time.Second)
	}
}
