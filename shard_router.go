// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"hash/fnv"
)

// Sharding (F6-7, ADR-0016). ShardRouter partitions data across N shard
// databases (each a *Client) by a shard key supplied per query via context.
// It implements ClientProvider, so For[T](ctx, shardRouter) routes the query
// to the owning shard's Client and runs unchanged. There is no implicit
// cross-shard fan-out, no cross-shard join, and no cross-shard transaction
// (a Tx is bound to the resolved shard's Client) — see ADR-0016.

// ShardResolver extracts the shard key from a context, returning "" when none
// is present. Use [DefaultShardResolver] with [WithShardKey], or supply your
// own (e.g. reading an existing request value).
type ShardResolver func(ctx context.Context) string

// ShardFunc maps a shard-key value to the name of the shard that owns it. It is
// the partitioning policy and the resharding seam — pluggable: hash-mod
// ([HashShardFunc], the default helper), range, geo, or a lookup table.
type ShardFunc func(shardKey string) string

// ShardRouter routes each query to the Client of the shard that owns the
// query's shard key (resolved from context). A query without a shard key in
// context is an error — there is no implicit cross-shard fan-out.
type ShardRouter struct {
	shards   map[string]*Client
	resolve  ShardResolver
	shardFor ShardFunc
}

// NewShardRouter builds a router over a fixed set of shards (name -> Client).
// resolve extracts the shard key from context; shardFor maps that key to a
// shard name. It errors if shards is empty or either function is nil — those
// are setup mistakes worth catching at construction.
func NewShardRouter(shards map[string]*Client, resolve ShardResolver, shardFor ShardFunc) (*ShardRouter, error) {
	if len(shards) == 0 {
		return nil, fmt.Errorf("%w: ShardRouter requires at least one shard", ErrInvalidQuery)
	}
	if resolve == nil || shardFor == nil {
		return nil, fmt.Errorf("%w: ShardRouter requires non-nil resolve and shardFor funcs", ErrInvalidQuery)
	}
	// Defensive copy so a caller mutating its map afterwards can't change
	// routing under live queries.
	cp := make(map[string]*Client, len(shards))
	for name, c := range shards {
		if c == nil {
			return nil, fmt.Errorf("%w: ShardRouter shard %q has a nil Client", ErrInvalidQuery, name)
		}
		cp[name] = c
	}
	return &ShardRouter{shards: cp, resolve: resolve, shardFor: shardFor}, nil
}

// GetClient implements [ClientProvider]: it resolves the shard key from ctx,
// maps it to a shard, and returns that shard's Client. It errors when no shard
// key is in context (no implicit fan-out) or when the mapped shard is unknown.
func (r *ShardRouter) GetClient(ctx context.Context) (*Client, error) {
	key := r.resolve(ctx)
	if key == "" {
		return nil, fmt.Errorf("%w: no shard key in context — set it with WithShardKey; cross-shard fan-out is not supported", ErrInvalidQuery)
	}
	name := r.shardFor(key)
	c, ok := r.shards[name]
	if !ok {
		return nil, fmt.Errorf("%w: shard %q (resolved for key %q) is not registered", ErrInvalidQuery, name, key)
	}
	return c, nil
}

// ShardNames returns the registered shard names (unspecified order). Useful for
// migrating/onboarding every shard. The Clients themselves are not exposed —
// route through For[T] so the shard-key discipline is preserved.
func (r *ShardRouter) ShardNames() []string {
	names := make([]string, 0, len(r.shards))
	for name := range r.shards {
		names = append(names, name)
	}
	return names
}

type shardKeyCtxType struct{}

// WithShardKey returns a context carrying the shard key for the operations run
// with it. A ShardRouter built with DefaultShardResolver reads this value.
func WithShardKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, shardKeyCtxType{}, key)
}

// ShardKeyFromContext returns the shard key set by [WithShardKey], or "".
func ShardKeyFromContext(ctx context.Context) string {
	v, _ := ctx.Value(shardKeyCtxType{}).(string)
	return v
}

// DefaultShardResolver reads the shard key set by [WithShardKey]. Pass it to
// [NewShardRouter] for the common case.
func DefaultShardResolver(ctx context.Context) string { return ShardKeyFromContext(ctx) }

// ShardKeyer is implemented by a model that owns its shard key. It is the model
// hook ADR-0016 anticipated for entity-based routing: the entity returns the
// value its partition is keyed on (its tenant id, user id, region, …), so the
// key-deriving logic lives on the model instead of being repeated at every call
// site. Reads, which carry no entity, keep using [WithShardKey] directly — the
// context stays the uniform routing mechanism; this only populates it from an
// entity. Implement it on the value or pointer you pass to [WithShardKeyOf]:
//
//	func (u User) ShardKey() string { return u.TenantID }
type ShardKeyer interface {
	// ShardKey returns the shard-key value for this entity. An empty string is
	// "no key": routing then fails like a missing [WithShardKey], rather than
	// silently fanning out across shards.
	ShardKey() string
}

// WithShardKeyOf returns a context carrying entity.ShardKey() as the shard key,
// so a write routes to the shard the entity belongs to:
//
//	ctx = quark.WithShardKeyOf(ctx, &user) // user implements ShardKeyer
//	err := quark.For[User](ctx, shardRouter).Create(&user)
//
// It is exactly WithShardKey(ctx, entity.ShardKey()) — a convenience that keeps
// the partition field in one place (the model's ShardKey method). The router is
// unchanged and still unaware of sharding (ADR-0016): routing happens in
// GetClient(ctx), so the key must be in context before For[T] resolves the
// shard — this helper puts it there from the entity.
func WithShardKeyOf(ctx context.Context, entity ShardKeyer) context.Context {
	return WithShardKey(ctx, entity.ShardKey())
}

// HashShardFunc returns a ShardFunc that maps a key to one of shardNames by
// FNV-1a hash modulo the shard count — a stable, uniform default assignment.
// The names are copied, so later mutation of the caller's slice does not change
// routing. With no names it maps everything to "" (GetClient then errors).
func HashShardFunc(shardNames []string) ShardFunc {
	names := append([]string(nil), shardNames...)
	return func(key string) string {
		if len(names) == 0 {
			return ""
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(key))
		return names[h.Sum32()%uint32(len(names))]
	}
}
