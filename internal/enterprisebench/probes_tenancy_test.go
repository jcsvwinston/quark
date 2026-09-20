// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktenant"

	sqlitedrv "modernc.org/sqlite"
)

// The tenancy family measures the two things an enterprise application
// actually depends on when it puts several customers in one database: WHICH
// STATEMENT reaches the engine (is the tenant predicate there? is the schema
// prefix there? which session variable is set?) and WHOSE ROWS come back.
// Both are read from the emitted SQL and from the returned rows, never from
// the presence of a symbol — a tenant predicate that a PK path silently drops
// is invisible to anything that only checks that the feature "exists".

// rlsRow is the tenant-scoped table this family measures against: a primary
// key, a tenant column, and a payload column whose value names its owner, so
// a leak is legible in the row itself and not only in a count.
type rlsRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Status   string `db:"status"`
}

func (rlsRow) TableName() string { return "rls_rows" }

// rlsSecondRow is a SECOND tenant-scoped model, with a table of its own.
//
// It exists for RLS-05, whose title says the policy generator renders the DDL
// for EVERY registered model. One model cannot tell a generator that iterates
// the registry from one that renders the first entry and stops, so the control
// needs two: the iteration is the claim, and the claim has to be measurable.
type rlsSecondRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Label    string `db:"label"`
}

func (rlsSecondRow) TableName() string { return "rls_second_rows" }

// rlsTenantKey carries the tenant the router resolves. An application would
// put it there from a request header; the probes put it there by hand.
type rlsTenantKey struct{}

func rlsCtx(tenant string) context.Context {
	return context.WithValue(context.Background(), rlsTenantKey{}, tenant)
}

func rlsResolver(ctx context.Context) string {
	tenant, _ := ctx.Value(rlsTenantKey{}).(string)
	return tenant
}

// rlsFixture opens a database of its own, creates the tenant-scoped table and
// seeds one row per tenant (id 1 belongs to "ta", id 2 to "tb").
//
// The shared client would be the default, but the probes in this family
// UPDATE and DELETE each other's rows on purpose: measuring a cross-tenant
// delete against a table an earlier probe already emptied would measure
// nothing. Each one therefore gets a database nothing else has touched.
func rlsFixture(t *testing.T, e *env, name string, opts ...any) (*quark.Client, *recorder) {
	t.Helper()
	c, rec := e.fresh(t, name, opts...)
	ctx := context.Background()
	if err := c.Migrate(ctx, &rlsRow{}); err != nil {
		t.Fatalf("%s: create the tenant table: %v", name, err)
	}
	for _, row := range []rlsRow{
		{ID: 1, TenantID: "ta", Status: "owned-by-ta"},
		{ID: 2, TenantID: "tb", Status: "owned-by-tb"},
	} {
		seed := row
		if err := quark.For[rlsRow](ctx, c).Create(&seed); err != nil {
			t.Fatalf("%s: seed %+v: %v", name, seed, err)
		}
	}
	rec.reset()
	return c, rec
}

// rlsPostgresShaped returns a client that SPEAKS PostgreSQL — the dialect
// decides the SQL and every tenancy gate in the router keys off its name —
// while the rows live in the bench's SQLite database.
//
// Native RLS is gated to the PostgreSQL dialect, so without this the whole
// Native pipeline (the session variable, the cache key, the implicit
// transaction) would be unreachable from a bench that has no live engine, and
// this family could only ever measure refusals. What it buys is exactly the
// client-side half: the statement emitted, the variable set, the key cached,
// the connection returned. What it does NOT buy is enforcement — SQLite has
// no policies, so no probe here concludes that rows are isolated from this
// setup, and the controls that turn on enforcement say so in their note and
// stay partial.
func rlsPostgresShaped(t *testing.T, e *env, name string, opts ...any) (*quark.Client, *recorder) {
	t.Helper()
	c, rec := e.fresh(t, name, append(opts, any(quark.WithDialect(quark.PostgreSQL())))...)
	ctx := context.Background()
	// The table is created in SQLite's own DDL: the migrator would emit
	// PostgreSQL DDL for this client, which the storage engine underneath
	// would reject. The fixture is setup, not measurement.
	if _, err := c.Raw().ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS rls_rows (id INTEGER PRIMARY KEY, tenant_id TEXT, status TEXT)`); err != nil {
		t.Fatalf("%s: create the tenant table: %v", name, err)
	}
	if _, err := c.Raw().ExecContext(ctx,
		`INSERT OR REPLACE INTO rls_rows (id, tenant_id, status) VALUES (1,'ta','owned-by-ta'),(2,'tb','owned-by-tb')`); err != nil {
		t.Fatalf("%s: seed the tenant table: %v", name, err)
	}
	rec.reset()
	return c, rec
}

// set_config is PostgreSQL's, and the Native router emits it verbatim. The
// shim below teaches SQLite a function of that name and arity so the probes
// can READ THE ARGUMENTS the router puts on the wire — which session variable
// it sets, to which value — instead of inferring them from a failure message.
//
// It records and returns the value; it does NOT filter anything. Isolation in
// PostgreSQL comes from the policies that read the variable, and no policy
// exists here, which is why the enforcement controls stay partial.
var (
	rlsSetConfigOnce sync.Once
	rlsSetConfigErr  error
	rlsSetConfigMu   sync.Mutex
	rlsSetConfigLog  []string
)

func rlsSetConfigShim(t *testing.T) {
	t.Helper()
	rlsSetConfigOnce.Do(func() {
		rlsSetConfigErr = sqlitedrv.RegisterScalarFunction("set_config", 3,
			func(_ *sqlitedrv.FunctionContext, args []driver.Value) (driver.Value, error) {
				name, _ := args[0].(string)
				value, _ := args[1].(string)
				rlsSetConfigMu.Lock()
				rlsSetConfigLog = append(rlsSetConfigLog, name+"="+value)
				rlsSetConfigMu.Unlock()
				return value, nil
			})
	})
	if rlsSetConfigErr != nil {
		t.Fatalf("install the set_config shim: %v", rlsSetConfigErr)
	}
}

func rlsSetConfigReset() {
	rlsSetConfigMu.Lock()
	defer rlsSetConfigMu.Unlock()
	rlsSetConfigLog = nil
}

func rlsSetConfigCalls() []string {
	rlsSetConfigMu.Lock()
	defer rlsSetConfigMu.Unlock()
	out := make([]string, len(rlsSetConfigLog))
	copy(out, rlsSetConfigLog)
	return out
}

// rlsRouter builds a TenantRouter over a base client for a strategy that
// shares one pool (every strategy this family measures except
// DatabasePerTenant, which brings its own factory).
func rlsRouter(base *quark.Client, strategy quark.TenantStrategy) *quark.TenantRouter {
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = strategy
	cfg.BaseClient = base
	// These probes measure the router's MECHANISM — which variable it sets,
	// how it scopes the cache — on a PostgreSQL-shaped SQLite that has no
	// pg_class. The policy check a Native router runs at first use (A8 S8)
	// would refuse every one of them; RLS-04 is the control that measures
	// that check, and builds its router with the check on.
	cfg.SkipPolicyVerification = true
	return quark.NewTenantRouter(cfg, rlsResolver, nil)
}

// rlsTenantsIn names the distinct owners among the returned rows, so a probe
// can say "tenant ta got tb's row" rather than "the count was wrong".
func rlsTenantsIn(rows []rlsRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.TenantID] {
			seen[r.TenantID] = true
			out = append(out, r.TenantID)
		}
	}
	return out
}

// rlsOwnedOnlyBy reports whether a read came back with rows and every one of
// them belongs to the given tenant.
//
// The weak form this replaced — "at most one distinct owner" — was satisfied
// by three different worlds: the right owner, somebody else's rows alone, and
// no rows at all. The last two are the leak and the blind spot. An empty read
// OBSERVES NOTHING: it is the fixture failing to offer a row, not the scoping
// keeping one back, and reading it as isolation is the mistake RLS-12 forbids
// itself in so many words.
func rlsOwnedOnlyBy(rows []rlsRow, tenant string) bool {
	owners := rlsTenantsIn(rows)
	return len(rows) > 0 && len(owners) == 1 && owners[0] == tenant
}

// rlsScopedToTenant reports whether a statement FILTERS by the tenant column.
//
// Only the WHERE clause counts: an UPDATE names the tenant column in its SET
// list and an INSERT in its column list, and neither scopes anything. Reading
// the whole statement instead called a cross-tenant UPDATE "scoped" — the
// probe passed while the write went to another tenant's row.
func rlsScopedToTenant(stmt string) bool {
	at := strings.LastIndex(stmt, " WHERE ")
	if at < 0 {
		return false
	}
	return strings.Contains(stmt[at:], `"tenant_id"`)
}

// rlsOrGroupScoped reports whether the tenant predicate is INSIDE the OR
// group, not merely beside it.
//
// `A AND B OR C` parses as `(A AND B) OR C`, so a tenant predicate that only
// precedes an Or() group is escaped by the group: every row matching C comes
// back whoever owns it. rlsScopedToTenant cannot tell the two apart — both
// statements carry "tenant_id" after the WHERE — so this reads the
// parenthesised group on its own.
func rlsOrGroupScoped(stmt string) bool {
	at := strings.LastIndex(stmt, " OR (")
	if at < 0 {
		return false
	}
	return strings.Contains(stmt[at:], `"tenant_id"`)
}

// rlsLastArgs returns the arguments bound to the most recent recorded
// statement, or nil when nothing was recorded.
//
// recorder.last() hands back the SQL TEXT, and the text is only half of a
// scoping: `WHERE "tenant_id" = ?` reads the same whether the router bound
// this caller's tenant or another one's. The recorder already keeps the whole
// quark.QueryEvent, so the values are there to be read; this family reads them
// here rather than growing the recorder's own API, which every family in this
// package shares.
func rlsLastArgs(r *recorder) []any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return nil
	}
	return r.events[len(r.events)-1].Args
}

// rlsTenantBindings returns the value bound to each `"tenant_id" = ?`
// predicate in the statement's WHERE clause.
//
// Only the WHERE counts, for the reason rlsScopedToTenant gives — an UPDATE
// names the tenant column in its SET list and scopes nothing — but the
// placeholders BEFORE the WHERE are still counted, because they are what puts
// the WHERE's own parameters at the index they occupy in args.
//
// It knows one predicate shape, the `?`-placeholder equality the client-side
// strategy injects on this bench's dialect. A tenant predicate of any other
// shape yields no bindings, and rlsBoundToTenant then reports false: a probe
// that cannot read the value must not conclude that the value is right.
func rlsTenantBindings(stmt string, args []any) []any {
	where := strings.LastIndex(stmt, " WHERE ")
	if where < 0 {
		return nil
	}
	var bound []any
	ordinal := 0
	for i := 0; i < len(stmt); i++ {
		if stmt[i] != '?' {
			continue
		}
		if i > where && strings.HasSuffix(stmt[:i], `"tenant_id" = `) {
			if ordinal < len(args) {
				bound = append(bound, args[ordinal])
			} else {
				bound = append(bound, nil)
			}
		}
		ordinal++
	}
	return bound
}

// rlsBoundToTenant reports whether the statement carries at least one tenant
// predicate and every one of them is bound to THIS tenant.
//
// The column name in a WHERE does not say which tenant it selects, and a
// scoping bound to the wrong tenant is exactly the leak these controls exist
// to catch: it emits the same statement the correct one does and serves
// somebody else's rows. Reading the text alone let that world share a verdict
// with the working one.
func rlsBoundToTenant(stmt string, args []any, tenant string) bool {
	bound := rlsTenantBindings(stmt, args)
	if len(bound) == 0 {
		return false
	}
	for _, v := range bound {
		if s, ok := v.(string); !ok || s != tenant {
			return false
		}
	}
	return true
}

// rlsScopedTo reads the last recorded statement and reports whether it filters
// by the tenant column AND binds that filter to the given tenant. It is the
// form every client-side scoping check in this family uses; rlsScopedToTenant
// alone survives only where the fact being measured is the ABSENCE of any
// tenant predicate.
func rlsScopedTo(rec *recorder, tenant string) bool {
	stmt := rec.last()
	return rlsScopedToTenant(stmt) && rlsBoundToTenant(stmt, rlsLastArgs(rec), tenant)
}

// ---------------------------------------------------------------------
// RLS-01
// ---------------------------------------------------------------------

// probeRlsNativeDialectCoverage asks every dialect Quark ships for a
// Native-RLS query and counts the ones that answer with a statement instead
// of a refusal. The plan for this arc assumed native RLS existed on more than
// one engine; the count is the answer, and the statement the accepting
// dialect emits names the engine it belongs to.
func probeRlsNativeDialectCoverage(t *testing.T, e *env) verdict {
	rlsSetConfigShim(t)

	candidates := []struct {
		name    string
		dialect quark.Dialect
	}{
		{"sqlite", quark.SQLite()},
		{"mysql", quark.MySQL()},
		{"mariadb", quark.MariaDB()},
		{"mssql", quark.MSSQL()},
		{"oracle", quark.Oracle()},
		{"postgres", quark.PostgreSQL()},
	}

	var accepted []string
	for _, cand := range candidates {
		c, _ := rlsPostgresShaped(t, e, "rls01_"+cand.name)
		// The dialect is what every Native gate reads; swap it after the
		// fixture built the table so each engine answers for itself.
		swapped, err := c.WithOptions(quark.WithDialect(cand.dialect), quark.WithLogger(quiet))
		if err != nil {
			t.Fatalf("clone the client as %s: %v", cand.name, err)
		}
		rlsSetConfigReset()
		_, listErr := quark.For[rlsRow](rlsCtx("ta"), rlsRouter(swapped, quark.RowLevelSecurityNative)).List()
		if errors.Is(listErr, quark.ErrUnsupportedFeature) {
			continue
		}
		if listErr != nil {
			t.Fatalf("%s: native list failed for a reason that is not a refusal: %v", cand.name, listErr)
		}
		accepted = append(accepted, cand.name)
		if cand.name == "postgres" {
			// The accepting dialect emits PostgreSQL's set_config: the
			// session-variable mechanism is that engine's, not a portable one.
			if calls := rlsSetConfigCalls(); len(calls) == 0 {
				t.Fatalf("postgres accepted the Native strategy but emitted no set_config call")
			}
		}
	}

	switch {
	case len(accepted) == 0:
		return absent
	case len(accepted) >= 3:
		// The arc plan's headline: native RLS on three engines.
		return present
	case len(accepted) == 1 && accepted[0] == "postgres":
		return partial
	default:
		// Neither "PostgreSQL alone" nor "three engines": whatever the set is
		// now, the control's title no longer describes it.
		t.Fatalf("native RLS is accepted by %v; this control's title says PostgreSQL alone — re-measure and retitle", accepted)
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-02
// ---------------------------------------------------------------------

// probeRlsNativeRefusalOnOtherEngines walks the three public doors of a
// Native router on an engine that has no native RLS — For[T], Tx and
// GetClient — and reads what each one hands back. A refusal at two doors and
// a working client at the third is not a refusal: the third door is where an
// application reads rows with no filter at all.
func probeRlsNativeRefusalOnOtherEngines(t *testing.T, e *env) verdict {
	c, rec := rlsFixture(t, e, "rls02_native_refusal") // SQLite: no native RLS
	router := rlsRouter(c, quark.RowLevelSecurityNative)
	ctx := rlsCtx("ta")

	_, listErr := quark.For[rlsRow](ctx, router).List()
	forRefused := errors.Is(listErr, quark.ErrUnsupportedFeature)

	txErr := router.Tx(ctx, func(tx *quark.Tx) error { return nil })
	txRefused := errors.Is(txErr, quark.ErrUnsupportedFeature)

	client, getErr := router.GetClient(ctx)
	getRefused := client == nil && getErr != nil

	foreignRows := 0
	if client != nil && getErr == nil {
		rec.reset()
		rows, err := quark.For[rlsRow](ctx, client).List()
		if err != nil {
			t.Fatalf("read through the client GetClient handed back: %v", err)
		}
		for _, r := range rows {
			if r.TenantID != "ta" {
				foreignRows++
			}
		}
		if foreignRows > 0 && rlsScopedToTenant(rec.last()) {
			t.Fatalf("a scoped statement returned foreign rows: %q", rec.last())
		}
	}

	switch {
	case forRefused && txRefused && getRefused:
		return present
	case forRefused && txRefused && foreignRows > 0:
		// Two doors refuse, the third serves every tenant: the degradation
		// the strategy promises not to have.
		return partial
	case !forRefused && !txRefused:
		return absent
	default:
		// Some door changed its mind since this control was written: the
		// verdict would be guesswork.
		t.Fatalf("doors measured: For[T] refuses=%v, Tx refuses=%v, GetClient refuses=%v, foreign rows through GetClient=%d — re-measure this control",
			forRefused, txRefused, getRefused, foreignRows)
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-03
// ---------------------------------------------------------------------

// probeRlsClientPredicatePKPaths drives the shapes an application uses under
// RowLevelSecurityClient — the five builder doors the control's note names
// (List, Where, Or, UpdateMap, DeleteBy) and the four that address a row
// directly (Find, Update(entity), Delete(entity), Create) — and reads the
// statement each one emitted. The strategy's whole contract is that the tenant
// predicate is in the WHERE and bound to the CALLER's tenant, so the probe
// reads both: the clause, because a row can come back from a scoped statement
// by coincidence and from an unscoped one by leak, and the value the router
// bound to that clause, because a column name in a WHERE says nothing about
// which tenant it selects. Where the door returns rows, their owner is read
// too — and a door that returned none has measured nothing.
//
// Every door the note claims is EXERCISED here. A note that lists five
// surviving paths while the probe drives one is an assertion somebody read
// once, and four paths that can lose the predicate without the bench noticing.
func probeRlsClientPredicatePKPaths(t *testing.T, e *env) verdict {
	c, rec := rlsFixture(t, e, "rls03_client_predicate")
	router := rlsRouter(c, quark.RowLevelSecurityClient)
	ta := rlsCtx("ta")

	// The builder path, for contrast: this is the one the strategy is named
	// after, and it must be scoped.
	rec.reset()
	listed, err := quark.For[rlsRow](ta, router).List()
	if err != nil {
		t.Fatalf("list under the tenant router: %v", err)
	}
	// Both halves are strict: the statement has to bind the tenant predicate
	// to "ta", and the rows that came back have to be ta's and not nobody's.
	listScoped := rlsScopedTo(rec, "ta") && rlsOwnedOnlyBy(listed, "ta")

	// A caller's own Where must not displace the injected one.
	rec.reset()
	filtered, err := quark.For[rlsRow](ta, router).Where("status", "!=", "").List()
	if err != nil {
		t.Fatalf("filtered list under the tenant router: %v", err)
	}
	whereScoped := rlsScopedTo(rec, "ta") && rlsOwnedOnlyBy(filtered, "ta")

	// Or() is the path where a predicate that merely PRECEDES the group is
	// escaped by operator precedence, so the group is read on its own.
	rec.reset()
	disjunctive, err := quark.For[rlsRow](ta, router).Or(func(q *quark.Query[rlsRow]) *quark.Query[rlsRow] {
		return q.Where("id", "=", 2)
	}).List()
	if err != nil {
		t.Fatalf("disjunctive list under the tenant router: %v", err)
	}
	// id 2 is tb's, so the group is aimed at a row this tenant must not see:
	// the owner of what comes back is the measurement, not just the shape of
	// the clause.
	orScoped := rlsScopedTo(rec, "ta") && rlsOrGroupScoped(rec.last()) && rlsOwnedOnlyBy(disjunctive, "ta")

	// Write by map, on this tenant's own row: the point is the WHERE, not the
	// effect, so it touches nothing the PK probes below depend on.
	rec.reset()
	if _, err := quark.For[rlsRow](ta, router).Where("id", "=", 1).
		UpdateMap(map[string]any{"status": "touched-by-ta"}); err != nil {
		t.Fatalf("UpdateMap under the tenant router: %v", err)
	}
	updateMapScoped := rlsScopedTo(rec, "ta")

	// Delete by condition, aimed at an id nothing owns: again the statement is
	// the measurement, and no row is removed.
	rec.reset()
	if _, err := quark.For[rlsRow](ta, router).Where("id", "=", 1000).DeleteBy(); err != nil {
		t.Fatalf("DeleteBy under the tenant router: %v", err)
	}
	deleteByScoped := rlsScopedTo(rec, "ta")

	builderScoped := listScoped && whereScoped && orScoped && updateMapScoped && deleteByScoped

	// Read by primary key: id 2 belongs to "tb".
	rec.reset()
	found, findErr := quark.For[rlsRow](ta, router).Find(2)
	findLeaks := findErr == nil && found.TenantID == "tb" && !rlsScopedToTenant(rec.last())

	// Write by primary key through the entity path.
	rec.reset()
	stolen := rlsRow{ID: 2, TenantID: "tb", Status: "written-by-ta"}
	updated, updErr := quark.For[rlsRow](ta, router).Update(&stolen)
	updateLeaks := updErr == nil && updated == 1 && !rlsScopedToTenant(rec.last())

	// Delete by primary key through the entity path.
	rec.reset()
	deleted, delErr := quark.For[rlsRow](ta, router).Delete(&rlsRow{ID: 2})
	deleteLeaks := delErr == nil && deleted == 1 && !rlsScopedToTenant(rec.last())

	// Create carrying somebody else's tenant id: the router resolves "ta",
	// the row says "tb", and nothing reconciles the two.
	planted := rlsRow{ID: 99, TenantID: "tb", Status: "planted-by-ta"}
	createLeaks := false
	if err := quark.For[rlsRow](ta, router).Create(&planted); err == nil {
		stored, err := quark.For[rlsRow](context.Background(), c).Find(99)
		createLeaks = err == nil && stored.TenantID == "tb"
	}

	// Each verdict is the CONJUNCTION of the facts its title states. A
	// disjunction ("any of the four leaks") would keep this control green
	// while three of the four closed, which is the regression the bench is
	// here to catch: the title would then describe a product that no longer
	// exists, and nothing would say so.
	switch {
	case builderScoped && !findLeaks && !updateLeaks && !deleteLeaks && !createLeaks:
		return present
	case builderScoped && findLeaks && updateLeaks && deleteLeaks && createLeaks:
		return partial
	case !builderScoped:
		// The predicate is missing from the path the strategy is named after.
		return absent
	default:
		t.Fatalf("client-side scoping measured: builder doors list=%v where=%v or=%v updateMap=%v deleteBy=%v; "+
			"direct paths leak find=%v update=%v delete=%v create=%v — some path changed its mind since this control was written, re-measure and retitle",
			listScoped, whereScoped, orScoped, updateMapScoped, deleteByScoped,
			findLeaks, updateLeaks, deleteLeaks, createLeaks)
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-04
// ---------------------------------------------------------------------

// probeRlsVerifyPolicies exercises the preflight that is supposed to make a
// missing policy loud, and then checks whether anything calls it: a Native
// router is built over a database where no policy was ever installed, and
// asked for rows.
func probeRlsVerifyPolicies(t *testing.T, e *env) verdict {
	rlsSetConfigShim(t)

	// Reachable, and gated to the engine whose catalog it reads.
	plain, _ := rlsFixture(t, e, "rls04_verify_gate")
	if err := plain.RegisterModel(&rlsRow{}); err != nil {
		t.Fatalf("register the model: %v", err)
	}
	_, gateErr := quarktenant.VerifyRLSPolicies(context.Background(), plain, quarktenant.DefaultInstallOptions())
	gated := errors.Is(gateErr, quark.ErrUnsupportedFeature)

	// Verifying nothing proves nothing: with no models registered the
	// preflight refuses instead of reporting success.
	empty, _ := rlsPostgresShaped(t, e, "rls04_verify_empty")
	_, emptyErr := quarktenant.VerifyRLSPolicies(context.Background(), empty, quarktenant.DefaultInstallOptions())
	refusesEmpty := errors.Is(emptyErr, quarktenant.ErrNoRegisteredModels)

	// The check itself reads the PostgreSQL catalog, which is where this
	// bench stops: on any other storage the query cannot even run.
	shaped, _ := rlsPostgresShaped(t, e, "rls04_verify_catalog")
	if err := shaped.RegisterModel(&rlsRow{}); err != nil {
		t.Fatalf("register the model on the PostgreSQL-shaped client: %v", err)
	}
	_, catalogErr := quarktenant.VerifyRLSPolicies(context.Background(), shaped, quarktenant.DefaultInstallOptions())
	needsLiveEngine := catalogErr != nil && strings.Contains(catalogErr.Error(), "pg_class")

	// Nothing verifies at boot: the router serves rows over a database whose
	// policies were never installed, without a word. If that ever changes the
	// list below stops with ErrRLSNotEnforced instead of returning rows, and
	// this control has gained its missing half.
	rlsSetConfigReset()
	verifying := quark.DefaultTenantConfig()
	verifying.Strategy = quark.RowLevelSecurityNative
	verifying.BaseClient = shaped
	rows, err := quark.For[rlsRow](rlsCtx("ta"), quark.NewTenantRouter(verifying, rlsResolver, nil)).List()
	bootVerified := errors.Is(err, quarktenant.ErrRLSNotEnforced)
	if err != nil && !bootVerified {
		t.Fatalf("native list over an unverified database: %v", err)
	}
	unverifiedBoot := !bootVerified && len(rows) > 0

	// No disjunction here. `gated || refusesEmpty` would absorb half the
	// title: the preflight could stop refusing an empty client — "refuses to
	// certify nothing", literally the control's own words — and the verdict
	// would stay partial because it was still gated to PostgreSQL.
	switch {
	case gated && refusesEmpty && bootVerified:
		return present
	case gated && refusesEmpty && unverifiedBoot && needsLiveEngine:
		return partial
	case !gated && !refusesEmpty:
		return absent
	default:
		t.Fatalf("verify measured: gated=%v refusesEmpty=%v bootVerified=%v unverifiedBoot=%v needsLiveEngine=%v — "+
			"one half of this control moved without the other; re-measure and retitle",
			gated, refusesEmpty, bootVerified, unverifiedBoot, needsLiveEngine)
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-05
// ---------------------------------------------------------------------

// probeRlsInstallPolicyDDL reads the DDL the generator renders for a
// registered model. The dry run is the whole measurement: it returns the
// statements an operator would apply, and a probe can check that the policy
// filters on the tenant column against the session variable in BOTH
// directions — a USING without a WITH CHECK would read safely and write
// anywhere.
func probeRlsInstallPolicyDDL(t *testing.T, e *env) verdict {
	shaped, rec := rlsPostgresShaped(t, e, "rls05_install")
	// TWO models, because "for every registered model" is the part of the
	// title that can rot invisibly: a generator that renders the first entry
	// of the registry and stops passes a one-model probe forever.
	if err := shaped.RegisterModel(&rlsRow{}, &rlsSecondRow{}); err != nil {
		t.Fatalf("register the models: %v", err)
	}
	opts := quarktenant.DefaultInstallOptions()
	opts.DryRun = true
	stmts, err := quarktenant.InstallRLSPolicies(context.Background(), shaped, opts)
	if err != nil {
		t.Fatalf("render the policy DDL: %v", err)
	}

	// The whole isolation block, read per table: ENABLE without FORCE exempts
	// the owner (usually the application role), a CREATE POLICY that is not
	// preceded by its DROP dies on the second run, and a USING without a WITH
	// CHECK reads safely and writes anywhere.
	block := func(table string) bool {
		var enable, force, idempotent, using, withCheck bool
		policy := `"` + table + `_tenant_isolation"`
		for _, s := range stmts {
			switch {
			case s == `ALTER TABLE "`+table+`" ENABLE ROW LEVEL SECURITY`:
				enable = true
			case s == `ALTER TABLE "`+table+`" FORCE ROW LEVEL SECURITY`:
				force = true
			case strings.HasPrefix(s, `DROP POLICY IF EXISTS `+policy+` ON "`+table+`"`):
				idempotent = true
			case strings.HasPrefix(s, `CREATE POLICY `+policy+` ON "`+table+`"`):
				using = strings.Contains(s, `USING ("tenant_id" = current_setting('app.tenant_id', true)`)
				withCheck = strings.Contains(s, `WITH CHECK ("tenant_id" = current_setting('app.tenant_id', true)`)
			}
		}
		return enable && force && idempotent && using && withCheck
	}
	rendered := block("rls_rows") && block("rls_second_rows")

	// And one policy per registered model: a table rendered twice, or a model
	// skipped, is a count that does not match.
	policies := 0
	for _, s := range stmts {
		if strings.HasPrefix(s, "CREATE POLICY ") {
			policies++
		}
	}
	perModel := policies == len(shaped.RegisteredModels())

	// A dry run must not touch the database.
	touched := len(rec.sql()) > 0

	// The cast lands inside the policy expression, so it is an injection
	// seam: the generator has to reject anything that is not a type token.
	opts.TenantColumnSQLCast = "text) OR (true"
	_, castErr := quarktenant.InstallRLSPolicies(context.Background(), shaped, opts)
	castGuarded := errors.Is(castErr, quarktenant.ErrInvalidCast)

	// And it refuses the engines whose DDL it does not speak.
	plain, _ := rlsFixture(t, e, "rls05_install_gate")
	if err := plain.RegisterModel(&rlsRow{}); err != nil {
		t.Fatalf("register the model on the SQLite client: %v", err)
	}
	_, gateErr := quarktenant.InstallRLSPolicies(context.Background(), plain, quarktenant.DefaultInstallOptions())
	gated := errors.Is(gateErr, quark.ErrUnsupportedFeature)

	switch {
	case rendered && perModel && !touched && castGuarded && gated:
		return present
	case !rendered && !perModel:
		// No isolation DDL for the registered models at all.
		return absent
	default:
		// Anything in between is a control whose title has stopped describing
		// it: a dry run that writes, a cast that gets through, an engine it
		// should refuse, a model it skipped. None of those is "a piece of the
		// control", which is what partial would claim — they are facts this
		// title asserts and no longer holds.
		t.Fatalf("install measured: rendered=%v perModel=%v (policies=%d, models=%d) touched=%v castGuarded=%v gated=%v — re-measure and retitle",
			rendered, perModel, policies, len(shaped.RegisteredModels()), touched, castGuarded, gated)
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-06
// ---------------------------------------------------------------------

// probeRlsPolicyCommandLine hands the embeddable runner the word the
// documentation prints in front of the action, and reads what it answers.
//
// WHAT THIS PROBE CAN AND CANNOT REACH. The rejection measured here is the
// runner's own argument parser refusing a `tenant` prefix that was never its
// vocabulary. The shipped `quark` binary is a different program: it DOES have
// a cobra `tenant` group (provision, migrate, list, migrate-all), it simply
// has no install-rls-policies inside it, and it lives in a module of its own
// (cmd/quark, ADR-0024) that this bench's module cannot import. So the control
// is titled for what runs here, not for the cobra refusal nobody measures.
func probeRlsPolicyCommandLine(t *testing.T, e *env) verdict {
	plain, _ := rlsFixture(t, e, "rls06_cli")
	if err := plain.RegisterModel(&rlsRow{}); err != nil {
		t.Fatalf("register the model: %v", err)
	}

	// The embeddable runner keeps its bare actions and reaches its dialect
	// gate through them; a `tenant` prefix is not its grammar.
	var stdout, stderr bytes.Buffer
	prefixed := quarktenant.RunWithIO(context.Background(),
		[]string{"tenant", "install-rls-policies", "--dry-run"}, plain, &stdout, &stderr)
	prefixRejected := prefixed == quarktenant.ExitError && strings.Contains(stderr.String(), `unknown action "tenant"`)
	stdout.Reset()
	stderr.Reset()
	embedded := quarktenant.RunWithIO(context.Background(),
		[]string{"install-rls-policies", "--dry-run"}, plain, &stdout, &stderr)
	runnerReached := embedded == quarktenant.ExitError && strings.Contains(stderr.String(), "requires PostgreSQL")
	if !prefixRejected || !runnerReached {
		t.Fatalf("the embeddable runner changed shape (prefix rejected=%v, bare action reached the gate=%v, stderr %q): re-measure",
			prefixRejected, runnerReached, stderr.String())
	}

	// The shipped binary, read from its sources: the tenant group registers
	// the two commands the documentation prints.
	src, ok := qk25RepoFile("cmd/quark/commands/tenant_rls.go")
	if !ok {
		src = ""
	}
	install := strings.Contains(src, `Use:           "install-rls-policies"`) && strings.Contains(src, "tenantCmd.AddCommand(")
	verify := strings.Contains(src, `Use:           "verify-rls-policies"`)
	switch {
	case install && verify:
		return present
	case install || verify:
		t.Logf("the binary registers install=%v verify=%v", install, verify)
		return partial
	default:
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-07
// ---------------------------------------------------------------------

// probeRlsRouterPolicyCoupling sets the router's session variable to one name
// and renders the policy DDL with the installer's default, then checks
// whether anything notices. The two sides of native RLS meet only through the
// spelling of that variable: the router writes it, the policy reads it, and
// when they disagree the policy simply matches nothing.
func probeRlsRouterPolicyCoupling(t *testing.T, e *env) verdict {
	rlsSetConfigShim(t)
	// The control is about the SILENCE, so the silence has to be observed.
	// Reading "no error was returned" is not the same fact: this product
	// complains through the structured log (RLS-11 measures exactly that), so
	// a warning about the mismatch would close this gap without any call
	// failing. The handler is installed at WARN, and the probe counts only the
	// records that NAME one of the two session variables — the builder's own
	// warning about an unbounded List is not a complaint about this coupling,
	// and counting it would make the probe measure the wrong silence.
	sink := &rlsLogSink{}
	shaped, _ := rlsPostgresShaped(t, e, "rls07_coupling",
		quark.WithLogger(slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	if err := shaped.RegisterModel(&rlsRow{}); err != nil {
		t.Fatalf("register the model: %v", err)
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityNative
	cfg.BaseClient = shaped
	cfg.NativeRLSVar = "app.divergent_from_the_policy"
	cfg.SkipPolicyVerification = true // the mechanism, not the policies: see rlsRouter
	router := quark.NewTenantRouter(cfg, rlsResolver, nil)

	rlsSetConfigReset()
	if _, err := quark.For[rlsRow](rlsCtx("ta"), router).List(); err != nil {
		// Nothing in the router compares its variable with the policy's, so
		// there is no error that MEANS the coupling. Reading any failure as
		// "the router refused the divergence" would let a storage fault, a
		// dialect gate or a broken set_config shim retire this gap: the probe
		// would report a coupling nobody wrote. When a sentinel for the
		// mismatch does appear, this is the line that names it.
		t.Fatalf("the divergent-variable list failed for a reason that is not the coupling: %v — re-measure this control", err)
	}
	calls := rlsSetConfigCalls()
	if len(calls) == 0 {
		t.Fatalf("the router emitted no set_config call")
	}
	routerSets := calls[len(calls)-1]

	opts := quarktenant.DefaultInstallOptions()
	opts.DryRun = true
	stmts, err := quarktenant.InstallRLSPolicies(context.Background(), shaped, opts)
	if err != nil {
		t.Fatalf("render the policy DDL: %v", err)
	}
	policyReads := strings.Join(stmts, "\n")

	// The router announced the variable it was configured with, and the policy
	// reads the installer's default instead.
	routerUsedOverride := strings.HasPrefix(routerSets, "app.divergent_from_the_policy=")
	policyKeptDefault := strings.Contains(policyReads, "current_setting('app.tenant_id'") &&
		!strings.Contains(policyReads, "divergent")
	mute := sink.count("app.divergent_from_the_policy") == 0 && sink.count("app.tenant_id") == 0

	switch {
	case !routerUsedOverride:
		// The router ignored NativeRLSVar altogether: the two sides cannot be
		// measured for divergence because one of them never diverged.
		t.Fatalf("the router set %q instead of the configured session variable — re-measure this control", routerSets)
		return absent
	case !policyKeptDefault:
		// The installer now renders the variable the router announces: the two
		// defaults are one, which is the coupling this control says is missing.
		return present
	case !mute:
		// They still diverge, but something said so — the gap the note
		// describes is the silence, and the silence is gone.
		return present
	default:
		return partial
	}
}

// ---------------------------------------------------------------------
// RLS-08
// ---------------------------------------------------------------------

// probeRlsCacheTenantScoped runs the SAME cached query for two tenants and
// reads the keys the cache store was asked for. Under native RLS the
// statement carries no tenant at all — the engine filters — so a cache keyed
// on the statement would serve one tenant's rows to the next. The store is
// the seam where that is visible.
func probeRlsCacheTenantScoped(t *testing.T, e *env) verdict {
	rlsSetConfigShim(t)
	store := &rlsCacheStore{data: map[string][]byte{}}
	shaped, rec := rlsPostgresShaped(t, e, "rls08_cache", quark.WithCacheStore(store))
	router := rlsRouter(shaped, quark.RowLevelSecurityNative)

	var statements []string
	for _, tenant := range []string{"ta", "tb", "ta"} {
		rec.reset()
		if _, err := quark.For[rlsRow](rlsCtx(tenant), router).
			Where("status", "!=", "").Cache(time.Minute).List(); err != nil {
			t.Fatalf("cached native list for %s: %v", tenant, err)
		}
		statements = append(statements, rec.last())
	}

	// If the two tenants emitted different SQL the cache key would be
	// distinct for a reason that has nothing to do with tenancy, and this
	// probe would be measuring the wrong thing.
	if statements[0] != statements[1] {
		t.Fatalf("the two tenants emitted different statements (%q vs %q); this probe cannot speak about the cache key",
			statements[0], statements[1])
	}

	keys := store.setKeys()
	distinct := map[string]bool{}
	for _, k := range keys {
		distinct[k] = true
	}
	// Two fills for two tenants, and the repeat visit served from the store.
	switch {
	case len(distinct) == 2 && store.hitCount() == 1:
		return present
	case len(distinct) == 2:
		return partial
	default:
		return absent
	}
}

// rlsCacheStore is a cache the probe can interrogate: which keys were read,
// which were written, how many reads found something.
type rlsCacheStore struct {
	mu   sync.Mutex
	data map[string][]byte
	sets []string
	hits int
}

func (s *rlsCacheStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	val, ok := s.data[key]
	if !ok {
		return nil, errors.New("miss")
	}
	s.hits++
	return val, nil
}

func (s *rlsCacheStore) Set(_ context.Context, key string, val []byte, _ time.Duration, _ ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets = append(s.sets, key)
	s.data[key] = val
	return nil
}

func (s *rlsCacheStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *rlsCacheStore) InvalidateTags(_ context.Context, _ ...string) error { return nil }

func (s *rlsCacheStore) setKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.sets))
	copy(out, s.sets)
	return out
}

func (s *rlsCacheStore) hitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// ---------------------------------------------------------------------
// RLS-09
// ---------------------------------------------------------------------

// probeRlsNativeDelegatesToEngine reads the statement a Native query emits
// and the rows it gets back from storage that has no policies. It measures
// the SHAPE of the guarantee — the builder adds no predicate, so whatever
// filtering happens is the engine's — and therefore cannot certify the
// filtering itself.
func probeRlsNativeDelegatesToEngine(t *testing.T, e *env) verdict {
	rlsSetConfigShim(t)
	shaped, rec := rlsPostgresShaped(t, e, "rls09_delegation")
	router := rlsRouter(shaped, quark.RowLevelSecurityNative)

	rlsSetConfigReset()
	rec.reset()
	rows, err := quark.For[rlsRow](rlsCtx("ta"), router).List()
	if err != nil {
		t.Fatalf("native list: %v", err)
	}
	stmt := rec.last()
	calls := rlsSetConfigCalls()

	// The pipeline ran (the tenant reached the session variable) and the
	// statement carries no tenant predicate of its own.
	delegates := len(calls) == 1 &&
		strings.HasSuffix(calls[0], "=ta") &&
		stmt != "" &&
		!rlsScopedToTenant(stmt)

	// And with no policy installed, every tenant's rows come back: the
	// separation is entirely the engine's to make.
	unfiltered := len(rlsTenantsIn(rows)) > 1

	switch {
	case delegates && unfiltered:
		// The delegation is visible end to end; the filtering it delegates to
		// is not something this storage can perform.
		return partial
	case !delegates:
		return absent
	default:
		t.Fatalf("the native path delegated but storage without policies still filtered the rows (%v) — something else is scoping the query; re-measure",
			rlsTenantsIn(rows))
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-10
// ---------------------------------------------------------------------

// probeRlsNativeImplicitTxLifecycle measures the two ends of the transaction
// the Native executor opens behind each query: a write must be committed by
// the time the call returns (a deferred commit would lose it on the way out),
// and the connection a read holds must go back to the pool when the request
// context ends (a held connection is a pool slot that never comes back).
func probeRlsNativeImplicitTxLifecycle(t *testing.T, e *env) verdict {
	rlsSetConfigShim(t)
	shaped, _ := rlsPostgresShaped(t, e, "rls10_implicit_tx")
	router := rlsRouter(shaped, quark.RowLevelSecurityNative)
	pool := shaped.Raw()

	// Write path: visible from ANOTHER connection the instant Create returns.
	writeCtx, cancelWrite := context.WithCancel(rlsCtx("ta"))
	defer cancelWrite()
	written := rlsRow{ID: 77, TenantID: "ta", Status: "written-under-native"}
	if err := quark.For[rlsRow](writeCtx, router).Create(&written); err != nil {
		t.Fatalf("native create: %v", err)
	}
	var found int
	if err := pool.QueryRowContext(context.Background(),
		`SELECT count(*) FROM rls_rows WHERE id = 77`).Scan(&found); err != nil {
		t.Fatalf("read the written row from another connection: %v", err)
	}
	durableOnReturn := found == 1

	// Read path: the connection is held for as long as the caller may still
	// be reading, and handed back when the context ends.
	readCtx, cancelRead := context.WithCancel(rlsCtx("ta"))
	if _, err := quark.For[rlsRow](readCtx, router).List(); err != nil {
		cancelRead()
		t.Fatalf("native list: %v", err)
	}
	heldDuringRequest := pool.Stats().InUse > 0
	cancelRead()
	released := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if pool.Stats().InUse == 0 {
			released = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	switch {
	case durableOnReturn && heldDuringRequest && released:
		return present
	case durableOnReturn || released:
		return partial
	default:
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-11
// ---------------------------------------------------------------------

// probeRlsRawWarning calls each raw door with a tenant in context and reads
// the log. The warning is a developer-experience cue, so the measurement is
// the log line: which door emits it, and which one hands out a *sql.DB with
// nothing said at all.
func probeRlsRawWarning(t *testing.T, e *env) verdict {
	const event = "quark.tenant.raw_under_native_rls"

	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true

	sink := &rlsLogSink{}
	native, _ := rlsFixture(t, e, "rls11_native_raw",
		quark.WithLimits(limits),
		quark.WithLogger(slog.New(slog.NewTextHandler(sink, nil))),
	)
	// Building the router is what arms the warning on the base client.
	_ = rlsRouter(native, quark.RowLevelSecurityNative)
	ta := rlsCtx("ta")

	before := sink.count(event)
	rows, err := native.RawQuery(ta, "SELECT id FROM rls_rows WHERE id = ?", 1)
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	_ = rows.Close()
	queryWarns := sink.count(event) > before

	before = sink.count(event)
	if err := native.Exec(ta, "UPDATE rls_rows SET status = ? WHERE id = ?", "raw", 1); err != nil {
		t.Fatalf("raw exec: %v", err)
	}
	execWarns := sink.count(event) > before

	// Raw() hands back the pool itself, and takes no context — so there is
	// no tenant to resolve and nothing to warn about, however the caller
	// then uses it.
	before = sink.count(event)
	if _, err := native.Raw().ExecContext(ta, "UPDATE rls_rows SET status = 'bypass' WHERE id = 1"); err != nil {
		t.Fatalf("exec through the raw pool: %v", err)
	}
	rawPoolWarns := sink.count(event) > before

	// The client strategy is the one where raw SQL really is a bypass of the
	// only filter there is. The warning is not armed there.
	clientSink := &rlsLogSink{}
	clientSide, _ := rlsFixture(t, e, "rls11_client_raw",
		quark.WithLimits(limits),
		quark.WithLogger(slog.New(slog.NewTextHandler(clientSink, nil))),
	)
	_ = rlsRouter(clientSide, quark.RowLevelSecurityClient)
	if err := clientSide.Exec(ta, "UPDATE rls_rows SET status = ? WHERE id = ?", "raw", 2); err != nil {
		t.Fatalf("raw exec under the client strategy: %v", err)
	}
	clientStrategyWarns := clientSink.count(event) > 0

	// Two of this control's four facts are NEGATIVE — Raw() cannot warn, the
	// client strategy never does — and a verdict reached by `queryWarns ||
	// execWarns` threw all but one of them away: the bench would stay green
	// with a door gone quiet, and green again on the day the blind doors
	// gained a warning, which is ground the arc wants to record.
	switch {
	case queryWarns && execWarns && (rawPoolWarns || clientStrategyWarns):
		// A blind door of the title learned to speak.
		return present
	case queryWarns && execWarns && !rawPoolWarns && !clientStrategyWarns:
		return partial
	case !queryWarns && !execWarns && !rawPoolWarns && !clientStrategyWarns:
		return absent
	default:
		t.Fatalf("raw-door warnings measured: RawQuery=%v Exec=%v Raw()=%v client-strategy=%v — re-measure and retitle",
			queryWarns, execWarns, rawPoolWarns, clientStrategyWarns)
		return absent
	}
}

// rlsLogSink collects the structured log the client writes, so a probe can
// count a specific event instead of asserting on formatting.
type rlsLogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *rlsLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *rlsLogSink) count(event string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Count(s.buf.String(), event)
}

// ---------------------------------------------------------------------
// RLS-12
// ---------------------------------------------------------------------

// probeRlsShardingWithTenancy puts a tenant in the context of a sharded read
// and reads whose rows come back — through BOTH sharded doors an application
// has: the key-routed query and the explicit scatter-gather fan-out.
//
// Scatter-gather is here because it is the door where the composition would
// fail hardest (it builds a query per shard against that shard's raw client),
// and reading its behaviour off the signatures instead of running it is how a
// note ends up asserting more than the bench measures.
//
// The inverse direction — a TenantRouter whose base is a ShardRouter — is not
// measured and cannot be: TenantConfig.BaseClient is a *Client, so the bench
// cannot even construct it. The title says only what runs here.
func probeRlsShardingWithTenancy(t *testing.T, e *env) verdict {
	shards := map[string]*quark.Client{}
	for _, name := range []string{"s0", "s1"} {
		c, _ := rlsFixture(t, e, "rls12_shard_"+name)
		shards[name] = c
	}
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver,
		quark.HashShardFunc([]string{"s0", "s1"}))
	if err != nil {
		t.Fatalf("build the shard router: %v", err)
	}

	ctx := quark.WithShardKey(rlsCtx("ta"), "customer-1")
	keyed, err := quark.For[rlsRow](ctx, router).List()
	if err != nil {
		t.Fatalf("list through the shard router: %v", err)
	}
	// An empty result observes NOTHING: no rows means no owners, and "no
	// foreign owners" would read as isolation the fixture never offered. If
	// the hash ever routes to a shard this fixture did not seed, the probe has
	// to be rebuilt, not believed.
	if len(keyed) == 0 {
		t.Fatalf("the key-routed read returned no rows; this fixture seeds every shard, so there is nothing to conclude from — re-measure this control")
	}
	keyedTenants := rlsTenantsIn(keyed)
	keyedScoped := len(keyedTenants) == 1 && keyedTenants[0] == "ta"

	// The explicit cross-shard read, same tenant in the same context.
	scattered, err := quark.ScatterGather(ctx, router,
		func(q *quark.Query[rlsRow]) *quark.Query[rlsRow] { return q },
		quark.ScatterMerge[rlsRow]{})
	if err != nil {
		t.Fatalf("scatter-gather through the shard router: %v", err)
	}
	if len(scattered) == 0 {
		t.Fatalf("the scatter-gather read returned no rows from either seeded shard — re-measure this control")
	}
	scatterTenants := rlsTenantsIn(scattered)
	scatterScoped := len(scatterTenants) == 1 && scatterTenants[0] == "ta"

	switch {
	case keyedScoped && scatterScoped:
		// Both sharded doors honour the tenant: the two compose.
		return present
	case !keyedScoped && !scatterScoped:
		// The tenant sat in the context of both reads and every owner came
		// back from both: the shard router is a different provider, and the
		// tenancy hook never sees it.
		return absent
	default:
		t.Fatalf("sharded reads measured: key-routed scoped=%v (owners %v), scatter-gather scoped=%v (owners %v) — one door started composing and the other did not; re-measure and retitle",
			keyedScoped, keyedTenants, scatterScoped, scatterTenants)
		return absent
	}
}

// ---------------------------------------------------------------------
// RLS-13
// ---------------------------------------------------------------------

// probeRlsOtherTenantStrategies drives the strategies whose scoping router.Tx
// has to carry — a pool per tenant, a schema per tenant, and the client-side
// predicate — through both the plain path and the transactional one. The
// transaction is where an application puts a read and its write, so a scoping
// that survives one path and not the other is a scoping an application cannot
// rely on.
//
// The RowLevelSecurityClient traversal is here because the control's note says
// that predicate is lost inside router.Tx "the same way". That sentence used
// to be a reading: the probe drove only the other two strategies, so the claim
// could have gone false without a test noticing. It is driven now.
func probeRlsOtherTenantStrategies(t *testing.T, e *env) verdict {
	ctx := context.Background()

	// DatabasePerTenant: a client per tenant, from the factory.
	perDatabase := quark.DefaultTenantConfig()
	perDatabase.Strategy = quark.DatabasePerTenant
	dbRouter := quark.NewTenantRouter(perDatabase, rlsResolver, func(tenant string) (*quark.Client, error) {
		c, _ := e.fresh(t, "rls13_dbper_"+tenant)
		if err := c.Migrate(ctx, &rlsRow{}); err != nil {
			return nil, err
		}
		row := rlsRow{ID: 1, TenantID: tenant, Status: "only-" + tenant}
		if err := quark.For[rlsRow](ctx, c).Create(&row); err != nil {
			return nil, err
		}
		return c, nil
	})
	perDatabaseScoped := true
	for _, tenant := range []string{"ta", "tb"} {
		tctx := rlsCtx(tenant)
		rows, err := quark.For[rlsRow](tctx, dbRouter).List()
		if err != nil {
			t.Fatalf("list under DatabasePerTenant for %s: %v", tenant, err)
		}
		if len(rlsTenantsIn(rows)) != 1 || rows[0].TenantID != tenant {
			perDatabaseScoped = false
		}
		if err := dbRouter.Tx(tctx, func(tx *quark.Tx) error {
			inTx, err := quark.ForTx[rlsRow](tctx, tx).List()
			if err != nil {
				return err
			}
			if len(rlsTenantsIn(inTx)) != 1 || inTx[0].TenantID != tenant {
				perDatabaseScoped = false
			}
			return nil
		}); err != nil {
			t.Fatalf("transaction under DatabasePerTenant for %s: %v", tenant, err)
		}
	}

	// SchemaPerTenant over one pool. SQLite has no schemas, but it can attach
	// a second database under a name and a qualified table name resolves into
	// it — so the tenant's schema holds a table of its own with only its rows,
	// and the evidence is the rows read back, not the shape of the statement.
	// The first version of this probe read the qualified name out of the
	// error SQLite raised for a schema it did not have, so a query that merely
	// FAILED inside the transaction satisfied it: no confinement was ever
	// observed. One connection, so the ATTACH is visible to every statement.
	rawLimits := quark.DefaultLimits()
	rawLimits.AllowRawQueries = true
	base, rec := rlsFixture(t, e, "rls13_schemaper", quark.WithMaxOpenConns(1), quark.WithLimits(rawLimits))
	for _, stmt := range []string{
		"ATTACH DATABASE ':memory:' AS ta",
		"CREATE TABLE ta.rls_rows (id INTEGER PRIMARY KEY, tenant_id TEXT, status TEXT)",
		"INSERT INTO ta.rls_rows VALUES (1, 'ta', 'in-ta-schema')",
	} {
		if err := base.Exec(ctx, stmt); err != nil {
			t.Fatalf("attach the tenant schema: %s: %v", stmt, err)
		}
	}
	schemaRouter := rlsRouter(base, quark.SchemaPerTenant)
	ta := rlsCtx("ta")

	// confinedToSchema is the positive fact, read the same way on both sides
	// of the transaction boundary: the statement names the tenant's schema,
	// and the rows that came back are the ones that live there — not the
	// shared table's rows for that tenant, which a filter would also return.
	confinedToSchema := func(stmt string, rows []rlsRow, err error) bool {
		return err == nil && strings.Contains(stmt, `"ta"."rls_rows"`) &&
			rlsOwnedOnlyBy(rows, "ta") && rows[0].Status == "in-ta-schema"
	}

	rec.reset()
	plainRows, plainErr := quark.For[rlsRow](ta, schemaRouter).List()
	schemaOutsideTx := confinedToSchema(rec.last(), plainRows, plainErr)

	// Inside the router's transaction, the same fact. An error or an
	// unqualified read inside is a verdict, not a fatal: the state where the
	// confinement is dropped reads the shared table and returns both tenants,
	// and that is exactly what this control exists to tell apart.
	schemaInsideTx := false
	inTxTenants := []string{}
	if err := schemaRouter.Tx(ta, func(tx *quark.Tx) error {
		rec.reset()
		rows, err := quark.ForTx[rlsRow](ta, tx).List()
		schemaInsideTx = confinedToSchema(rec.last(), rows, err)
		inTxTenants = rlsTenantsIn(rows)
		return nil
	}); err != nil {
		t.Fatalf("transaction under SchemaPerTenant: %v", err)
	}
	schemaLostInTx := schemaOutsideTx && !schemaInsideTx

	// RowLevelSecurityClient through the same door: outside the transaction
	// the predicate is in the WHERE (RLS-03 measures that in detail), and
	// inside it the same predicate, bound to the same tenant, with only that
	// tenant's rows back.
	clientBase, clientRec := rlsFixture(t, e, "rls13_clientside")
	clientRouter := rlsRouter(clientBase, quark.RowLevelSecurityClient)
	clientRec.reset()
	outsideRows, outsideErr := quark.For[rlsRow](ta, clientRouter).List()
	if outsideErr != nil {
		t.Fatalf("client-side list outside a transaction: %v", outsideErr)
	}
	// This is the PREMISE of the fact the title states: "survives" only means
	// something if the predicate was there, bound to this tenant, outside the
	// transaction. A wrong-tenant binding or an empty read would have
	// satisfied the weak form and left the control partial.
	clientOutsideTx := rlsScopedTo(clientRec, "ta") && rlsOwnedOnlyBy(outsideRows, "ta")

	clientInsideTx := false
	clientTxTenants := []string{}
	if err := clientRouter.Tx(ta, func(tx *quark.Tx) error {
		clientRec.reset()
		rows, err := quark.ForTx[rlsRow](ta, tx).List()
		if err != nil {
			return err
		}
		// The same premise inside. The first version accepted a predicate on
		// ANY tenant with no look at the rows, so a wrong binding passed.
		clientInsideTx = rlsScopedTo(clientRec, "ta") && rlsOwnedOnlyBy(rows, "ta")
		clientTxTenants = rlsTenantsIn(rows)
		return nil
	}); err != nil {
		t.Fatalf("transaction under RowLevelSecurityClient: %v", err)
	}
	clientLostInTx := clientOutsideTx && !clientInsideTx

	// Conjunction, not disjunction. `perDatabaseScoped || schemaOutsideTx`
	// passed on either half alone, and schemaLostInTx — the fact the title is
	// named after — was computed and then dropped on the floor.
	// And present demands the POSITIVE fact on every side: confinement
	// observed inside each transaction, not merely "not observed to be lost".
	switch {
	case perDatabaseScoped && schemaOutsideTx && schemaInsideTx && clientOutsideTx && clientInsideTx:
		return present
	case perDatabaseScoped && schemaOutsideTx && schemaLostInTx && clientLostInTx:
		return partial
	case !perDatabaseScoped && !schemaOutsideTx:
		return absent
	default:
		t.Fatalf("tenant strategies measured: DatabasePerTenant scoped=%v; SchemaPerTenant qualifies outside Tx=%v, loses it inside=%v; RowLevelSecurityClient scoped outside Tx=%v, loses it inside=%v (owners in Tx: schema %v, client %v) — re-measure and retitle",
			perDatabaseScoped, schemaOutsideTx, schemaLostInTx, clientOutsideTx, clientLostInTx, inTxTenants, clientTxTenants)
		return absent
	}
}
