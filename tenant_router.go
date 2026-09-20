// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
)

var validTenantID = regexp.MustCompile(`^[a-z0-9_-]+$`)

// TenantStrategy defines how multi-tenancy is handled.
type TenantStrategy int

const (
	// DatabasePerTenant uses a separate database connection pool per tenant.
	// This requires an LRU cache to prevent connection exhaustion.
	DatabasePerTenant TenantStrategy = iota
	// SchemaPerTenant uses a single database connection pool but prefixes
	// the table name with the tenant ID (e.g. "tenant_acme.users").
	SchemaPerTenant
	// RowLevelSecurityClient uses a single database connection pool and
	// injects a "WHERE tenant_id = ?" predicate into every query the
	// builder constructs. This is **client-side tenant scoping**, not
	// engine-enforced Row-Level Security: `client.Raw()` and `client.Exec()`
	// bypass the predicate. See ADR-0012 and `docs/playbooks/tenant.md` for
	// the limitations. On PostgreSQL, prefer RowLevelSecurityNative for
	// engine-enforced isolation. On other engines this remains the only
	// row-level option.
	RowLevelSecurityClient
	// RowLevelSecurityNative delegates row-level isolation to the
	// database engine via PostgreSQL row-level security policies. Each
	// query is wrapped in a transaction that first calls
	// `set_config('app.tenant_id', <tenantID>, true)` (i.e., SET LOCAL);
	// `CREATE POLICY` clauses on each tenant-scoped table reference that
	// session variable to filter rows. Unlike RowLevelSecurityClient,
	// `client.Raw()` / `client.Exec()` are still filtered: the policy
	// runs server-side and returns zero rows when `app.tenant_id` is not
	// set on the current transaction — there is no client-side bypass.
	//
	// A structured warning for Raw/Exec callers under a Native router
	// context is deferred to a follow-up (TASKS.md F5-2 closure block).
	// The engine enforcement is the security boundary; the warning would
	// be a developer-experience cue, not a safety net.
	//
	// Native is PostgreSQL-only. Constructing a Query[T] under a Native
	// router with a non-PostgreSQL dialect returns ErrUnsupportedFeature.
	//
	// See ADR-0012 §"Cómo se ejecuta SET LOCAL por query" for the
	// rationale and the F5-3 CLI for the DDL generator.
	RowLevelSecurityNative
)

// RowLevelSecurity is the legacy name for RowLevelSecurityClient. The
// constant value is identical, so existing code and serialized configs
// continue to work without changes.
//
// Deprecated: use RowLevelSecurityClient. The name change clarifies that
// this strategy is client-side WHERE injection, not engine-enforced RLS —
// see ADR-0012 and RowLevelSecurityNative for the engine-enforced
// PostgreSQL variant.
// Scheduled for removal in v2.0.0, no earlier than 2026-12-08.
// See docs/deprecations/DEP-2026-001-rowlevelsecurity-alias.md for the
// migration path and the reasoning behind both dates.
const RowLevelSecurity = RowLevelSecurityClient

// TenantConfig configures the TenantRouter.
type TenantConfig struct {
	Strategy       TenantStrategy
	MaxCachedPools int     // Maximum number of DB connection pools to keep open (for DatabasePerTenant)
	BaseClient     *Client // Used for SchemaPerTenant, RowLevelSecurityClient and RowLevelSecurityNative
	TenantColumn   string  // Column name for RowLevelSecurityClient, default is "tenant_id"

	// SkipPolicyVerification turns off the check a RowLevelSecurityNative
	// router runs the first time it touches each table: that row-level
	// security is enabled on it and at least one policy exists (A8 S8). Off
	// by default, the router refuses with ErrRLSNotEnforced instead of
	// serving every tenant's rows over a table whose policies were never
	// installed. Set it when policies are managed outside Quark's catalog
	// view — a test that measures something else, a schema the connecting
	// role cannot inspect — and run quarktenant.VerifyRLSPolicies yourself.
	SkipPolicyVerification bool

	// NativeRLSVar is the PostgreSQL session variable name used by
	// RowLevelSecurityNative to carry the resolved tenant ID. Each
	// query under a Native router is wrapped in a transaction that
	// calls `set_config(NativeRLSVar, <tenantID>, true)` before
	// executing; the `CREATE POLICY` clauses installed by
	// `quark tenant install-rls-policies` (F5-3) reference the same
	// variable.
	//
	// Defaults to "app.tenant_id". Must be a valid PostgreSQL
	// configuration parameter name (lowercase, dot-namespaced).
	// Ignored when Strategy is not RowLevelSecurityNative.
	NativeRLSVar string
}

// DefaultTenantConfig provides sensible defaults.
func DefaultTenantConfig() TenantConfig {
	return TenantConfig{
		Strategy:       DatabasePerTenant,
		MaxCachedPools: 100,
		TenantColumn:   "tenant_id",
		NativeRLSVar:   "app.tenant_id",
	}
}

// defaultNativeRLSVar returns the configured NativeRLSVar or the
// "app.tenant_id" default. Callers should use this helper so the
// fallback stays consistent across the codebase.
func (cfg TenantConfig) defaultNativeRLSVar() string {
	if cfg.NativeRLSVar == "" {
		return "app.tenant_id"
	}
	return cfg.NativeRLSVar
}

// lruEntry represents a cached tenant client.
type lruEntry struct {
	tenantID string
	client   *Client
}

// TenantRouter manages dynamic database connections or queries for different tenants.
type TenantRouter struct {
	config   TenantConfig
	resolver func(ctx context.Context) string
	factory  func(tenantID string) (*Client, error)

	// LRU Cache for DatabasePerTenant strategy
	cache   map[string]*list.Element
	lruList *list.List
	mu      sync.Mutex

	// verified holds the tables whose row-level security this Native
	// router has confirmed in the engine's catalog; a table is checked
	// once and a failure is not cached, so installing the policies is
	// enough to recover (A8 S8).
	verified   map[string]bool
	verifiedMu sync.Mutex
}

// NewTenantRouter creates a new router for multi-tenant database access.
func NewTenantRouter(
	config TenantConfig,
	resolver func(ctx context.Context) string,
	factory func(tenantID string) (*Client, error),
) *TenantRouter {
	// Stamp the shared BaseClient so its RawQuery/Exec can warn when a
	// raw call runs with a tenant in context under Native RLS. Done once
	// at setup; the field is read on the raw path, never mutated by
	// queries. If the same BaseClient backs multiple Native routers
	// (unusual — strategies are exclusive per router), the last
	// NewTenantRouter call wins for this warning. See
	// Client.warnRawUnderNativeRLS.
	if config.Strategy == RowLevelSecurityNative && config.BaseClient != nil {
		config.BaseClient.nativeTenantResolver = resolver
	}
	r := &TenantRouter{
		config:   config,
		resolver: resolver,
		factory:  factory,
		cache:    make(map[string]*list.Element),
		lruList:  list.New(),
	}
	// The shared pool learns which router confines it, so a transaction
	// opened straight on the BaseClient with a tenant in its context is
	// confined the way router.Tx confines one (QK-26; see Client.BeginTx).
	// DatabasePerTenant has no shared pool: each tenant's client is its own
	// confinement, and router.Tx stamps the transaction itself.
	switch config.Strategy {
	case SchemaPerTenant, RowLevelSecurityClient, RowLevelSecurityNative:
		if config.BaseClient != nil {
			config.BaseClient.tenantRouter = r
		}
	}
	return r
}

// ResolveTenant returns the tenant ID for the context.
func (r *TenantRouter) ResolveTenant(ctx context.Context) (string, error) {
	tenantID := r.resolver(ctx)
	if tenantID == "" {
		return "", errors.New("tenant_id not found in context")
	}
	if !validTenantID.MatchString(tenantID) {
		return "", fmt.Errorf("invalid tenant_id: %s", tenantID)
	}
	return tenantID, nil
}

// tenantFor returns the tenant a query is confined to: the context's tenant
// outside a transaction, the transaction's inside one.
//
// The transaction fixes the tenant (ADR-0025). Under RowLevelSecurityNative
// the engine is already filtering by the tenant confineTx set on that
// connection, and under DatabasePerTenant the pool was chosen for it, so a
// query built inside the transaction cannot be honoured for another tenant
// without silently crossing tenants — it fails with ErrTenantMismatch
// instead. A context that resolves no tenant at all inherits the
// transaction's: the caller already named it when opening the transaction,
// and asking twice is what broke DatabasePerTenant in the first cut of this
// fix (a ForTx with a plain context failed where the pool alone had sufficed).
func (r *TenantRouter) tenantFor(ctx context.Context, tx *Tx) (string, error) {
	if tx == nil || tx.tenantID == "" {
		return r.ResolveTenant(ctx)
	}
	if fromCtx := r.resolver(ctx); fromCtx != "" && fromCtx != tx.tenantID {
		return "", fmt.Errorf("%w: the query's context resolves tenant %q but the transaction was opened for tenant %q",
			ErrTenantMismatch, fromCtx, tx.tenantID)
	}
	return tx.tenantID, nil
}

// confineTx confines an open transaction to the tenant its context resolves,
// for this router's strategy. It is what makes a *Tx carry its tenant, and it
// is the ONE place that does — [TenantRouter.Tx] calls it, and so does
// [Client.BeginTx] when the client is this router's BaseClient.
//
// A transaction that is already confined is left alone, so the two callers
// compose: router.Tx over a stamped BaseClient confines once, in BeginTx, and
// emits set_config once. A context with NO tenant leaves the transaction as
// it was — there is nobody to confine it to — and it is the caller's job to
// have refused that earlier when it matters (router.Tx does). A non-empty
// tenant that fails validation is an error, never a bare transaction:
// nothing here downgrades silently.
//
// Under RowLevelSecurityNative this is where `set_config('<NativeRLSVar>',
// <tenant>, true)` runs — SET LOCAL, scoped to the transaction — so the
// engine's policies filter every statement the transaction runs, raw SQL
// included. Under the other strategies the confinement is applied per
// statement by applyTenantConfinement, from the router and tenant recorded
// here.
func (r *TenantRouter) confineTx(ctx context.Context, tx *Tx) error {
	if tx.router != nil {
		return nil
	}
	tenantID := r.resolver(ctx)
	if tenantID == "" {
		return nil
	}
	if !validTenantID.MatchString(tenantID) {
		return fmt.Errorf("invalid tenant_id: %s", tenantID)
	}
	if r.config.Strategy == RowLevelSecurityNative {
		if tx.client.dialect.Name() != "postgres" {
			return fmt.Errorf("%w: RowLevelSecurityNative requires PostgreSQL, got dialect %q",
				ErrUnsupportedFeature, tx.client.dialect.Name())
		}
		if _, err := tx.tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", r.config.defaultNativeRLSVar(), tenantID); err != nil {
			return fmt.Errorf("native rls: set_config: %w", err)
		}
	}
	tx.router = r
	tx.tenantID = tenantID
	return nil
}

// verifyNativePolicy confirms, once per table, that the engine enforces
// row-level security on it: pg_class.relrowsecurity is on and at least one
// pg_policy exists. Anything else — disabled, no policy, a catalog that
// cannot be read — is ErrRLSNotEnforced, and the query that asked does not
// run (A8 S8, RLS-04). Before this, a Native router served rows over a
// database whose policies were never installed, without a word; the only
// check was quarktenant.VerifyRLSPolicies, which nothing called at boot.
// That function remains the detailed preflight (policy name, FORCE, what the
// predicate says); this is the floor under it.
func (r *TenantRouter) verifyNativePolicy(ctx context.Context, client *Client, table string) error {
	if r.config.SkipPolicyVerification {
		return nil
	}
	r.verifiedMu.Lock()
	ok := r.verified[table]
	r.verifiedMu.Unlock()
	if ok {
		return nil
	}
	if err := client.guard.ValidateIdentifier(table); err != nil {
		return err
	}
	var enabled bool
	var policies int
	err := client.db.QueryRowContext(ctx, `
		SELECT c.relrowsecurity, (SELECT count(*) FROM pg_policy p WHERE p.polrelid = c.oid)
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname = $1 AND c.relkind = 'r' AND n.nspname = current_schema()`, table).Scan(&enabled, &policies)
	switch {
	case err != nil:
		return fmt.Errorf("%w: cannot confirm the policies on %q from the catalog (%v); set TenantConfig.SkipPolicyVerification to serve anyway",
			ErrRLSNotEnforced, table, err)
	case !enabled:
		return fmt.Errorf("%w: table %q has row-level security disabled — run install-rls-policies (quarktenant.InstallRLSPolicies)",
			ErrRLSNotEnforced, table)
	case policies == 0:
		return fmt.Errorf("%w: table %q has row-level security enabled but no policy — run install-rls-policies (quarktenant.InstallRLSPolicies)",
			ErrRLSNotEnforced, table)
	}
	r.verifiedMu.Lock()
	if r.verified == nil {
		r.verified = map[string]bool{}
	}
	r.verified[table] = true
	r.verifiedMu.Unlock()
	return nil
}

// GetClient resolves the tenant ID from the context and returns the corresponding Client.
// It implements the ClientProvider interface so it can be used with For[T].
func (r *TenantRouter) GetClient(ctx context.Context) (*Client, error) {
	tenantID, err := r.ResolveTenant(ctx)
	if err != nil {
		return nil, err
	}

	switch r.config.Strategy {
	case DatabasePerTenant:
		return r.getOrCreateCached(tenantID)
	case SchemaPerTenant, RowLevelSecurityClient, RowLevelSecurityNative:
		if r.config.BaseClient == nil {
			return nil, errors.New("BaseClient must be provided for SchemaPerTenant, RowLevelSecurityClient or RowLevelSecurityNative strategies")
		}
		// The third door fails closed like the other two (A8 S8, RLS-02):
		// a Native router over an engine with no native RLS used to hand
		// back the base client here, and a read through it returned every
		// tenant's rows — the silent degradation the strategy claims not
		// to have.
		if r.config.Strategy == RowLevelSecurityNative && r.config.BaseClient.dialect.Name() != "postgres" {
			return nil, fmt.Errorf("%w: RowLevelSecurityNative requires PostgreSQL, got dialect %q",
				ErrUnsupportedFeature, r.config.BaseClient.dialect.Name())
		}
		return r.config.BaseClient, nil
	default:
		return nil, fmt.Errorf("unknown tenant strategy: %v", r.config.Strategy)
	}
}

func (r *TenantRouter) getOrCreateCached(tenantID string) (*Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Check if already in cache
	if elem, ok := r.cache[tenantID]; ok {
		r.lruList.MoveToFront(elem)
		return elem.Value.(*lruEntry).client, nil
	}

	// Create new client via factory (while locked to prevent race conditions on factory execution,
	// though this could block other tenants if factory is slow. For a more robust solution,
	// a singleflight pattern could be used).
	newClient, err := r.factory(tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to create client for tenant %s: %w", tenantID, err)
	}

	// Add to cache
	elem := r.lruList.PushFront(&lruEntry{tenantID: tenantID, client: newClient})
	r.cache[tenantID] = elem

	// Evict if over capacity
	if r.config.MaxCachedPools > 0 && r.lruList.Len() > r.config.MaxCachedPools {
		r.evictOldest()
	}

	return newClient, nil
}

// evictOldest removes the oldest client from the cache and closes its connection.
// Must be called with r.mu locked.
func (r *TenantRouter) evictOldest() {
	elem := r.lruList.Back()
	if elem != nil {
		r.lruList.Remove(elem)
		entry := elem.Value.(*lruEntry)
		delete(r.cache, entry.tenantID)

		// Close the underlying sql.DB connection to prevent leaks
		if entry.client != nil && entry.client.db != nil {
			// Do this in a goroutine to not block the current lock
			go func(db interface{ Close() error }, tid string) {
				_ = db.Close()
			}(entry.client.db, entry.tenantID)
		}
	}
}

// ActiveTenants returns a list of active tenant connections in the cache.
func (r *TenantRouter) ActiveTenants() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	active := make([]string, 0, len(r.cache))
	for k := range r.cache {
		active = append(active, k)
	}
	return active
}
