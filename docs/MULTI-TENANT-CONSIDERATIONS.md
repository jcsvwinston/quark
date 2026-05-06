# Multi-Tenant Considerations for Quark ORM

This document outlines the design patterns, security considerations, and implementation strategies for building multi-tenant applications with Quark ORM.

## Overview

Quark supports three multi-tenant isolation strategies, natively integrated into its `TenantRouter`:

1. **Row-Level Security (RLS)** - Single database, transparent `WHERE tenant_id = ?` injection.
2. **Schema-per-Tenant** - Single database, transparent schema prefixes (e.g. `tenant_a.table`).
3. **Database-per-Tenant** - Complete isolation with separate databases per tenant, using an LRU eviction cache to manage active pools.

**GoFrame Note**: The framework allows choosing the best strategy depending on scale. Strategy #3 provides best isolation but high connection overhead. Strategy #2 provides logical isolation with shared connections. Strategy #1 provides lowest overhead.

---

## Architecture: TenantRouter

### Core Components

```
┌─────────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   HTTP Handler  │────▶│  TenantRouter    │────▶│  Tenant DB      │
│                 │     │                  │     │  (or shared)    │
└─────────────────┘     └──────────────────┘     └─────────────────┘
        │                        │
        │              ┌─────────┴─────────┐
        │              │                   │
        ▼              ▼                   ▼
   context.Context   Resolver          Strategy Config
   (carries tenant_id)                 (LRU, Base DB, etc)
```

### TenantRouter

The `TenantRouter` implements the `ClientProvider` interface, allowing `quark.For[T]` to seamlessly route queries:

```go
type TenantRouter struct {
    config    TenantConfig                              // Defines Strategy, LRU limits, etc.
    resolver  func(ctx context.Context) string          // Extracts tenant_id from context
    factory   func(tenantID string) (*Client, error)    // Creates clients (Strategy #3 only)
    cache     map[string]*list.Element                  // Connection cache (LRU)
    // ...
}
```

---

## Implementation

### 1. Setup

```go
// main.go
router := quark.NewTenantRouter(
    // Resolver: Extract tenant_id from context
    func(ctx context.Context) string {
        tenantID, _ := ctx.Value("tenant_id").(string)
        return tenantID
    },
    
    // Factory: Create connection for each tenant
    func(tenantID string) (*quark.Client, error) {
        // Dynamic DSN per tenant
        dsn := fmt.Sprintf("postgres://user:pass@localhost/%s?sslmode=disable", tenantID)
        
        db, err := sql.Open("postgres", dsn)
        if err != nil {
            return nil, err
        }
        
        // Per-tenant connection pool limits
        // Note: These can now be set via WithMaxOpenConns, WithMaxIdleConns, WithConnMaxLifetime options
        // when creating the client
        
        return quark.New("postgres", dsn)
    },
)
```

### 2. Middleware (Tenant Injection)

```go
func TenantMiddleware(next http.Handler) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // Extract from JWT, subdomain, or header
        tenantID := extractTenantFromRequest(r)
        
        // Inject into context
        ctx := context.WithValue(r.Context(), "tenant_id", tenantID)
        
        next.ServeHTTP(w, r.WithContext(ctx))
    }
}
```

### 3. Usage in Handlers

```go
func getUsersHandler(router *quark.TenantRouter) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        ctx := r.Context()
        
        // Quark automatically:
        // 1. Reads tenant_id from context ("acme_corp")
        // 2. Looks up/creates connection to DB "acme_corp"
        // 3. Executes query on that isolated database
        users, err := quark.For[User](ctx, router).List()
        if err != nil {
            http.Error(w, err.Error(), 500)
            return
        }
        
        json.NewEncoder(w).Encode(users)
    }
}
```

---

## Security Considerations

### Tenant ID Validation

**CRITICAL**: Never allow SQL injection in database names.

```go
var validTenantID = regexp.MustCompile(`^[a-z0-9_-]+$`)

func (r *TenantRouter) For(ctx context.Context) (*Client, error) {
    tenantID := r.resolver(ctx)
    
    if tenantID == "" {
        return nil, errors.New("tenant_id not found in context")
    }
    
    // Sanitization: prevent SQL injection
    if !validTenantID.MatchString(tenantID) {
        return nil, fmt.Errorf("invalid tenant_id: %s", tenantID)
    }
    
    return r.getOrCreate(tenantID)
}
```

### Cross-Tenant Access Prevention

```go
// ❌ NEVER: Direct database access without tenant validation
db.Query("SELECT * FROM users")  // Could access wrong tenant!

// ✅ ALWAYS: Use TenantRouter which enforces tenant isolation
quark.For[User](ctx, router).List()  // Always hits correct tenant DB
```

### Connection Security

- Each tenant's database should have unique credentials
- Use SSL/TLS for all tenant database connections
- Rotate credentials periodically
- Store DSNs in secure vault (HashiCorp Vault, AWS Secrets Manager)

---

## Resource Management

### Connection Pool Limits

```go
factory := func(tenantID string) (*quark.Client, error) {
    db, err := sql.Open("postgres", dsn)
    
    // Per-tenant limits prevent one tenant from exhausting resources
    // Connection pool limits can now be set via options:
    // WithMaxOpenConns, WithMaxIdleConns, WithConnMaxLifetime, WithConnMaxIdleTime
    
    return quark.New("postgres", dsn)
}
```

### Tenant Cache Limits

```go
router := quark.NewTenantRouter(resolver, factory,
    quark.WithMaxCachedTenants(1000),      // LRU eviction when limit reached
    quark.WithClientTTL(30 * time.Minute), // Close inactive connections
    quark.WithPreWarm(false),              // Don't create until needed
)
```

### Health Checks

```go
// Check all active tenant connections
for tenantID, client := range router.ActiveTenants() {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    
    // Simple ping via query
    _, err := quark.For[User](ctx, client).First()
    
    log.Printf("Tenant %s health: %v", tenantID, err)
    cancel()
}
```

---

## Cross-Tenant Operations

### Migration Strategy

When you need to run operations across all tenants:

```go
func MigrateAllTenants(router *quark.TenantRouter, migration Migration) error {
    for tenantID, client := range router.ActiveTenants() {
        ctx := context.WithValue(context.Background(), "tenant_id", tenantID)
        
        // Execute migration on this tenant's database
        err := migration.Run(ctx, client)
        if err != nil {
            return fmt.Errorf("tenant %s: %w", tenantID, err)
        }
    }
    return nil
}
```

### Cross-Tenant Analytics (Read-only)

For analytics that aggregate across tenants:

```go
type AnalyticsAggregator struct {
    router *quark.TenantRouter
}

func (a *AnalyticsAggregator) CountUsersAcrossTenants() (map[string]int64, error) {
    results := make(map[string]int64)
    
    for tenantID, client := range a.router.ActiveTenants() {
        ctx := context.WithValue(context.Background(), "tenant_id", tenantID)
        
        count, _ := quark.For[User](ctx, client).Count()
        results[tenantID] = count
    }
    
    return results, nil
}
```

---

## New Tenant Provisioning

### Automated Setup

```go
func ProvisionNewTenant(tenantID string, router *quark.TenantRouter) error {
    // 1. Validate tenant ID
    if !isValidTenantID(tenantID) {
        return errors.New("invalid tenant ID")
    }
    
    // 2. Create database
    adminDB, _ := sql.Open("postgres", adminDSN)
    _, err := adminDB.Exec(fmt.Sprintf("CREATE DATABASE %s", tenantID))
    if err != nil {
        return err
    }
    
    // 3. Run schema migrations
    client, _ := router.Factory(tenantID)
    err = runMigrations(client)
    if err != nil {
        return err
    }
    
    // 4. Seed initial data
    ctx := context.WithValue(context.Background(), "tenant_id", tenantID)
    seedData(ctx, client)
    
    return nil
}
```

### Schema Template

```sql
-- init/template.sql
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

CREATE TABLE users (
    id BIGSERIAL PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP
);

CREATE INDEX idx_users_email ON users(email) WHERE deleted_at IS NULL;
-- ... additional tables
```

---

## Monitoring & Observability

### Per-Tenant Metrics

```go
// Track queries per tenant
func WithTenantMetrics(router *quark.TenantRouter) quark.Option {
    return func(c *quark.Client) {
        c.observers = append(c.observers, func(ctx context.Context, q quark.QueryInfo) {
            tenantID, _ := ctx.Value("tenant_id").(string)
            
            // Emit metrics: tenant_id, query duration, rows affected
            metrics.Record("db.query", 
                "tenant", tenantID,
                "duration_ms", q.Duration.Milliseconds(),
                "table", q.Table,
            )
        })
    }
}
```

### Tenant-Aware Logging

```go
// Include tenant_id in all logs
func WithTenantLogger(logger *slog.Logger) quark.Option {
    return func(c *quark.Client) {
        c.logger = logger.With("component", "quark")
        
        c.queryObserver = func(ctx context.Context, q quark.QueryInfo) {
            tenantID, _ := ctx.Value("tenant_id").(string)
            
            c.logger.InfoContext(ctx, "query executed",
                "tenant_id", tenantID,
                "sql", q.SQL,
                "duration", q.Duration,
            )
        }
    }
}
```

---

## Testing

### Isolated Test Tenants

```go
func TestWithTenant(t *testing.T) {
    // Create isolated test database
    testTenantID := fmt.Sprintf("test_%s", uuid.New().String())
    
    // Provision
    ProvisionNewTenant(testTenantID, router)
    
    // Run tests
    ctx := context.WithValue(context.Background(), "tenant_id", testTenantID)
    
    user := User{Name: "Test", Email: "test@test.com"}
    err := quark.For[User](ctx, router).Create(&user)
    
    // Cleanup
    defer CleanupTenant(testTenantID)
}
```

### Parallel Tenant Tests

```go
func TestParallelTenants(t *testing.T) {
    var wg sync.WaitGroup
    
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            
            tenantID := fmt.Sprintf("parallel_test_%d", idx)
            ctx := context.WithValue(context.Background(), "tenant_id", tenantID)
            
            // Each goroutine operates on isolated tenant DB
            quark.For[User](ctx, router).Create(&User{Name: "User"})
        }(i)
    }
    
    wg.Wait()
}
```

---

## Best Practices

1. **Never bypass TenantRouter** - Always use `quark.For[T](ctx, router)` not direct `*Client`
2. **Validate tenant IDs** - Strict regex validation before DB name construction
3. **Set connection limits** - Prevent resource exhaustion from a single tenant
4. **Use context deadlines** - Always pass timeouts through context
5. **Monitor per-tenant metrics** - Track query patterns and resource usage per tenant
6. **Regular security audits** - Verify no cross-tenant data leaks
7. **Test with real isolation** - Use separate test databases, not shared with mocks
8. **Document tenant schema** - Version control your tenant database schema
9. **Backup per-tenant** - Implement tenant-specific backup/restore procedures
10. **Graceful degradation** - Handle tenant DB outages without affecting others

---

## Related Documents

- `ARCHITECTURE.md` - Core Quark architecture
- `README.md` - API documentation and examples
- `SECURITY.md` - SQLGuard and security features

---

**Document Version**: 1.0  
**Last Updated**: May 2025  
**Applies to**: Quark v0.1.0+
