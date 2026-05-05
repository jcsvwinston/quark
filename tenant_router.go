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
	// RowLevelSecurity uses a single database connection pool and injects
	// a "WHERE tenant_id = ?" condition to every query.
	RowLevelSecurity
)

// TenantConfig configures the TenantRouter.
type TenantConfig struct {
	Strategy       TenantStrategy
	MaxCachedPools int     // Maximum number of DB connection pools to keep open (for DatabasePerTenant)
	BaseClient     *Client // Used for SchemaPerTenant and RowLevelSecurity
	TenantColumn   string  // Column name for RowLevelSecurity, default is "tenant_id"
}

// DefaultTenantConfig provides sensible defaults.
func DefaultTenantConfig() TenantConfig {
	return TenantConfig{
		Strategy:       DatabasePerTenant,
		MaxCachedPools: 100,
		TenantColumn:   "tenant_id",
	}
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
	case SchemaPerTenant, RowLevelSecurity:
		if r.config.BaseClient == nil {
			return nil, errors.New("BaseClient must be provided for SchemaPerTenant or RowLevelSecurity strategies")
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
