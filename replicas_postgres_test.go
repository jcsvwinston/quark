// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build integration
// +build integration

package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type rrPGUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (rrPGUser) TableName() string { return "rr_pg_users" }

var rrPGQuiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestPostgresReplicaRouting exercises read-replica routing (F6-5, ADR-0015)
// against the real pgx driver, not just SQLite. It is NOT a streaming-replication
// test — Quark does not manage replication; it routes. To prove routing against
// a real engine without a physical standby, it provisions a SECOND database on
// the same server, seeds it with different data than the primary, and registers
// it as the "replica". A read returning the replica's data therefore proves the
// query was routed to the replica pool; Sticky proves it can be pinned back to
// the primary. This validates that readExec's *sql.DB identity check, the pgx
// pool, and the round-trip all behave as the SQLite unit tests assume.
//
// Runs under -tags=integration. CI wires it into the postgres matrix; it skips
// if a second database cannot be created (e.g. a restricted env-provided DSN).
func TestPostgresReplicaRouting(t *testing.T) {
	baseDSN := resolvePostgresDSN(t)
	if baseDSN == "" {
		t.Skip("QUARK_TEST_POSTGRES_DSN not set (rebuild with -tags=integration to spin up a container)")
	}

	const replicaDB = "quark_replica_rr"
	replicaDSN, err := swapPostgresDBName(baseDSN, replicaDB)
	if err != nil {
		t.Skipf("cannot derive replica DSN from %q: %v", baseDSN, err)
	}

	// Provision the replica database on the same server. CREATE DATABASE runs in
	// autocommit (database/sql Exec), so no explicit tx is needed.
	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		t.Fatalf("open admin connection: %v", err)
	}
	ctx := context.Background()
	// replicaDB is a fixed safe constant; quote it as a PG identifier anyway,
	// per the project's "always quote identifiers" norm (CLAUDE.md §6).
	dropDB := fmt.Sprintf("DROP DATABASE IF EXISTS %q", replicaDB)
	createDB := fmt.Sprintf("CREATE DATABASE %q", replicaDB)
	if _, err := admin.ExecContext(ctx, dropDB); err != nil {
		_ = admin.Close()
		t.Skipf("cannot drop pre-existing replica database (insufficient privileges?): %v", err)
	}
	if _, err := admin.ExecContext(ctx, createDB); err != nil {
		_ = admin.Close()
		t.Skipf("cannot create replica database (insufficient privileges?): %v", err)
	}
	t.Cleanup(func() {
		// Drop the throwaway database after all clients are closed. Best-effort:
		// the testcontainer is torn down anyway; this only matters for a
		// persistent env-provided server.
		if _, err := admin.ExecContext(context.Background(), dropDB); err != nil {
			t.Logf("cleanup: drop replica database: %v", err)
		}
		_ = admin.Close()
	})

	// Seed the replica with 3 rows (ids 1..3); seed the primary with 1 (id 1).
	rc, err := quark.New("pgx", replicaDSN, quark.WithLogger(rrPGQuiet))
	if err != nil {
		t.Fatalf("open replica client: %v", err)
	}
	if err := rc.Migrate(ctx, &rrPGUser{}); err != nil {
		t.Fatalf("migrate replica: %v", err)
	}
	for _, name := range []string{"rep1", "rep2", "rep3"} {
		if err := quark.For[rrPGUser](ctx, rc).Create(&rrPGUser{Name: name}); err != nil {
			t.Fatalf("seed replica row %q: %v", name, err)
		}
	}
	// The replica database persists independently of this connection, so it can
	// be closed; the primary client opens its own pool to it via WithReplicas.
	_ = rc.Close()

	c, err := quark.New("pgx", baseDSN,
		quark.WithReplicas(replicaDSN),
		quark.WithLogger(rrPGQuiet),
	)
	if err != nil {
		t.Fatalf("open primary client: %v", err)
	}
	t.Cleanup(func() {
		// Drop the table so a persistent env server stays clean between runs.
		_, _ = c.Raw().ExecContext(context.Background(), "DROP TABLE IF EXISTS rr_pg_users")
		_ = c.Close()
	})
	if err := c.Migrate(ctx, &rrPGUser{}); err != nil {
		t.Fatalf("migrate primary: %v", err)
	}
	if err := quark.For[rrPGUser](ctx, c).Create(&rrPGUser{Name: "primary"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	// Multi-row read routes to the replica (3 rows there, 1 on the primary).
	if rows, err := quark.For[rrPGUser](ctx, c).OrderBy("id", "ASC").List(); err != nil || len(rows) != 3 {
		t.Fatalf("List = %d rows (err %v), want 3 (routed to replica)", len(rows), err)
	}
	// Sticky pins the read to the primary.
	if rows, err := quark.For[rrPGUser](quark.Sticky(ctx), c).List(); err != nil || len(rows) != 1 {
		t.Fatalf("Sticky List = %d rows (err %v), want 1 (primary)", len(rows), err)
	}

	// Single-row reads route too (ADR-0015 follow-up).
	if n, err := quark.For[rrPGUser](ctx, c).Count(); err != nil || n != 3 {
		t.Fatalf("Count = %d (err %v), want 3 (routed to replica)", n, err)
	}
	if n, err := quark.For[rrPGUser](quark.Sticky(ctx), c).Count(); err != nil || n != 1 {
		t.Fatalf("Sticky Count = %d (err %v), want 1 (primary)", n, err)
	}
	if m, err := quark.For[rrPGUser](ctx, c).Max("id"); err != nil || m != 3 {
		t.Fatalf("Max(id) = %v (err %v), want 3 (routed to replica)", m, err)
	}

	// A write goes to the primary: the replica count is unchanged afterwards.
	if err := quark.For[rrPGUser](ctx, c).Create(&rrPGUser{Name: "primary2"}); err != nil {
		t.Fatalf("write to primary: %v", err)
	}
	if n, err := quark.For[rrPGUser](ctx, c).Count(); err != nil || n != 3 {
		t.Fatalf("post-write replica Count = %d (err %v), want 3 (write must not hit replica)", n, err)
	}
	if n, err := quark.For[rrPGUser](quark.Sticky(ctx), c).Count(); err != nil || n != 2 {
		t.Fatalf("post-write primary Count = %d (err %v), want 2 (write hit primary)", n, err)
	}
}

// swapPostgresDBName rewrites the database name in a postgres URL DSN
// (postgres://user:pass@host:port/dbname?opts). Returns an error for a non-URL
// (keyword) DSN, which the caller treats as a skip.
func swapPostgresDBName(dsn, db string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", &url.Error{Op: "parse", URL: dsn, Err: errNotURLDSN}
	}
	u.Path = "/" + db
	return u.String(), nil
}

var errNotURLDSN = errString("not a postgres URL DSN")

type errString string

func (e errString) Error() string { return string(e) }
