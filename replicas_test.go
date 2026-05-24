// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"io"
	"log/slog"
	"testing"

	_ "modernc.org/sqlite"
)

type rrUser struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (rrUser) TableName() string { return "rr_users" }

var rrQuiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// seedReplicaDB opens a throwaway client at dsn, migrates rr_users, and inserts
// one row with the given marker name. The returned client must stay open for
// the lifetime of the test so the shared-cache in-memory DB (which the main
// client also connects to) is not torn down. Distinct marker names per DB let
// a read reveal which database actually served it.
func seedReplicaDB(t *testing.T, dsn, marker string) *Client {
	t.Helper()
	rc, err := New("sqlite", dsn, WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("open replica seed client: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	ctx := context.Background()
	if err := rc.Migrate(ctx, &rrUser{}); err != nil {
		t.Fatalf("migrate replica: %v", err)
	}
	if err := For[rrUser](ctx, rc).Create(&rrUser{Name: marker}); err != nil {
		t.Fatalf("seed replica row: %v", err)
	}
	return rc
}

// TestReadReplicaRouting proves the ADR-0015 routing: reads round-robin to the
// configured replicas, writes go to the primary, and Sticky(ctx) pins reads to
// the primary. Each database is seeded with a distinct marker row so the row a
// read returns identifies the database that served it.
func TestReadReplicaRouting(t *testing.T) {
	ctx := context.Background()
	primaryDSN := "file:rr_primary?mode=memory&cache=shared"
	rep0DSN := "file:rr_rep0?mode=memory&cache=shared"
	rep1DSN := "file:rr_rep1?mode=memory&cache=shared"

	c, err := New("sqlite", primaryDSN,
		WithReplicas(rep0DSN, rep1DSN),
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Seed the two replica DBs with distinct markers, primary with its own.
	seedReplicaDB(t, rep0DSN, "rep0")
	seedReplicaDB(t, rep1DSN, "rep1")
	if err := c.Migrate(ctx, &rrUser{}); err != nil {
		t.Fatalf("migrate primary: %v", err)
	}
	if err := For[rrUser](ctx, c).Create(&rrUser{Name: "primary"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	read := func() string {
		t.Helper()
		rows, err := For[rrUser](ctx, c).List()
		if err != nil || len(rows) != 1 {
			t.Fatalf("read: %v (n=%d)", err, len(rows))
		}
		return rows[0].Name
	}

	// Reads round-robin across the two replicas (pickReplica: rep0, rep1, ...).
	if got := read(); got != "rep0" {
		t.Errorf("read 1 served by %q, want rep0 (should route to a replica)", got)
	}
	if got := read(); got != "rep1" {
		t.Errorf("read 2 served by %q, want rep1 (round-robin)", got)
	}
	if got := read(); got != "rep0" {
		t.Errorf("read 3 served by %q, want rep0 (round-robin wrap)", got)
	}

	// Sticky pins the read to the primary.
	stickyRows, err := For[rrUser](Sticky(ctx), c).List()
	if err != nil || len(stickyRows) != 1 {
		t.Fatalf("sticky read: %v (n=%d)", err, len(stickyRows))
	}
	if stickyRows[0].Name != "primary" {
		t.Errorf("sticky read served by %q, want primary", stickyRows[0].Name)
	}

	// A write goes to the primary: it must not appear on the replicas, and a
	// Sticky read of the primary must see two rows afterwards.
	if err := For[rrUser](ctx, c).Create(&rrUser{Name: "primary2"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	primaryRows, err := For[rrUser](Sticky(ctx), c).OrderBy("id", "ASC").List()
	if err != nil {
		t.Fatalf("sticky read after write: %v", err)
	}
	if len(primaryRows) != 2 {
		t.Fatalf("primary has %d rows after write, want 2 (write must hit primary)", len(primaryRows))
	}
}

// TestCreateBatchWritesToPrimary guards the subtle case the reviewer caught:
// CreateBatch's INSERT ... RETURNING reads rows back via the multi-row path
// (executeQuery's primitive), so it must be pinned to the primary, not routed
// to a replica. Seeding the replica with a sentinel row lets us assert the
// batch landed on the primary (count grows there) and not on the replica.
func TestCreateBatchWritesToPrimary(t *testing.T) {
	ctx := context.Background()
	primaryDSN := "file:rr_cb_primary?mode=memory&cache=shared"
	repDSN := "file:rr_cb_rep?mode=memory&cache=shared"

	c, err := New("sqlite", primaryDSN, WithReplicas(repDSN),
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	rc := seedReplicaDB(t, repDSN, "replica-sentinel") // replica starts with 1 row
	if err := c.Migrate(ctx, &rrUser{}); err != nil {
		t.Fatalf("migrate primary: %v", err)
	}

	// CreateBatch (INSERT ... RETURNING on SQLite) must hit the primary.
	batch := []*rrUser{{Name: "b1"}, {Name: "b2"}, {Name: "b3"}}
	if err := For[rrUser](ctx, c).CreateBatch(batch); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	for i, u := range batch {
		if u.ID == 0 {
			t.Errorf("batch[%d] got no PK back from RETURNING", i)
		}
	}

	// Primary must now hold the 3 inserted rows (Sticky pins the read to it).
	primaryRows, err := For[rrUser](Sticky(ctx), c).List()
	if err != nil {
		t.Fatalf("primary read: %v", err)
	}
	if len(primaryRows) != 3 {
		t.Fatalf("primary has %d rows, want 3 (CreateBatch must write to primary)", len(primaryRows))
	}
	// Replica must still hold only its sentinel — the batch did not leak to it.
	repRows, err := For[rrUser](ctx, rc).List()
	if err != nil {
		t.Fatalf("replica read: %v", err)
	}
	if len(repRows) != 1 || repRows[0].Name != "replica-sentinel" {
		t.Fatalf("replica has %d rows (%v), want 1 sentinel — CreateBatch leaked a write to the replica", len(repRows), repRows)
	}
}

// TestReadReplicaRoutingNoReplicas is the regression guard: with no replicas
// configured, reads use the primary exactly as before (single-DB behaviour).
func TestReadReplicaRoutingNoReplicas(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:rr_noreplica?mode=memory&cache=shared",
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if c.pickReplica() != nil {
		t.Fatal("pickReplica returned non-nil with no replicas configured")
	}
	if err := c.Migrate(ctx, &rrUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := For[rrUser](ctx, c).Create(&rrUser{Name: "only"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, err := For[rrUser](ctx, c).List()
	if err != nil || len(rows) != 1 || rows[0].Name != "only" {
		t.Fatalf("read with no replicas: %v rows=%v", err, rows)
	}
}
