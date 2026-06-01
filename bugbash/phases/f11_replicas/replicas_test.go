// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f11_replicas is bug-bash phase F11: read/write split, sticky reads,
// transactional reads, replica strategy spread, and failover/cooldown.
//
// The phase stands up its OWN PostgreSQL topology — 1 primary + 3 replicas, all
// independent instances (no streaming replication) — because the one-container-
// per-engine bug-bash harness does not model a primary/replica fleet. Routing is
// proven by data presence rather than by scraping the OTel db.host label, which
// is a stronger signal: a row written only to the primary is absent from every
// replica, so
//
//   - a non-sticky read of that row returns 0  → it was served by a replica;
//   - a Sticky / in-Tx read returns 1           → it was served by the primary;
//   - with every replica stopped, a non-sticky read returns 1 → it transparently
//     failed over to the primary (query_exec.go markReplicaDown + retry-on-primary).
//
// PostgreSQL only (the spec's engine); replica routing itself is DSN-based and
// engine-agnostic, but the topology orchestration here is Postgres-specific.
// Requires Docker — the test skips with a logged reason when Docker is absent,
// so the default `-engines=sqlite` run is a clean no-op.
//
// Out of scope (logged): real streaming replication and replica lag (the
// data-presence model needs neither), and the spec's explicit OTel db.host
// metric assertion (covered by proxy — data presence proves the routing).
package f11_replicas

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	phase        = "f11_replicas"
	pgImage      = "postgres:16-alpine"
	primaryName  = "bugbash-f11-primary"
	primaryPort  = "55440"
	numReplicas  = 3
	replicaPort0 = 55441 // replicas use 55441, 55442, 55443
)

type rep struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type rec struct {
	t   *testing.T
	cat reporter.Category
}

func newRec(t *testing.T, cat reporter.Category) rec { return rec{t: t, cat: cat} }

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: name, Engine: "postgres", Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestReplicas ./phases/f11_replicas/...",
		},
	})
}

// pgInstance is one container in the topology.
type pgInstance struct {
	name string
	dsn  string
}

func TestReplicas(t *testing.T) {
	if !dockerAvailable() {
		// Not a t.Skip (CLAUDE.md rule 7): log and return so the default
		// -engines=sqlite bug-bash run is a clean no-op. Docker is a hard
		// prerequisite for the primary+replica topology this phase stands up.
		t.Log("F11 needs Docker for its primary+replicas Postgres topology; none reachable — skipped (logged)")
		return
	}
	ctx := context.Background()

	primary, replicas, teardown := bootTopology(t, ctx)
	defer teardown()

	replicaDSNs := make([]string, len(replicas))
	for i, r := range replicas {
		replicaDSNs[i] = r.dsn
	}

	// Schema on the primary and on every replica (independent instances), so a
	// SELECT on a replica returns rows/0 rather than "relation does not exist".
	migrate(t, ctx, primary.dsn)
	for _, r := range replicas {
		migrate(t, ctx, r.dsn)
	}

	// Main client: reads route to replicas (round-robin default), writes to the
	// primary. Short cooldown so the failover sub-tests don't wait 5s.
	client, err := quark.New("pgx", primary.dsn,
		quark.WithReplicas(replicaDSNs...),
		quark.WithReplicaDownCooldown(500*time.Millisecond))
	if err != nil {
		t.Fatalf("quark.New with replicas: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	// Shared fixture: wX exists only on the primary (writes route there). Fatal
	// on failure — every read sub-test asserts against wX, so a failed setup
	// would otherwise cascade into false P1 findings in Sticky/Tx/Failover.
	if err := quark.For[rep](ctx, client).Create(&rep{Name: "wX"}); err != nil {
		t.Fatalf("seed primary fixture wX: %v", err)
	}

	// Sub-tests run in order: the failover group stops containers, so it must
	// come last (PrimaryDownWritesFail kills the primary outright).
	t.Run("ReadWriteSplit", func(t *testing.T) { readWriteSplit(t, ctx, client) })
	t.Run("StickyReadsPrimary", func(t *testing.T) { stickyReadsPrimary(t, ctx, client) })
	t.Run("TxReadsPrimary", func(t *testing.T) { txReadsPrimary(t, ctx, client) })
	t.Run("RoundRobinSpread", func(t *testing.T) { roundRobinSpread(t, ctx, client, replicaDSNs) })
	t.Run("FailoverOneReplicaDown", func(t *testing.T) { failoverOneReplicaDown(t, ctx, client, replicas) })
	t.Run("FailoverAllReplicasDown", func(t *testing.T) { failoverAllReplicasDown(t, ctx, client, replicas) })
	t.Run("PrimaryDownWritesFail", func(t *testing.T) { primaryDownWritesFail(t, ctx, client, primary) })
}

// ── topology ────────────────────────────────────────────────────────────────

func bootTopology(t *testing.T, ctx context.Context) (pgInstance, []pgInstance, func()) {
	t.Helper()
	var all []string
	teardown := func() {
		for _, n := range all {
			_ = exec.Command("docker", "rm", "-f", n).Run()
		}
	}

	primary := pgInstance{name: primaryName, dsn: pgDSN(primaryPort)}
	if err := runPG(primaryName, primaryPort); err != nil {
		teardown()
		t.Fatalf("boot primary: %v", err)
	}
	all = append(all, primaryName)

	replicas := make([]pgInstance, numReplicas)
	for i := 0; i < numReplicas; i++ {
		name := fmt.Sprintf("bugbash-f11-replica-%d", i)
		port := fmt.Sprintf("%d", replicaPort0+i)
		if err := runPG(name, port); err != nil {
			teardown()
			t.Fatalf("boot replica %d: %v", i, err)
		}
		all = append(all, name)
		replicas[i] = pgInstance{name: name, dsn: pgDSN(port)}
	}

	// Wait for every instance to accept connections.
	for _, inst := range append([]pgInstance{primary}, replicas...) {
		if err := waitReady(ctx, inst.dsn, 60*time.Second); err != nil {
			teardown()
			t.Fatalf("instance %s never became ready: %v", inst.name, err)
		}
	}
	return primary, replicas, teardown
}

func pgDSN(port string) string {
	return "postgres://postgres:quark@localhost:" + port + "/postgres?sslmode=disable"
}

func runPG(name, hostPort string) error {
	_ = exec.Command("docker", "rm", "-f", name).Run() // clear any stopped leftover
	args := []string{"run", "-d", "--name", name, "-p", hostPort + ":5432",
		"-e", "POSTGRES_PASSWORD=quark", pgImage}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func dockerAvailable() bool { return exec.Command("docker", "info").Run() == nil }

func waitReady(ctx context.Context, dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		db, err := sql.Open("pgx", dsn)
		if err == nil {
			db.SetMaxOpenConns(1)
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			lastErr = db.PingContext(pingCtx)
			cancel()
			_ = db.Close()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
}

// migrate creates the rep table on the given instance via its own client.
func migrate(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	c, err := quark.New("pgx", dsn)
	if err != nil {
		t.Fatalf("migrate client %s: %v", dsn, err)
	}
	defer c.Close()
	if err := c.Migrate(ctx, &rep{}); err != nil {
		t.Fatalf("migrate rep on %s: %v", dsn, err)
	}
}

// seedReplica inserts n rows named "rr" directly into one replica instance,
// bypassing the primary, to give each replica a distinct row count for the
// round-robin spread check.
func seedReplica(t *testing.T, ctx context.Context, dsn string, n int) {
	t.Helper()
	c, err := quark.New("pgx", dsn)
	if err != nil {
		t.Fatalf("seed client: %v", err)
	}
	defer c.Close()
	for i := 0; i < n; i++ {
		if err := quark.For[rep](ctx, c).Create(&rep{Name: "rr"}); err != nil {
			t.Fatalf("seed replica row: %v", err)
		}
	}
}

// ── sub-tests ─────────────────────────────────────────────────────────────

// readWriteSplit: a row written to the primary is invisible to a non-sticky
// read (served by an empty replica) — proving reads route off the primary while
// the write landed on it.
func readWriteSplit(t *testing.T, ctx context.Context, c *quark.Client) {
	r := newRec(t, reporter.CategoryRegression)
	// wX was written to the primary by the parent fixture. A non-sticky read
	// routes to a replica, which never received it.
	got, err := quark.For[rep](ctx, c).Where("name", "=", "wX").Count()
	if err != nil {
		r.fail("ReadWriteSplit", reporter.SeverityP1, "non-sticky read: %v", err)
		return
	}
	if got != 0 {
		r.fail("ReadWriteSplit", reporter.SeverityP1,
			"non-sticky read saw %d wX rows, want 0 (read should route to a replica, not the primary)", got)
	}
}

// stickyReadsPrimary: Sticky(ctx) pins the read to the primary, which has wX.
func stickyReadsPrimary(t *testing.T, ctx context.Context, c *quark.Client) {
	r := newRec(t, reporter.CategoryRegression)
	got, err := quark.For[rep](quark.Sticky(ctx), c).Where("name", "=", "wX").Count()
	if err != nil {
		r.fail("StickyReadsPrimary", reporter.SeverityP1, "sticky read: %v", err)
		return
	}
	if got != 1 {
		r.fail("StickyReadsPrimary", reporter.SeverityP1,
			"sticky read saw %d wX rows, want 1 (Sticky must route to the primary)", got)
	}
}

// txReadsPrimary: a read inside Client.Tx uses the tx connection (primary).
func txReadsPrimary(t *testing.T, ctx context.Context, c *quark.Client) {
	r := newRec(t, reporter.CategoryRegression)
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		got, err := quark.ForTx[rep](ctx, tx).Where("name", "=", "wX").Count()
		if err != nil {
			return err
		}
		if got != 1 {
			r.fail("TxReadsPrimary", reporter.SeverityP1,
				"in-tx read saw %d wX rows, want 1 (tx reads must use the primary)", got)
		}
		return nil
	})
	if err != nil {
		r.fail("TxReadsPrimary", reporter.SeverityP1, "tx: %v", err)
	}
}

// roundRobinSpread: seed each replica with a distinct row count (1, 2, 3) and
// confirm non-sticky reads are spread across them — more than one distinct count
// is observed over many reads (round-robin, the default strategy).
func roundRobinSpread(t *testing.T, ctx context.Context, c *quark.Client, replicaDSNs []string) {
	r := newRec(t, reporter.CategoryRegression)
	for i, dsn := range replicaDSNs {
		seedReplica(t, ctx, dsn, i+1) // replica i gets i+1 "rr" rows
	}
	seen := map[int64]bool{}
	for i := 0; i < 10*len(replicaDSNs); i++ {
		got, err := quark.For[rep](ctx, c).Where("name", "=", "rr").Count()
		if err != nil {
			r.fail("RoundRobinSpread", reporter.SeverityP1, "spread read %d: %v", i, err)
			return
		}
		if got == 0 {
			// Replicas hold 1/2/3 rr rows; the primary holds 0. A 0 means the
			// non-sticky read hit the primary — replica routing fell back.
			r.fail("RoundRobinSpread", reporter.SeverityP1,
				"non-sticky read %d returned 0 rr rows → it hit the primary (which has none); replica routing fell back unexpectedly", i)
			return
		}
		seen[got] = true
	}
	if len(seen) < 2 {
		r.fail("RoundRobinSpread", reporter.SeverityP1,
			"non-sticky reads observed only counts %v across replicas, want spread over >1 replica (round-robin not distributing)", seen)
	}
}

// failoverOneReplicaDown: stop one replica; non-sticky reads must keep
// succeeding — the read routed to the dead replica fails over to the primary,
// and the cooldown takes it out of rotation so later reads skip it.
func failoverOneReplicaDown(t *testing.T, ctx context.Context, c *quark.Client, replicas []pgInstance) {
	r := newRec(t, reporter.CategoryRegression)
	if err := exec.Command("docker", "stop", replicas[0].name).Run(); err != nil {
		r.fail("FailoverOneReplicaDown", reporter.SeverityP1, "docker stop %s: %v", replicas[0].name, err)
		return
	}
	for i := 0; i < 20; i++ {
		if _, err := quark.For[rep](ctx, c).Where("name", "=", "wX").Count(); err != nil {
			r.fail("FailoverOneReplicaDown", reporter.SeverityP1,
				"read %d errored with one replica down (must fail over, not surface the error): %v", i, err)
			return
		}
	}
}

// failoverAllReplicasDown: stop every replica; a non-sticky read must fall over
// to the primary and therefore SEE wX (count 1), with no error.
func failoverAllReplicasDown(t *testing.T, ctx context.Context, c *quark.Client, replicas []pgInstance) {
	r := newRec(t, reporter.CategoryRegression)
	for _, rep := range replicas {
		_ = exec.Command("docker", "stop", rep.name).Run()
	}
	// A couple of reads to let any not-yet-marked replica trip and fail over.
	var got int64
	var err error
	for i := 0; i < 5; i++ {
		got, err = quark.For[rep](ctx, c).Where("name", "=", "wX").Count()
		if err != nil {
			r.fail("FailoverAllReplicasDown", reporter.SeverityP1, "read %d with all replicas down: %v", i, err)
			return
		}
	}
	if got != 1 {
		r.fail("FailoverAllReplicasDown", reporter.SeverityP1,
			"with all replicas down a non-sticky read saw %d wX rows, want 1 (must fail over to the primary)", got)
	}
}

// primaryDownWritesFail: stop the primary; a write must fail. There is no
// primary→replica failover by design (ADR-0015). Runs last — it kills the
// primary.
func primaryDownWritesFail(t *testing.T, ctx context.Context, c *quark.Client, primary pgInstance) {
	r := newRec(t, reporter.CategoryRegression)
	if err := exec.Command("docker", "stop", primary.name).Run(); err != nil {
		r.fail("PrimaryDownWritesFail", reporter.SeverityP1, "docker stop primary: %v", err)
		return
	}
	if err := quark.For[rep](ctx, c).Create(&rep{Name: "afterPrimaryDown"}); err == nil {
		r.fail("PrimaryDownWritesFail", reporter.SeverityP1,
			"write succeeded with the primary down, want an error (no primary→replica failover by design)")
	}
}
