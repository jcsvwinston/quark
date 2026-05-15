package quark_test

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// TestRowLevelSecurityAliasBackwardCompat guards the contract from F5-1
// (Fase 5): the legacy name RowLevelSecurity must remain a usable alias
// of RowLevelSecurityClient until v1.0. If this test breaks, the alias
// was removed prematurely or the underlying value drifted — both would
// break existing callers compiled against v0.x.
//
// Sunset: remove this test in the same PR that removes the
// RowLevelSecurity alias declaration (scheduled for v1.0). Leaving it
// here after the alias is gone is harmless but the file won't compile.
//
// See ADR-0012 §"Renombrado y deprecation" and tenant_router.go's
// RowLevelSecurity declaration.
func TestRowLevelSecurityAliasBackwardCompat(t *testing.T) {
	// Both spellings must refer to the same TenantStrategy value.
	if quark.RowLevelSecurity != quark.RowLevelSecurityClient {
		t.Fatalf("alias drift: RowLevelSecurity(%d) != RowLevelSecurityClient(%d)",
			quark.RowLevelSecurity, quark.RowLevelSecurityClient)
	}

	// The deprecated alias must still type-check in a TenantConfig.
	// We do not exercise the router runtime here — the canonical name is
	// exercised by every other test in this file; the alias only needs
	// to compile and equal the canonical value.
	//nolint:staticcheck // intentional use of the deprecated alias.
	cfg := quark.TenantConfig{Strategy: quark.RowLevelSecurity}
	if cfg.Strategy != quark.RowLevelSecurityClient {
		t.Fatalf("config assignment drift via alias: got %d", cfg.Strategy)
	}
}

// orRLSCapture records the SELECT SQL emitted while a query runs.
type orRLSCapture struct {
	mu   sync.Mutex
	stmt []string
}

func (c *orRLSCapture) ObserveQuery(e quark.QueryEvent) {
	if e.Operation != "SELECT" {
		return
	}
	c.mu.Lock()
	c.stmt = append(c.stmt, e.SQL)
	c.mu.Unlock()
}

func (c *orRLSCapture) selects() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.stmt))
	copy(out, c.stmt)
	return out
}

// testOrRLSLeak is the regression test for P0-1: Or() must not let an OR group
// escape the RowLevelSecurityClient tenant_id predicate via SQL operator precedence.
//
// Scenario: two tenants ("ta", "tb") share a table. Each tenant has rows in
// every status. A query under tenant "ta" with .Where(status=pending).Or(status=paid)
// must return only "ta" rows. Before the fix, the OR branch matched any tenant.
func testOrRLSLeak(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type RLSOrder struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Status   string `db:"status"`
	}

	dropTable(baseClient, "rls_orders")
	if err := baseClient.Migrate(ctx, &RLSOrder{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	defer dropTable(baseClient, "rls_orders")

	// Seed both tenants directly through the base client (bypass the router so
	// we can plant rows under arbitrary tenant_ids).
	rootCtx := context.Background()
	seed := []RLSOrder{
		{TenantID: "ta", Status: "pending"},
		{TenantID: "ta", Status: "paid"},
		{TenantID: "ta", Status: "shipped"},
		{TenantID: "tb", Status: "pending"},
		{TenantID: "tb", Status: "paid"},
		{TenantID: "tb", Status: "shipped"},
	}
	for i := range seed {
		if err := quark.For[RLSOrder](rootCtx, baseClient).Create(&seed[i]); err != nil {
			t.Fatalf("seed insert failed for %+v: %v", seed[i], err)
		}
	}

	// Build a router in RowLevelSecurityClient with a SQL-capturing observer.
	cap := &orRLSCapture{}
	observedClient, err := baseClient.WithOptions(quark.WithQueryObserver(cap))
	if err != nil {
		t.Fatalf("WithOptions failed: %v", err)
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityClient
	cfg.BaseClient = observedClient

	type ctxKey string
	const tenantKey ctxKey = "tenant_id"
	resolver := func(c context.Context) string {
		if v, ok := c.Value(tenantKey).(string); ok {
			return v
		}
		return ""
	}
	// factory is nil: RowLevelSecurityClient reuses the BaseClient and does not
	// need a per-tenant connection pool.
	router := quark.NewTenantRouter(cfg, resolver, nil)

	// findSelectWithOrGroup locates the captured SELECT containing an OR group.
	findSelectWithOrGroup := func(t *testing.T, stmts []string) string {
		t.Helper()
		for _, s := range stmts {
			if strings.Contains(strings.ToLower(s), " or (") {
				return s
			}
		}
		t.Fatalf("did not observe an OR group in any SELECT; got: %v", stmts)
		return ""
	}

	// countTenantPredicate counts occurrences of the tenant column reference in
	// the SQL. Robust across dialect quoting (PG/SQLite "x", MySQL `x`, MSSQL
	// [x], Oracle uppercase). We bound by `tenant_id` substring (case-insensitive)
	// and require it to appear with a comparison operator nearby.
	countTenantPredicate := func(sql string) int {
		re := regexp.MustCompile(`(?i)tenant_id\s*[\]"\x60]?\s*=\s*`)
		return len(re.FindAllString(sql, -1))
	}

	t.Run("FlatOrRespectsTenant", func(t *testing.T) {
		cap.mu.Lock()
		cap.stmt = nil
		cap.mu.Unlock()

		ctxA := context.WithValue(rootCtx, tenantKey, "ta")
		got, err := quark.For[RLSOrder](ctxA, router).
			Where("status", "=", "pending").
			Or(func(q *quark.Query[RLSOrder]) *quark.Query[RLSOrder] {
				return q.Where("status", "=", "paid")
			}).
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}

		if len(got) != 2 {
			t.Errorf("expected 2 rows for tenant ta (pending+paid), got %d: %+v", len(got), got)
		}
		for _, r := range got {
			if r.TenantID != "ta" {
				t.Errorf("tenant leak: got row with tenant_id=%q (expected ta): %+v", r.TenantID, r)
			}
		}

		// SQL inspection — independent of dialect quoting, the OR group must
		// carry the tenant predicate inside its parenthesis.
		sel := findSelectWithOrGroup(t, cap.selects())
		// Single-level group: regex with no nested-paren handling is sufficient.
		groupRe := regexp.MustCompile(`(?is)\bOR\s*\([^()]*tenant_id[^()]*\)`)
		if !groupRe.MatchString(sel) {
			t.Errorf("OR group does not contain tenant_id predicate.\nSQL: %s", sel)
		}
		// Outer + 1 group → at least 2 occurrences of the tenant predicate.
		if got := countTenantPredicate(sel); got < 2 {
			t.Errorf("expected tenant predicate to appear at least 2× (outer + OR group), got %d.\nSQL: %s", got, sel)
		}
	})

	t.Run("NestedOrRespectsTenant", func(t *testing.T) {
		cap.mu.Lock()
		cap.stmt = nil
		cap.mu.Unlock()

		ctxA := context.WithValue(rootCtx, tenantKey, "ta")
		got, err := quark.For[RLSOrder](ctxA, router).
			Where("status", "=", "pending").
			Or(func(q *quark.Query[RLSOrder]) *quark.Query[RLSOrder] {
				return q.Where("status", "=", "paid").
					Or(func(q2 *quark.Query[RLSOrder]) *quark.Query[RLSOrder] {
						return q2.Where("status", "=", "shipped")
					})
			}).
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("nested List failed: %v", err)
		}

		// All three statuses for tenant "ta" → 3 rows; nothing from "tb".
		if len(got) != 3 {
			t.Errorf("expected 3 rows for tenant ta across 3 statuses, got %d: %+v", len(got), got)
		}
		for _, r := range got {
			if r.TenantID != "ta" {
				t.Errorf("nested tenant leak: tenant_id=%q in %+v", r.TenantID, r)
			}
		}

		// SQL inspection — count-based to handle nested parens that defeat
		// non-recursive regex. Outer + two OR groups → 3 tenant predicates.
		sel := findSelectWithOrGroup(t, cap.selects())
		if got := countTenantPredicate(sel); got < 3 {
			t.Errorf("expected tenant predicate to appear at least 3× (outer + 2 nested OR groups), got %d.\nSQL: %s", got, sel)
		}
	})

	t.Run("OtherTenantUnaffected", func(t *testing.T) {
		ctxB := context.WithValue(rootCtx, tenantKey, "tb")
		got, err := quark.For[RLSOrder](ctxB, router).
			Where("status", "=", "pending").
			Or(func(q *quark.Query[RLSOrder]) *quark.Query[RLSOrder] {
				return q.Where("status", "=", "paid")
			}).
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("tenant B List failed: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("tenant B expected 2 rows, got %d: %+v", len(got), got)
		}
		for _, r := range got {
			if r.TenantID != "tb" {
				t.Errorf("tenant B saw foreign tenant_id=%q in %+v", r.TenantID, r)
			}
		}
	})
}
