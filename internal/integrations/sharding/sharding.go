// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package sharding is the code of the guide "Sharding in production": the
// shards and the router over them, writes routed by the entity and reads by
// the key, the migration and the audit the router does not do, a shard
// function that adds a shard without moving data, the transaction that
// stays on one shard, and tenants confined inside their shard.
package sharding

import (
	"context"
	"errors"
	"fmt"

	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)

// Order is partitioned by tenant: every order of a tenant lives on the
// tenant's shard, so a tenant's reads and writes never cross shards.
type Order struct {
	ID       int64  `db:"id" pk:"true"`
	Tenant   string `db:"tenant_id" quark:"not_null"`
	Customer string `db:"customer" quark:"not_null"`
	Total    int64  `db:"total_cents"`
}

// ShardKey makes Order route itself on writes: WithShardKeyOf reads it.
func (o Order) ShardKey() string { return o.Tenant }

// Shards is the operator's map of shard name to client. The router keeps
// its own copy and hands clients out only through For, so the service
// keeps this map for the work the router does not do: migrating and
// closing every shard, and auditing where the rows are.
type Shards map[string]*quark.Client

// Open opens one client per shard and the router over them. The names are
// part of the data placement: HashShardFunc hashes a key onto the names in
// the order given, so the list is configuration kept with the data, never
// a literal that a refactor may reorder.
func Open(dsns map[string]string, names []string) (Shards, *quark.ShardRouter, error) {
	shards := make(Shards, len(dsns))
	for name, dsn := range dsns {
		c, err := quark.New("sqlite", dsn)
		if err != nil {
			_ = shards.Close()
			return nil, nil, fmt.Errorf("shard %s: %w", name, err)
		}
		shards[name] = c
	}
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, quark.HashShardFunc(names))
	if err != nil {
		_ = shards.Close()
		return nil, nil, err
	}
	return shards, router, nil
}

// Migrate runs the schema on every shard. The router has no migrate and
// the CLI has no shard list: a shard that is missing a table fails every
// query routed to it, so this runs before the service takes traffic and
// again, on every shard, each time the model changes.
func (s Shards) Migrate(ctx context.Context) error {
	for name, c := range s {
		if err := c.Migrate(ctx, &Order{}); err != nil {
			return fmt.Errorf("shard %s: %w", name, err)
		}
	}
	return nil
}

// Close closes every shard; the router closes nothing.
func (s Shards) Close() error {
	var errs []error
	for _, c := range s {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}

// Place writes the order on the shard that owns its tenant.
func Place(ctx context.Context, router *quark.ShardRouter, o *Order) error {
	return quark.For[Order](quark.WithShardKeyOf(ctx, o), router).Create(o)
}

// OrdersOf reads a tenant's orders. A read carries no entity, so the key is
// passed by hand; a query with no key fails rather than search every shard.
// The key routes and does not filter: the shard holds every tenant that
// hashes to it, so the query still says whose rows it wants.
func OrdersOf(ctx context.Context, router *quark.ShardRouter, tenant string) ([]Order, error) {
	return quark.For[Order](quark.WithShardKey(ctx, tenant), router).
		Where("tenant_id", "=", tenant).
		OrderBy("id", "DESC").
		Limit(100).
		List()
}

// LargestOrders is a cross-shard read, written as one on purpose: every
// shard runs the query at the same time, each returns its own n largest,
// and the merge keeps the n largest of all. Its cost is one query on every
// shard per call, and the rows of every shard in memory before the merge.
func LargestOrders(ctx context.Context, router *quark.ShardRouter, n int) ([]Order, error) {
	return quark.ScatterGather(ctx, router,
		func(q *quark.Query[Order]) *quark.Query[Order] {
			return q.OrderBy("total_cents", "DESC").Limit(n)
		},
		quark.ScatterMerge[Order]{
			Less:  func(a, b Order) bool { return a.Total > b.Total },
			Limit: n,
		})
}

// TotalOrders sums the count of every shard. The shards are disjoint, so
// the sum is exact; a shard that fails fails the call, never a low total.
func TotalOrders(ctx context.Context, router *quark.ShardRouter) (int64, error) {
	return quark.ScatterCount[Order](ctx, router, nil)
}

// Refund is a transaction on one shard: the router resolves the shard, the
// shard's client opens the transaction, and every statement in it stays
// there. There is no transaction across shards; a change that must touch
// two tenants is two transactions and a reconciliation the service owns.
func Refund(ctx context.Context, router *quark.ShardRouter, tenant string, id int64) error {
	ctx = quark.WithShardKey(ctx, tenant)
	client, err := router.GetClient(ctx)
	if err != nil {
		return err
	}
	return client.Tx(ctx, func(tx *quark.Tx) error {
		o, err := quark.ForTx[Order](ctx, tx).Find(id)
		if err != nil {
			return err
		}
		o.Total = 0
		_, err = quark.ForTx[Order](ctx, tx).UpdateFields(&o, "total_cents")
		return err
	})
}

// Audit reads every shard directly and reports the rows whose tenant the
// function would route elsewhere. It is the check that data landed where
// routing says, and the check to run after a deployment that changed the
// shard list or the function.
func Audit(ctx context.Context, shards Shards, shardFor quark.ShardFunc) ([]string, error) {
	var misplaced []string
	for name, c := range shards {
		err := quark.For[Order](ctx, c).Iter(func(o Order) error {
			if owner := shardFor(o.Tenant); owner != name {
				misplaced = append(misplaced, fmt.Sprintf("order %d of tenant %s is on %s and routes to %s", o.ID, o.Tenant, name, owner))
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("shard %s: %w", name, err)
		}
	}
	return misplaced, nil
}

// Directory is a shard function that adds a shard without moving data. The
// tenants placed before the change keep the shard they are on, recorded
// here when the new shard joins; every other tenant hashes over the new
// list. Replacing the hash's list alone would route most existing tenants
// to a shard that does not have their rows.
type Directory struct {
	Pinned map[string]string // tenant -> the shard it was on when the list changed
	Hash   quark.ShardFunc   // over the new list, for everyone else
}

func (d Directory) ShardFor(tenant string) string {
	if shard, ok := d.Pinned[tenant]; ok {
		return shard
	}
	return d.Hash(tenant)
}

// Placements reads every shard and records which shard each tenant is on:
// the Pinned map of a Directory, taken while the old router still serves.
func Placements(ctx context.Context, shards Shards) (map[string]string, error) {
	pinned := make(map[string]string)
	for name, c := range shards {
		err := quark.For[Order](ctx, c).Iter(func(o Order) error {
			pinned[o.Tenant] = name
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("shard %s: %w", name, err)
		}
	}
	return pinned, nil
}

type tenantKey struct{}

// WithTenant carries the tenant the request authenticated.
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// TenantFromContext is the resolver both routers below read.
func TenantFromContext(ctx context.Context) string {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	return tenant
}

// Confined is one tenant router per shard, each confining queries to the
// tenant's rows inside the shard's client. The shard router alone does not
// do that: it hands back the shard's client, and Quark adds the tenant
// predicate only to a query built on a TenantRouter — so when several
// tenants share a shard, the query is built on the tenant router of the
// shard, not on the shard router.
type Confined struct {
	shardFor quark.ShardFunc
	routers  map[string]*quark.TenantRouter
}

func NewConfined(shards Shards, shardFor quark.ShardFunc) *Confined {
	c := &Confined{shardFor: shardFor, routers: make(map[string]*quark.TenantRouter, len(shards))}
	for name, client := range shards {
		cfg := quark.DefaultTenantConfig()
		cfg.Strategy = quark.RowLevelSecurityClient
		cfg.BaseClient = client
		c.routers[name] = quark.NewTenantRouter(cfg, TenantFromContext, nil)
	}
	return c
}

// Router returns the tenant router of the shard that owns the tenant in
// ctx; the query is built on it.
func (c *Confined) Router(ctx context.Context) (*quark.TenantRouter, error) {
	tenant := TenantFromContext(ctx)
	if tenant == "" {
		return nil, errors.New("no tenant in context")
	}
	router, ok := c.routers[c.shardFor(tenant)]
	if !ok {
		return nil, fmt.Errorf("tenant %s routes to shard %s, which has no router", tenant, c.shardFor(tenant))
	}
	return router, nil
}

// TenantOrders reads the tenant's orders from the tenant's shard, confined
// to the tenant.
func TenantOrders(ctx context.Context, confined *Confined) ([]Order, error) {
	router, err := confined.Router(ctx)
	if err != nil {
		return nil, err
	}
	return quark.For[Order](ctx, router).OrderBy("id", "DESC").Limit(100).List()
}
