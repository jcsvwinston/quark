// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package caching is the code of the guide "Caching in production": a
// client with a cache store, the two reads worth caching, the writes that
// invalidate them and the one that does not, a store that counts what Quark
// asks of it, and stores whose backend is down.
package caching

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)

// Product is the catalogue row this service caches: read on every page
// view, written by an operator a few times a day. The cache stores rows as
// JSON, so the json tags decide what a hit gives back.
type Product struct {
	ID     int64  `db:"id" pk:"true" json:"id"`
	SKU    string `db:"sku" quark:"unique,not_null" json:"sku"`
	Name   string `db:"name" json:"name"`
	Price  int64  `db:"price_cents" json:"price_cents"`
	Active bool   `db:"active" json:"active"`
}

// Open builds the one client the service shares. Quark wraps the store with
// singleflight, TTL jitter and early refresh on its own; cross-instance
// coordination is opt-in and only works on a store with a lock (the Redis
// store has one). Raw statements are allowed for Deactivate below.
func Open(dsn string, store quark.CacheStore, obs quark.QueryObserver) (*quark.Client, error) {
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	return quark.New("sqlite", dsn,
		quark.WithCacheStore(store),
		quark.WithCacheJitter(0.2),
		quark.WithCacheXFetchBeta(1),
		quark.WithCacheCrossInstance(),
		quark.WithQueryObserver(obs),
		quark.WithLimits(limits),
	)
}

// ActiveProducts is the catalogue page: the hottest read and the one worth
// caching. The table tag "products" is what every write drops, so it stays
// in the list when custom tags are added; "catalogue" is a tag the service
// drops by hand.
func ActiveProducts(ctx context.Context, client *quark.Client) ([]Product, error) {
	return quark.For[Product](ctx, client).
		Where("active", "=", true).
		OrderBy("name", "ASC").
		Limit(200).
		Cache(5*time.Minute, "products", "catalogue").
		List()
}

// ProductByID is the detail page, cached under the row tag alone,
// "<table>:<pk>". Every write drops the table tag, so an entry that carried
// it too would go with every write to the table; under the row tag alone it
// goes only when a write that knows this key runs. The price: UpdateMap,
// DeleteBy and raw SQL do not know keys and leave this entry in place.
func ProductByID(ctx context.Context, client *quark.Client, id int64) (Product, error) {
	return quark.For[Product](ctx, client).
		Cache(10*time.Minute, "products:"+strconv.FormatInt(id, 10)).
		Find(id)
}

// Reprice is the operator's write. UpdateFields knows the key, so once the
// UPDATE has run Quark drops "products" and "products:<id>" — the catalogue
// and this product's detail page — and nothing else is needed.
func Reprice(ctx context.Context, client *quark.Client, id, priceCents int64) error {
	p := Product{ID: id, Price: priceCents}
	_, err := quark.For[Product](ctx, client).UpdateFields(&p, "price_cents")
	return err
}

// Import loads a catalogue in one transaction. The tags are dropped when
// each INSERT runs and again when the transaction commits, so a request
// that read and cached the old catalogue between the two does not leave it
// cached past the commit.
func Import(ctx context.Context, client *quark.Client, products []*Product) error {
	return client.Tx(ctx, func(tx *quark.Tx) error {
		for _, p := range products {
			if err := quark.ForTx[Product](ctx, tx).Create(p); err != nil {
				return err
			}
		}
		return nil
	})
}

// Deactivate is a raw statement. Quark does not parse raw SQL for the tables
// it writes, so it drops no tag: a cached ActiveProducts would keep the row
// until its TTL ended. The service drops the table tag itself.
func Deactivate(ctx context.Context, client *quark.Client, store quark.CacheStore, sku string) error {
	if err := client.Exec(ctx, "UPDATE products SET active = 0 WHERE sku = ?", sku); err != nil {
		return err
	}
	return store.InvalidateTags(ctx, "products")
}

// LockingStore is a store with the lock cross-instance coordination needs.
// The memory and Redis stores are both one.
type LockingStore interface {
	quark.CacheStore
	quark.CacheLocker
}

// Counting wraps a store and counts what Quark asks of it: a hit is a Get
// that returned bytes, a miss one that did not. Embedding the store forwards
// its lock too — Quark finds the lock by a type assertion on the store it
// was given, so a wrapper that hid it would switch cross-instance
// coordination off without a word.
type Counting struct {
	LockingStore
	Hits, Misses, Sets, Invalidations atomic.Int64
}

func (c *Counting) Get(ctx context.Context, key string) ([]byte, error) {
	b, err := c.LockingStore.Get(ctx, key)
	if err != nil {
		c.Misses.Add(1)
	} else {
		c.Hits.Add(1)
	}
	return b, err
}

func (c *Counting) Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	c.Sets.Add(1)
	return c.LockingStore.Set(ctx, key, val, ttl, tags...)
}

func (c *Counting) InvalidateTags(ctx context.Context, tags ...string) error {
	c.Invalidations.Add(1)
	return c.LockingStore.InvalidateTags(ctx, tags...)
}

// Statements counts the SELECTs that reached the database. A read answered
// from the cache runs no statement and reports no event, so for a cached
// read the hit ratio is 1 - Selects/calls.
type Statements struct{ Selects atomic.Int64 }

func (s *Statements) ObserveQuery(e quark.QueryEvent) {
	if e.Operation == "SELECT" && e.Error == nil {
		s.Selects.Add(1)
	}
}

// ErrCacheDown is what a store returns when its backend is unreachable.
var ErrCacheDown = errors.New("cache backend unreachable")

// Down is a store whose backend is unreachable: every call fails. Quark
// treats a failed Get as a miss and a failed Set as a warning in the log,
// so every read answers from the database and the caller sees no error;
// nothing is served stale, and nothing is cached until the backend returns.
type Down struct{}

func (Down) Get(context.Context, string) ([]byte, error)                         { return nil, ErrCacheDown }
func (Down) Set(context.Context, string, []byte, time.Duration, ...string) error { return ErrCacheDown }
func (Down) Delete(context.Context, string) error                                { return ErrCacheDown }
func (Down) InvalidateTags(context.Context, ...string) error                     { return ErrCacheDown }

// HalfDown is the worse outage: reads and writes to the store work and
// invalidation does not. Quark does not check the error InvalidateTags
// returns, so a write succeeds, the entries it should have dropped stay,
// and cached reads answer with the old rows until their TTL ends.
type HalfDown struct{ quark.CacheStore }

func (HalfDown) InvalidateTags(context.Context, ...string) error { return ErrCacheDown }
