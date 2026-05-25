// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// TestReplicaFailoverToPrimary proves F6-6: a read routed to a replica that has
// gone down (its pool is closed → a transient connection error) fails over to
// the primary transparently, and the replica is taken out of rotation so later
// reads skip it.
func TestReplicaFailoverToPrimary(t *testing.T) {
	ctx := context.Background()
	primaryDSN := "file:rr_fo_primary?mode=memory&cache=shared"
	repDSN := "file:rr_fo_rep?mode=memory&cache=shared"

	c, err := New("sqlite", primaryDSN, WithReplicas(repDSN),
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	seedReplicaDB(t, repDSN, "replica") // replica answers reads while healthy
	if err := c.Migrate(ctx, &rrUser{}); err != nil {
		t.Fatalf("migrate primary: %v", err)
	}
	if err := For[rrUser](ctx, c).Create(&rrUser{Name: "primary"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	// Healthy: the read is served by the replica.
	if rows, _ := For[rrUser](ctx, c).List(); len(rows) != 1 || rows[0].Name != "replica" {
		t.Fatalf("pre-failover read = %v, want [replica]", rows)
	}

	// Simulate the replica going down by closing its pool.
	if err := c.replicas[0].Close(); err != nil {
		t.Fatalf("close replica: %v", err)
	}

	// The next read routes to the (down) replica, fails transiently, and fails
	// over to the primary — returning the primary's row, not an error.
	rows, err := For[rrUser](ctx, c).List()
	if err != nil {
		t.Fatalf("read after replica down should have failed over, got error: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "primary" {
		t.Fatalf("failover read = %v, want [primary]", rows)
	}

	// The replica is now out of rotation: pickReplica skips it (only one
	// replica, so it returns nil → reads use the primary).
	if c.pickReplica() != nil {
		t.Error("downed replica was not taken out of rotation")
	}
}

// TestReplicaHealthRecovery checks the passive-recovery side of F6-6: a replica
// marked down is skipped until its cooldown expires, then becomes eligible
// again. Uses a tiny cooldown to keep the test fast.
func TestReplicaHealthRecovery(t *testing.T) {
	repDSN := "file:rr_rec_rep?mode=memory&cache=shared"
	seedReplicaDB(t, repDSN, "rep")
	c, err := New("sqlite", "file:rr_rec_primary?mode=memory&cache=shared",
		WithReplicas(repDSN), WithReplicaDownCooldown(50*time.Millisecond),
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if c.pickReplica() == nil {
		t.Fatal("healthy replica should be pickable")
	}
	c.markReplicaDown(c.replicas[0])
	if c.pickReplica() != nil {
		t.Fatal("replica should be skipped while in cooldown")
	}
	time.Sleep(70 * time.Millisecond)
	if c.pickReplica() == nil {
		t.Error("replica should be eligible again after cooldown expired")
	}
}

// TestIsTransientConnErr pins the classifier that drives failover: connection
// failures are transient (retry/failover), query/logic errors are not.
func TestIsTransientConnErr(t *testing.T) {
	transient := []struct {
		name string
		err  error
	}{
		{"ErrBadConn", driver.ErrBadConn},
		{"wrapped ErrBadConn", fmt.Errorf("query failed: %w", driver.ErrBadConn)},
		{"ErrConnDone", sql.ErrConnDone},
		{"net error", &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
		{"pg class 08", &pgconn.PgError{Code: "08006"}},
		{"pg admin shutdown", &pgconn.PgError{Code: "57P01"}},
		{"sqlite closed", errors.New("sql: database is closed")},
	}
	for _, tc := range transient {
		if !isTransientConnErr(tc.err) {
			t.Errorf("%s: isTransientConnErr = false, want true", tc.name)
		}
	}

	notTransient := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"ErrNoRows", sql.ErrNoRows},
		{"plain error", errors.New("syntax error near FROM")},
		{"pg unique violation", &pgconn.PgError{Code: "23505"}},
		// context.DeadlineExceeded implements net.Error — must NOT be treated
		// as a transient connection failure (it is the caller's timeout, not a
		// downed replica), or a slow query would wrongly evict a healthy replica.
		{"context deadline", context.DeadlineExceeded},
		{"context canceled", context.Canceled},
		{"wrapped context deadline", fmt.Errorf("query failed: %w", context.DeadlineExceeded)},
	}
	for _, tc := range notTransient {
		if isTransientConnErr(tc.err) {
			t.Errorf("%s: isTransientConnErr = true, want false", tc.name)
		}
	}
}

// TestReplicaStrategyRandom checks ReplicaRandom spreads picks across all
// healthy replicas and never returns one that is in cooldown.
func TestReplicaStrategyRandom(t *testing.T) {
	rep0 := "file:rr_rnd_rep0?mode=memory&cache=shared"
	rep1 := "file:rr_rnd_rep1?mode=memory&cache=shared"
	seedReplicaDB(t, rep0, "rep0")
	seedReplicaDB(t, rep1, "rep1")
	c, err := New("sqlite", "file:rr_rnd_primary?mode=memory&cache=shared",
		WithReplicas(rep0, rep1), WithReplicaStrategy(ReplicaRandom),
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Over many picks, a uniform-random strategy hits both replicas.
	seen := map[*sql.DB]int{}
	for i := 0; i < 200; i++ {
		r := c.pickReplica()
		if r == nil {
			t.Fatal("random returned nil with two healthy replicas")
		}
		seen[r]++
	}
	if len(seen) != 2 {
		t.Fatalf("random picked %d distinct replicas over 200 tries, want 2", len(seen))
	}

	// A replica in cooldown is never chosen.
	c.markReplicaDown(c.replicas[0])
	for i := 0; i < 100; i++ {
		if c.pickReplica() == c.replicas[0] {
			t.Fatal("random picked a replica that is in cooldown")
		}
	}
}

// TestReplicaStrategyLeastConn checks ReplicaLeastConn steers a read to the
// replica with the fewest in-use connections, and falls back to the only
// healthy replica when the idle one is in cooldown.
func TestReplicaStrategyLeastConn(t *testing.T) {
	rep0 := "file:rr_lc_rep0?mode=memory&cache=shared"
	rep1 := "file:rr_lc_rep1?mode=memory&cache=shared"
	seedReplicaDB(t, rep0, "rep0")
	seedReplicaDB(t, rep1, "rep1")
	c, err := New("sqlite", "file:rr_lc_primary?mode=memory&cache=shared",
		WithReplicas(rep0, rep1), WithReplicaStrategy(ReplicaLeastConn),
		WithMaxOpenConns(2), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Hold a connection on replica 0 so its InUse count is higher than replica 1's.
	conn, err := c.replicas[0].Conn(context.Background())
	if err != nil {
		t.Fatalf("grab conn on replica 0: %v", err)
	}
	defer conn.Close()

	if got := c.pickReplica(); got != c.replicas[1] {
		t.Fatalf("least-conn picked the busier replica; want the idle replica[1]")
	}

	// With the idle replica in cooldown, least-conn falls back to the only
	// healthy replica even though it is busier.
	c.markReplicaDown(c.replicas[1])
	if got := c.pickReplica(); got != c.replicas[0] {
		t.Fatalf("least-conn did not fall back to the only healthy replica")
	}
}

// TestSingleRowReadsRouteToReplica proves the ADR-0015 follow-up: single-row
// reads (Count and the aggregates) now route to a replica like multi-row reads,
// while Sticky still pins them to the primary. The replica is seeded with more
// rows than the primary so the value a read returns identifies which database
// served it.
func TestSingleRowReadsRouteToReplica(t *testing.T) {
	ctx := context.Background()
	primaryDSN := "file:rr_srr_primary?mode=memory&cache=shared"
	repDSN := "file:rr_srr_rep?mode=memory&cache=shared"

	c, err := New("sqlite", primaryDSN, WithReplicas(repDSN),
		WithMaxOpenConns(1), WithLogger(rrQuiet))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Replica: 3 rows (ids 1..3). Primary: 1 row (id 1).
	rc := seedReplicaDB(t, repDSN, "rep1")
	if err := For[rrUser](ctx, rc).Create(&rrUser{Name: "rep2"}); err != nil {
		t.Fatalf("seed replica row 2: %v", err)
	}
	if err := For[rrUser](ctx, rc).Create(&rrUser{Name: "rep3"}); err != nil {
		t.Fatalf("seed replica row 3: %v", err)
	}
	if err := c.Migrate(ctx, &rrUser{}); err != nil {
		t.Fatalf("migrate primary: %v", err)
	}
	if err := For[rrUser](ctx, c).Create(&rrUser{Name: "primary"}); err != nil {
		t.Fatalf("seed primary: %v", err)
	}

	// Count routes to the replica (3), Sticky pins it to the primary (1).
	if n, err := For[rrUser](ctx, c).Count(); err != nil || n != 3 {
		t.Fatalf("Count = %d (err %v), want 3 (routed to replica)", n, err)
	}
	if n, err := For[rrUser](Sticky(ctx), c).Count(); err != nil || n != 1 {
		t.Fatalf("Sticky Count = %d (err %v), want 1 (primary)", n, err)
	}

	// Aggregates route too: MAX(id) is 3 on the replica, 1 on the primary.
	if m, err := For[rrUser](ctx, c).Max("id"); err != nil || m != 3 {
		t.Fatalf("Max(id) = %v (err %v), want 3 (routed to replica)", m, err)
	}
	if m, err := For[rrUser](Sticky(ctx), c).Max("id"); err != nil || m != 1 {
		t.Fatalf("Sticky Max(id) = %v (err %v), want 1 (primary)", m, err)
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
