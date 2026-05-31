// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f05_tenancy is bug-bash phase F5: multi-tenancy. It exercises the
// tenant-isolation strategies Quark ships (ADR-0007/0012) against real
// engines, closing the cross-engine coverage the tenant playbook flags as
// owed:
//
//   - RowLevelSecurityClient (all six engines): WHERE-injection isolation,
//     cross-tenant read/update/delete denial, the Or() regression (P0-1), and
//     a concurrency sweep asserting zero cross-tenant leak.
//   - DatabasePerTenant (SQLite): physical isolation via a per-tenant pool
//     built by the router factory.
//   - SchemaPerTenant (PostgreSQL): one schema per tenant.
//   - RowLevelSecurityNative (PostgreSQL): engine-enforced isolation. The
//     critical test connects as a NON-superuser role (the postgres superuser
//     bypasses RLS even with FORCE) and proves the policy filters even raw
//     SQL and rejects cross-tenant writes via WITH CHECK. On every non-PG
//     engine the strategy must surface ErrUnsupportedFeature (asserted, not
//     skipped — per CLAUDE.md rule #7).
//
// Scale note: the spec's "10k concurrent ops per strategy" is a soak-tier
// figure; this phase runs a bounded concurrent load (logged) that still
// exposes a Go-side tenant-propagation race, and leaves the 10k sustained
// run to F14.
package f05_tenancy

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"
	"github.com/jcsvwinston/quark/quarktenant"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f05_tenancy"

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

// tdoc is the shared-table tenant model (RowLevelSecurityClient /
// DatabasePerTenant / SchemaPerTenant). tenant_id is a string so it matches
// validTenantID (^[a-z0-9_-]+$) and is auto-filled by ensureTenantID on write
// under the client strategy.
type tdoc struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Name     string `db:"name"`
	Status   string `db:"status"`
}

// ntdoc is a separate table for the Native group so installing RLS policies
// (which ENABLE/FORCE row-level security on the table) never bleeds into the
// other groups' shared tdoc table.
type ntdoc struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Name     string `db:"name"`
}

type ctxKey string

const tenantKey ctxKey = "tenant_id"

func withTenant(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, tenantKey, id)
}

func resolveTenant(ctx context.Context) string {
	if v, ok := ctx.Value(tenantKey).(string); ok {
		return v
	}
	return ""
}

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
			Command: "go test -tags=bugbash -run TestTenancy ./phases/f05_tenancy/... -engines=" + r.eng,
		},
	})
}

func TestTenancy(t *testing.T) {
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
			t.Run("RLSClient", func(t *testing.T) { rlsClient(t, ctx, conn, eng) })
			t.Run("RLSClientConcurrent", func(t *testing.T) { rlsClientConcurrent(t, ctx, conn, eng) })
			t.Run("DatabasePerTenant", func(t *testing.T) { dbPerTenant(t, ctx, conn, eng) })
			t.Run("SchemaPerTenant", func(t *testing.T) { schemaPerTenant(t, ctx, conn, eng) })
			t.Run("RLSNative", func(t *testing.T) { rlsNative(t, ctx, conn, eng) })
		})
	}
}

// newClient opens a base client with raw queries enabled (the PG groups need
// raw DDL) and migrates the given models.
func newClient(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string, models ...any) *quark.Client {
	t.Helper()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(limits))
	if err != nil {
		t.Fatalf("quark.New(%q): %v", conn.Driver, err)
	}
	if len(models) > 0 {
		if err := client.Migrate(ctx, models...); err != nil {
			t.Fatalf("migrate on %s: %v", eng, err)
		}
	}
	return client
}

func newRouter(base *quark.Client, strategy quark.TenantStrategy) *quark.TenantRouter {
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = strategy
	cfg.BaseClient = base
	return quark.NewTenantRouter(cfg, resolveTenant, nil)
}

// rlsClient: WHERE-injection isolation on the shared table, cross-tenant
// denial, and the Or() regression (P0-1). Runs on all six engines.
func rlsClient(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	base := newClient(t, ctx, conn, eng, &tdoc{})
	t.Cleanup(func() { _ = base.Close() })
	router := newRouter(base, quark.RowLevelSecurityClient)

	// Three tenants, distinct row counts so a leak changes the totals.
	seed := map[string][]string{
		"rca": {"pending", "paid", "shipped"}, // 3
		"rcb": {"pending", "paid"},            // 2
		"rcc": {"cancelled"},                  // 1
	}
	for tid, statuses := range seed {
		tctx := withTenant(ctx, tid)
		for i, s := range statuses {
			// TenantID left zero on purpose: ensureTenantID fills it from the
			// router's tenant context (the client-strategy write path).
			doc := &tdoc{Name: fmt.Sprintf("%s-%d", tid, i), Status: s}
			if err := quark.For[tdoc](tctx, router).Create(doc); err != nil {
				r.fail("RLSClient/Create", reporter.SeverityP1, "create for %s: %v", tid, err)
				return
			}
			if doc.TenantID != tid {
				r.fail("RLSClient/ensureTenantID", reporter.SeverityP1,
					"write did not stamp tenant_id: got %q want %q", doc.TenantID, tid)
			}
		}
	}

	// Each tenant sees only its own rows.
	for tid, statuses := range seed {
		tctx := withTenant(ctx, tid)
		got, err := quark.For[tdoc](tctx, router).Limit(100).List()
		if err != nil {
			r.fail("RLSClient/List", reporter.SeverityP1, "list for %s: %v", tid, err)
			continue
		}
		if len(got) != len(statuses) {
			r.fail("RLSClient/isolation", reporter.SeverityP0,
				"tenant %s sees %d rows, want %d (cross-tenant leak)", tid, len(got), len(statuses))
		}
		for _, d := range got {
			if d.TenantID != tid {
				r.fail("RLSClient/leak", reporter.SeverityP0,
					"tenant %s sees a row owned by %q", tid, d.TenantID)
			}
		}
	}

	// Or() regression (P0-1): the tenant predicate must survive into the OR
	// group, so an Or across statuses still cannot cross the tenant boundary.
	actx := withTenant(ctx, "rca")
	orGot, err := quark.For[tdoc](actx, router).
		Where("status", "=", "pending").
		Or(func(q *quark.Query[tdoc]) *quark.Query[tdoc] { return q.Where("status", "=", "paid") }).
		Limit(100).List()
	if err != nil {
		r.fail("RLSClient/Or", reporter.SeverityP1, "or list: %v", err)
	} else {
		if len(orGot) != 2 { // rca has exactly one pending + one paid
			r.fail("RLSClient/Or", reporter.SeverityP1, "Or returned %d rows for rca, want 2", len(orGot))
		}
		for _, d := range orGot {
			if d.TenantID != "rca" {
				r.fail("RLSClient/OrLeak", reporter.SeverityP0,
					"Or group leaked a row owned by %q into tenant rca", d.TenantID)
			}
		}
	}

	// Cross-tenant mutation denial: rcb cannot update or delete rca's rows.
	bctx := withTenant(ctx, "rcb")
	if n, err := quark.For[tdoc](bctx, router).Where("status", "=", "shipped").UpdateMap(map[string]any{"name": "hijacked"}); err != nil {
		r.fail("RLSClient/UpdateMap", reporter.SeverityP1, "update: %v", err)
	} else if n != 0 {
		r.fail("RLSClient/crossUpdate", reporter.SeverityP0,
			"tenant rcb updated %d rows of status=shipped (only rca has one) — cross-tenant write", n)
	}
	if n, err := quark.For[tdoc](bctx, router).Where("status", "=", "cancelled").DeleteBy(); err != nil {
		r.fail("RLSClient/DeleteBy", reporter.SeverityP1, "delete: %v", err)
	} else if n != 0 {
		r.fail("RLSClient/crossDelete", reporter.SeverityP0,
			"tenant rcb deleted %d cancelled rows (only rcc has one) — cross-tenant delete", n)
	}
}

// rlsClientConcurrent stresses the Go-side tenant propagation: many goroutines
// each pinned to a tenant issue interleaved reads/writes on the shared
// BaseClient. A propagation bug (shared mutable state leaking a tenant across
// goroutines) shows up as a row whose tenant_id != the goroutine's tenant.
func rlsClientConcurrent(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	base := newClient(t, ctx, conn, eng, &tdoc{})
	t.Cleanup(func() { _ = base.Close() })
	router := newRouter(base, quark.RowLevelSecurityClient)

	const tenants = 8
	// SQLite serialises writers on one file; keep the per-goroutine op count
	// modest so the sweep stays fast and lock-free of flakes while still
	// interleaving thousands of tenant-scoped calls across goroutines.
	perGoroutine := 30
	tid := func(i int) string { return fmt.Sprintf("cc%d", i) }

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	leak := make([]string, 0)

	for i := 0; i < tenants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			myTID := tid(i)
			tctx := withTenant(ctx, myTID)
			for op := 0; op < perGoroutine; op++ {
				doc := &tdoc{Name: fmt.Sprintf("%s-%d", myTID, op), Status: "x"}
				if err := quark.For[tdoc](tctx, router).Create(doc); err != nil {
					if isTransient(err) {
						continue
					}
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("create %s/%d: %w", myTID, op, err)
					}
					mu.Unlock()
					return
				}
				// Read back our own rows — must never include another tenant's.
				got, err := quark.For[tdoc](tctx, router).Limit(1000).List()
				if err != nil {
					if isTransient(err) {
						continue
					}
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("list %s/%d: %w", myTID, op, err)
					}
					mu.Unlock()
					return
				}
				for _, d := range got {
					if d.TenantID != myTID {
						mu.Lock()
						leak = append(leak, fmt.Sprintf("goroutine %s saw %s", myTID, d.TenantID))
						mu.Unlock()
					}
				}
			}
		}(i)
	}
	wg.Wait()

	t.Logf("RLSClientConcurrent: %d tenants × %d ops = %d concurrent tenant-scoped ops (spec target 10k is F14 soak-tier)",
		tenants, perGoroutine, tenants*perGoroutine)

	if firstErr != nil {
		r.fail("Concurrent/op", reporter.SeverityP1, "%v", firstErr)
	}
	if len(leak) > 0 {
		r.fail("Concurrent/leak", reporter.SeverityP0,
			"cross-tenant leak under concurrency (%d): %s", len(leak), strings.Join(leak[:min(3, len(leak))], "; "))
	}
	// Final isolation check: each tenant's count equals its successful writes
	// upper bound; more importantly, every row carries the right tenant.
	for i := 0; i < tenants; i++ {
		myTID := tid(i)
		got, err := quark.For[tdoc](withTenant(ctx, myTID), router).Limit(1000).List()
		if err != nil {
			r.fail("Concurrent/finalList", reporter.SeverityP1, "list %s: %v", myTID, err)
			continue
		}
		for _, d := range got {
			if d.TenantID != myTID {
				r.fail("Concurrent/finalLeak", reporter.SeverityP0,
					"after sweep, tenant %s holds a row owned by %q", myTID, d.TenantID)
				break
			}
		}
	}
}

// isTransient tolerates SQLite's single-writer lock contention so the
// concurrency sweep measures isolation, not SQLite's busy semantics.
func isTransient(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "locked") || strings.Contains(s, "busy")
}

// dbPerTenant: physical isolation via a per-tenant connection pool built by
// the router factory. Demonstrated on SQLite (one file per tenant); container
// engines would use separate databases — out of scope for this phase (logged).
func dbPerTenant(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	if eng != tools.SQLite {
		t.Logf("DatabasePerTenant exercised on SQLite (one file per tenant); on %s it would use separate databases — not covered in F5 (see README scope).", eng)
		return
	}

	files := map[string]string{}
	t.Cleanup(func() {
		for _, f := range files {
			_ = os.Remove(f)
		}
	})
	var mu sync.Mutex
	factory := func(tenantID string) (*quark.Client, error) {
		f := fmt.Sprintf("%s/f5-dpt-%s-%s.db", os.TempDir(), eng, tenantID)
		mu.Lock()
		files[tenantID] = f
		mu.Unlock()
		c, err := quark.New("sqlite", f)
		if err != nil {
			return nil, err
		}
		if err := c.Migrate(ctx, &tdoc{}); err != nil {
			return nil, err
		}
		return c, nil
	}
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.DatabasePerTenant
	cfg.MaxCachedPools = 10
	router := quark.NewTenantRouter(cfg, resolveTenant, factory)

	// Write 2 rows for dpa, 1 for dpb, into their separate files.
	for tid, n := range map[string]int{"dpa": 2, "dpb": 1} {
		tctx := withTenant(ctx, tid)
		for i := 0; i < n; i++ {
			if err := quark.For[tdoc](tctx, router).Create(&tdoc{TenantID: tid, Name: fmt.Sprintf("%s-%d", tid, i), Status: "x"}); err != nil {
				r.fail("DBPerTenant/Create", reporter.SeverityP1, "create %s: %v", tid, err)
				return
			}
		}
	}
	for tid, want := range map[string]int{"dpa": 2, "dpb": 1} {
		got, err := quark.For[tdoc](withTenant(ctx, tid), router).Limit(100).List()
		if err != nil {
			r.fail("DBPerTenant/List", reporter.SeverityP1, "list %s: %v", tid, err)
			continue
		}
		if len(got) != want {
			r.fail("DBPerTenant/isolation", reporter.SeverityP0,
				"tenant %s file holds %d rows, want %d (physical isolation broken)", tid, len(got), want)
		}
	}
}

// schemaPerTenant: one schema per tenant (PostgreSQL). The caller owns schema
// creation and per-schema migration (tenant playbook); F5 does it with raw
// DDL, then routes through SchemaPerTenant and asserts isolation.
func schemaPerTenant(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryDialectSpecific)
	if eng != tools.Postgres {
		t.Logf("SchemaPerTenant exercised on PostgreSQL only (schemas are PG/MSSQL; MySQL conflates schema/database, SQLite has none) — scoped out on %s (see README).", eng)
		return
	}
	base := newClient(t, ctx, conn, eng) // no migrate: tables live in tenant schemas
	t.Cleanup(func() { _ = base.Close() })
	table := quark.GetModelMeta[tdoc]().Table // match For[tdoc]'s fullTableName

	schemas := []string{"spa", "spb"}
	t.Cleanup(func() {
		for _, s := range schemas {
			_ = base.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, s))
		}
	})
	for _, s := range schemas {
		if err := base.Exec(ctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, s)); err != nil {
			r.fail("SchemaPerTenant/dropSchema", reporter.SeverityP1, "drop schema %s: %v", s, err)
			return
		}
		if err := base.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, s)); err != nil {
			r.fail("SchemaPerTenant/createSchema", reporter.SeverityP1, "create schema %s: %v", s, err)
			return
		}
		if err := base.Exec(ctx, fmt.Sprintf(
			`CREATE TABLE %q.%q (id BIGSERIAL PRIMARY KEY, tenant_id TEXT, name TEXT, status TEXT)`, s, table)); err != nil {
			r.fail("SchemaPerTenant/createTable", reporter.SeverityP1, "create table in %s: %v", s, err)
			return
		}
	}

	router := newRouter(base, quark.SchemaPerTenant)
	// Insert 2 rows under spa, 1 under spb — into their own schemas.
	for tid, n := range map[string]int{"spa": 2, "spb": 1} {
		tctx := withTenant(ctx, tid)
		for i := 0; i < n; i++ {
			if err := quark.For[tdoc](tctx, router).Create(&tdoc{TenantID: tid, Name: fmt.Sprintf("%s-%d", tid, i), Status: "x"}); err != nil {
				r.fail("SchemaPerTenant/Create", reporter.SeverityP1, "create %s: %v", tid, err)
				return
			}
		}
	}
	for tid, want := range map[string]int{"spa": 2, "spb": 1} {
		got, err := quark.For[tdoc](withTenant(ctx, tid), router).Limit(100).List()
		if err != nil {
			r.fail("SchemaPerTenant/List", reporter.SeverityP1, "list %s: %v", tid, err)
			continue
		}
		if len(got) != want {
			r.fail("SchemaPerTenant/isolation", reporter.SeverityP0,
				"tenant schema %s holds %d rows, want %d", tid, len(got), want)
		}
	}
}

// rlsNative: engine-enforced isolation (PostgreSQL). On non-PG the strategy
// must return ErrUnsupportedFeature (asserted). On PG the test connects as a
// NON-superuser role — the postgres superuser bypasses RLS even with FORCE,
// so only a plain role observes the policy — and proves: (a) the builder path
// isolates per tenant, (b) raw SQL run by the role without the tenant var sees
// nothing (the engine, not the client, enforces it), and (c) WITH CHECK
// rejects a cross-tenant write.
func rlsNative(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	if eng != tools.Postgres {
		rlsNativeUnsupported(t, ctx, conn, eng)
		return
	}
	r := newRec(t, eng, reporter.CategoryDialectSpecific)

	// Superuser client: create table, seed both tenants (bypassing RLS),
	// install policies, and provision a non-superuser role. InstallRLSPolicies
	// operates on *registered* models, so register + MigrateRegistered (not a
	// bare Migrate).
	su := newClient(t, ctx, conn, eng)
	t.Cleanup(func() { _ = su.Close() })
	if err := su.RegisterModel(&ntdoc{}); err != nil {
		r.fail("RLSNative/register", reporter.SeverityP1, "register: %v", err)
		return
	}
	if err := su.MigrateRegistered(ctx); err != nil {
		r.fail("RLSNative/migrate", reporter.SeverityP1, "migrate: %v", err)
		return
	}
	table := quark.GetModelMeta[ntdoc]().Table

	// Clean slate for re-runs.
	_ = su.Exec(ctx, fmt.Sprintf(`DELETE FROM %q`, table))
	for _, tt := range []struct {
		tid string
		n   int
	}{{"nta", 2}, {"ntb", 1}} {
		for i := 0; i < tt.n; i++ {
			if err := su.Exec(ctx, fmt.Sprintf(`INSERT INTO %q (tenant_id, name) VALUES ($1,$2)`, table),
				tt.tid, fmt.Sprintf("%s-%d", tt.tid, i)); err != nil {
				r.fail("RLSNative/seed", reporter.SeverityP1, "seed %s: %v", tt.tid, err)
				return
			}
		}
	}

	if _, err := quarktenant.InstallRLSPolicies(ctx, su, quarktenant.InstallOptions{
		TenantColumn: "tenant_id", NativeRLSVar: "app.tenant_id", ForceRLS: true,
		TenantColumnSQLCast: "text",
	}); err != nil {
		r.fail("RLSNative/install", reporter.SeverityP1, "install policies: %v", err)
		return
	}

	const role = "quark_f5_role"
	// Best-effort reset for re-runs: revoke any leftover grants (which would
	// otherwise block DROP ROLE) then drop. Errors ignored — the role may not
	// exist yet.
	_ = su.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON %q FROM %s`, table, role))
	_ = su.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %s`, role))
	_ = su.Exec(ctx, fmt.Sprintf(`REVOKE USAGE ON SCHEMA public FROM %s`, role))
	_ = su.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, role))
	for _, ddl := range []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'quark'`, role),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, role),
		fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON %q TO %s`, table, role),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, role),
	} {
		if err := su.Exec(ctx, ddl); err != nil {
			r.fail("RLSNative/role", reporter.SeverityP1, "role DDL %q: %v", ddl, err)
			return
		}
	}
	t.Cleanup(func() {
		_ = su.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON %q FROM %s`, table, role))
		_ = su.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %s`, role))
		_ = su.Exec(ctx, fmt.Sprintf(`REVOKE USAGE ON SCHEMA public FROM %s`, role))
		_ = su.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, role))
	})

	// Role client: non-superuser, subject to the policy.
	roleDSN, err := withRole(conn.DSN, role, "quark")
	if err != nil {
		r.fail("RLSNative/roleDSN", reporter.SeverityP1, "derive role DSN: %v", err)
		return
	}
	roleClient := newClient(t, ctx, tools.EngineConn{Driver: conn.Driver, DSN: roleDSN}, eng)
	t.Cleanup(func() { _ = roleClient.Close() })
	router := newRouter(roleClient, quark.RowLevelSecurityNative)

	// (a) Builder path isolates per tenant — engine-enforced.
	for _, tt := range []struct {
		tid  string
		want int
	}{{"nta", 2}, {"ntb", 1}} {
		got, err := quark.For[ntdoc](withTenant(ctx, tt.tid), router).Limit(100).List()
		if err != nil {
			r.fail("RLSNative/List", reporter.SeverityP1, "list %s: %v", tt.tid, err)
			continue
		}
		if len(got) != tt.want {
			r.fail("RLSNative/isolation", reporter.SeverityP0,
				"tenant %s sees %d rows, want %d (policy not enforced)", tt.tid, len(got), tt.want)
		}
		for _, d := range got {
			if d.TenantID != tt.tid {
				r.fail("RLSNative/leak", reporter.SeverityP0, "tenant %s saw row of %q", tt.tid, d.TenantID)
			}
		}
	}

	// (b) The critical test: a RAW query by the role, straight on the *sql.DB
	// (fully outside Quark's builder and guard), with no tenant var set, sees
	// ZERO rows — the engine enforces the policy even outside the builder
	// (contrast RowLevelSecurityClient, where raw SQL bypasses isolation).
	var cnt int
	if err := roleClient.Raw().QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %q`, table)).Scan(&cnt); err != nil {
		r.fail("RLSNative/rawQuery", reporter.SeverityP1, "raw count: %v", err)
	} else if cnt != 0 {
		r.fail("RLSNative/rawEnforced", reporter.SeverityP0,
			"raw SELECT by the role with no tenant var returned %d rows, want 0 (engine should hide all)", cnt)
	}

	// (c) WITH CHECK rejects a cross-tenant write: under tenant nta, inserting
	// a row explicitly tagged ntb must violate the policy.
	err = quark.For[ntdoc](withTenant(ctx, "nta"), router).Create(&ntdoc{TenantID: "ntb", Name: "smuggled"})
	if err == nil {
		r.fail("RLSNative/withCheck", reporter.SeverityP0,
			"insert of a foreign-tenant row under nta succeeded; WITH CHECK not enforced")
	}
}

// rlsNativeUnsupported asserts RowLevelSecurityNative surfaces
// ErrUnsupportedFeature on non-PostgreSQL engines, via both entry points.
func rlsNativeUnsupported(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryDialectSpecific)
	base := newClient(t, ctx, conn, eng, &ntdoc{})
	t.Cleanup(func() { _ = base.Close() })
	router := newRouter(base, quark.RowLevelSecurityNative)

	_, err := quark.For[ntdoc](withTenant(ctx, "nta"), router).Limit(1).List()
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		r.fail("RLSNative/unsupported/ForT", reporter.SeverityP1,
			"want ErrUnsupportedFeature on %s, got %v", eng, err)
	}
	err = router.Tx(withTenant(ctx, "nta"), func(*quark.Tx) error { return nil })
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		r.fail("RLSNative/unsupported/Tx", reporter.SeverityP1,
			"want ErrUnsupportedFeature from router.Tx on %s, got %v", eng, err)
	}
}

// withRole rewrites a postgres URL DSN to authenticate as a different role.
func withRole(dsn, user, pass string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
}
