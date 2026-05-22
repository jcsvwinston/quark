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
// Deprecated: use RowLevelSecurityClient. The alias is scheduled for
// removal in v1.0. The name change clarifies that this strategy is
// client-side WHERE injection, not engine-enforced RLS — see ADR-0012
// and (when available) RowLevelSecurityNative for the engine-enforced
// PostgreSQL variant introduced in Fase 5.
const RowLevelSecurity = RowLevelSecurityClient

// TenantConfig configures the TenantRouter.
type TenantConfig struct {
	Strategy       TenantStrategy
	MaxCachedPools int     // Maximum number of DB connection pools to keep open (for DatabasePerTenant)
	BaseClient     *Client // Used for SchemaPerTenant, RowLevelSecurityClient and RowLevelSecurityNative
	TenantColumn   string  // Column name for RowLevelSecurityClient, default is "tenant_id"

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
	return &TenantRouter{
		config:   config,
		resolver: resolver,
		factory:  factory,
		cache:    make(map[string]*list.Element),
		lruList:  list.New(),
	}
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
