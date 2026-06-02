// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f06_migrations is bug-bash phase F6: the schema-as-code cycle, per
// engine.
//
//   - PlanAndApply: PlanMigration is empty when the model matches the schema;
//     adding a column makes it detect the exact diff (OpAddColumn); ApplyPlan
//     applies it and the next PlanMigration is empty again.
//   - BackfillResumes: Backfill fills a new column by PK in batches; a run whose
//     Process fails mid-way leaves a resume token, and a re-run completes from
//     the last successful PK (the F3-4-resumable guarantee — stands in for the
//     spec's kill -9).
//   - VersionedUpDown: a migration registered in the global registry applies via
//     Migrator.Up and reverts via Migrator.Down.
//   - LockSerializes: two goroutines contend for AcquireMigrationLock; the second
//     gets ErrLockTimeout while the first holds it, then succeeds once released.
//     SQLite has no cross-process lock (single writer) → ErrUnsupportedFeature,
//     logged and skipped.
//
// Each sub-test owns its own table(s) and drops them first, so the schema
// mutations don't collide on a shared server-engine database.
//
// Note: TASKS.md still lists F3-3/4/5/6 as open, but PlanMigration / ApplyPlan /
// Backfill ship with integration tests in the root module — the markers are
// stale, not a missing feature. Flagged for TASKS hygiene in this PR's notes.
package f06_migrations

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"
	"github.com/jcsvwinston/quark/migrate"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f06_migrations"

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// planV1 / planV2 are two shapes of the same table (f6_plan): V2 adds a column.
type planV1 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (planV1) TableName() string { return "f6_plan" }

type planV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Price int    `db:"price"`
}

func (planV2) TableName() string { return "f6_plan" }

// bfRow is the backfill subject; legacy_id is the column we fill.
type bfRow struct {
	ID     int64                 `db:"id" pk:"true"`
	Name   string                `db:"name"`
	Legacy quark.Nullable[int64] `db:"legacy_id"`
}

func (bfRow) TableName() string { return "f6_backfill" }

// verRow is created/dropped by the versioned migration.
type verRow struct {
	ID  int64  `db:"id" pk:"true"`
	Tag string `db:"tag"`
}

func (verRow) TableName() string { return "f6_versioned" }

type rec struct {
	t   *testing.T
	eng string
	cat reporter.Category
}

func newRec(t *testing.T, eng string, cat reporter.Category) rec {
	return rec{t: t, eng: eng, cat: cat}
}

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: name, Engine: r.eng, Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestMigrations ./phases/f06_migrations/... -engines=" + r.eng,
		},
	})
}

func TestMigrations(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var ce []string
		for _, e := range engines {
			if e != tools.SQLite {
				ce = append(ce, e)
			}
		}
		tools.Down(ce...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			t.Run("PlanAndApply", func(t *testing.T) { planAndApply(t, ctx, conn, eng) })
			t.Run("BackfillResumes", func(t *testing.T) { backfillResumes(t, ctx, conn, eng) })
			t.Run("VersionedUpDown", func(t *testing.T) { versionedUpDown(t, ctx, conn, eng) })
			t.Run("LockSerializes", func(t *testing.T) { lockSerializes(t, ctx, conn, eng) })
		})
	}
}

func newClient(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) *quark.Client {
	t.Helper()
	// The versioned migrator's bookkeeping uses the guarded raw-exec path, so
	// enable raw queries (same as the root migrate tests).
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	c, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(limits))
	if err != nil {
		t.Fatalf("quark.New(%q): %v", conn.Driver, err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		if eng == tools.SQLite {
			_ = os.Remove(conn.DSN)
		}
	})
	return c
}

// dropTable best-effort drops a table so a sub-test starts from a clean schema
// on a shared server-engine database. Oracle has no DROP TABLE IF EXISTS, but
// Oracle is excluded from CI; the error is ignored either way.
func dropTable(ctx context.Context, c *quark.Client, name string) {
	_, _ = c.Raw().ExecContext(ctx, "DROP TABLE IF EXISTS "+name)
}

// planAndApply: empty plan on a matching schema; exact diff on an added column;
// ApplyPlan applies it; the schema then round-trips the new column.
func planAndApply(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	c := newClient(t, ctx, conn, eng)
	dropTable(ctx, c, "f6_plan")

	if err := c.Migrate(ctx, &planV1{}); err != nil {
		r.fail("PlanAndApply", reporter.SeverityP1, "migrate planV1: %v", err)
		return
	}
	// Scope every assertion to our own table: the connection's database may hold
	// other tables (system tables on MySQL's `mysql` DB, other phases' tables on
	// a shared server DB), and PlanMigration diffs the WHOLE current schema, so
	// ops for foreign tables are expected noise — only f6_plan ops are ours.

	// Matching model → no ops touching f6_plan.
	if plan, err := c.PlanMigration(ctx, &planV1{}); err != nil {
		r.fail("PlanAndApply", reporter.SeverityP1, "plan v1: %v", err)
		return
	} else if ours := opsForTable(plan, "f6_plan"); len(ours) != 0 {
		r.fail("PlanAndApply", reporter.SeverityP1, "plan v1 has %d ops on f6_plan, want 0 (false-positive diff): %s", len(ours), opsString(ours))
	}

	// Added column → an f6_plan op that mentions the new column.
	plan, err := c.PlanMigration(ctx, &planV2{})
	if err != nil {
		r.fail("PlanAndApply", reporter.SeverityP1, "plan v2: %v", err)
		return
	}
	ours := opsForTable(plan, "f6_plan")
	if len(ours) == 0 || !opsMention(ours, "price") {
		r.fail("PlanAndApply", reporter.SeverityP1, "plan v2 f6_plan ops do not add column price: %s", opsString(ours))
		return
	}

	// Apply only our table's ops (so we don't touch foreign/system tables), then
	// re-plan: no f6_plan ops should remain.
	if err := c.ApplyPlan(ctx, quark.Plan{Ops: ours}); err != nil {
		r.fail("PlanAndApply", reporter.SeverityP1, "apply plan (f6_plan ops): %v", err)
		return
	}
	if plan2, err := c.PlanMigration(ctx, &planV2{}); err != nil {
		r.fail("PlanAndApply", reporter.SeverityP1, "plan v2 after apply: %v", err)
		return
	} else if rem := opsForTable(plan2, "f6_plan"); len(rem) != 0 {
		r.fail("PlanAndApply", reporter.SeverityP1, "f6_plan still has %d ops after ApplyPlan: %s", len(rem), opsString(rem))
	}

	// The new column round-trips.
	w := planV2{Name: "w", Price: 42}
	if err := quark.For[planV2](ctx, c).Create(&w); err != nil {
		r.fail("PlanAndApply", reporter.SeverityP1, "create planV2 after apply: %v", err)
		return
	}
	got, err := quark.For[planV2](ctx, c).Find(w.ID)
	if err != nil || got.Price != 42 {
		r.fail("PlanAndApply", reporter.SeverityP1, "round-trip new column: got=%+v err=%v", got, err)
	}
}

// opsForTable returns the plan ops whose rendered form references table — used
// to ignore ops on foreign/system tables that PlanMigration surfaces because it
// diffs the whole current schema, not just our model's table.
func opsForTable(p quark.Plan, table string) []quark.Operation {
	var out []quark.Operation
	for _, op := range p.Ops {
		if strings.Contains(strings.ToLower(op.String()), strings.ToLower(table)) {
			out = append(out, op)
		}
	}
	return out
}

func opsMention(ops []quark.Operation, needle string) bool {
	for _, op := range ops {
		if strings.Contains(strings.ToLower(op.String()), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func opsString(ops []quark.Operation) string {
	parts := make([]string, len(ops))
	for i, op := range ops {
		parts[i] = op.String()
	}
	return strings.Join(parts, " | ")
}

// backfillResumes: fill legacy_id by PK; a Process that fails mid-way leaves a
// resume token, and a re-run with the same Name completes the rest.
func backfillResumes(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	c := newClient(t, ctx, conn, eng)
	dropTable(ctx, c, "f6_backfill")
	dropTable(ctx, c, "quark_backfill_state") // reset backfill resume tokens for a clean run
	if err := c.Migrate(ctx, &bfRow{}); err != nil {
		r.fail("BackfillResumes", reporter.SeverityP1, "migrate bfRow: %v", err)
		return
	}

	const n = 250
	for i := 0; i < n; i++ {
		if err := quark.For[bfRow](ctx, c).Create(&bfRow{Name: "r"}); err != nil {
			r.fail("BackfillResumes", reporter.SeverityP1, "seed row %d: %v", i, err)
			return
		}
	}

	fill := func(bctx context.Context, batch []int64) error {
		for _, pk := range batch {
			if _, err := quark.For[bfRow](bctx, c).Where("id", "=", pk).
				UpdateMap(map[string]any{"legacy_id": pk}); err != nil {
				return err
			}
		}
		return nil
	}
	countFilled := func() int64 {
		got, _ := quark.For[bfRow](ctx, c).Where("legacy_id", "IS NOT NULL", nil).Count()
		return got
	}

	// First run fails partway: Process errors once it sees a PK past a cutoff,
	// so the rows up to the last successful batch are filled and a resume token
	// is recorded.
	cutoff := int64(n / 2)
	errInjected := errors.New("f6: injected mid-backfill failure")
	failOnce := func(bctx context.Context, batch []int64) error {
		for _, pk := range batch {
			if pk > cutoff {
				return errInjected
			}
		}
		return fill(bctx, batch)
	}
	spec := quark.BackfillSpec{Name: "f6-legacy", Table: "f6_backfill", PKColumn: "id", BatchSize: 25, Process: failOnce}
	if err := c.Backfill(ctx, spec); !errors.Is(err, errInjected) {
		r.fail("BackfillResumes", reporter.SeverityP1, "first Backfill should surface the injected failure, got %v", err)
	}
	partial := countFilled()
	if partial == 0 || partial >= n {
		r.fail("BackfillResumes", reporter.SeverityP1, "after failed run %d/%d rows filled, want a partial fill (resume token)", partial, n)
	}

	// Re-run with the same Name resumes from the last successful PK and finishes.
	spec.Process = fill
	if err := c.Backfill(ctx, spec); err != nil {
		r.fail("BackfillResumes", reporter.SeverityP1, "resume Backfill: %v", err)
		return
	}
	if filled := countFilled(); filled != n {
		r.fail("BackfillResumes", reporter.SeverityP1, "after resume %d/%d rows filled, want all", filled, n)
	}
}

// versionedUpDown: a registered migration applies via Migrator.Up and reverts
// via Migrator.Down.
func versionedUpDown(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	c := newClient(t, ctx, conn, eng)
	dropTable(ctx, c, "f6_versioned")
	dropTable(ctx, c, "quark_migrations")

	migrate.Reset()
	t.Cleanup(migrate.Reset)
	migrate.Register(&migrate.Migration{
		ID:   "f6-001",
		Name: "create f6_versioned",
		Up:   func(ctx context.Context, cl *quark.Client) error { return cl.Migrate(ctx, &verRow{}) },
		Down: func(ctx context.Context, cl *quark.Client) error { dropTable(ctx, cl, "f6_versioned"); return nil },
	})

	m := migrate.NewMigrator(c)
	if err := m.Up(ctx, 1); err != nil {
		r.fail("VersionedUpDown", reporter.SeverityP1, "migrator Up: %v", err)
		return
	}
	// Table exists: a write+read round-trips.
	v := verRow{Tag: "up"}
	if err := quark.For[verRow](ctx, c).Create(&v); err != nil {
		r.fail("VersionedUpDown", reporter.SeverityP1, "create after Up (table should exist): %v", err)
		return
	}

	if err := m.Down(ctx, 1); err != nil {
		r.fail("VersionedUpDown", reporter.SeverityP1, "migrator Down: %v", err)
		return
	}
	// Table gone: a read must now error.
	if _, err := quark.For[verRow](ctx, c).Count(); err == nil {
		r.fail("VersionedUpDown", reporter.SeverityP1, "table f6_versioned still queryable after Down, want it dropped")
	}
}

// lockSerializes: two goroutines contend for the same migration lock. The
// second must time out while the first holds it, then succeed after release.
func lockSerializes(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	c := newClient(t, ctx, conn, eng)

	lock1, err := c.AcquireMigrationLock(ctx, "f6-lock", 2*time.Second)
	if err != nil {
		if errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Logf("LockSerializes: %s has no cross-process migration lock (single-writer) — ErrUnsupportedFeature, skipped", eng)
			return
		}
		r.fail("LockSerializes", reporter.SeverityP1, "acquire first lock: %v", err)
		return
	}

	// A second client/goroutine tries the same lock with a short timeout while
	// the first holds it — it must time out (serialisation).
	c2 := newClient(t, ctx, conn, eng)
	var wg sync.WaitGroup
	var secondErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		l, e := c2.AcquireMigrationLock(ctx, "f6-lock", 500*time.Millisecond)
		if e == nil {
			_ = l.Release(ctx)
		}
		secondErr = e
	}()
	wg.Wait()
	if !errors.Is(secondErr, quark.ErrLockTimeout) {
		r.fail("LockSerializes", reporter.SeverityP1, "second acquire while held should ErrLockTimeout, got %v", secondErr)
	}

	// Release the first; the lock must now be acquirable.
	if err := lock1.Release(ctx); err != nil {
		r.fail("LockSerializes", reporter.SeverityP1, "release first lock: %v", err)
		return
	}
	lock3, err := c.AcquireMigrationLock(ctx, "f6-lock", 2*time.Second)
	if err != nil {
		r.fail("LockSerializes", reporter.SeverityP1, "re-acquire after release: %v", err)
		return
	}
	_ = lock3.Release(ctx)
}
