// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package caching

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
)

// Page is the guide this package is the code of.
const Page = "website/docs/guides/caching-in-production.mdx"

func open(t *testing.T, store quark.CacheStore) (*quark.Client, *Statements) {
	t.Helper()
	obs := &Statements{}
	client, err := Open("file:"+filepath.Join(t.TempDir(), "catalogue.db"), store, obs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Migrate(context.Background(), &Product{}); err != nil {
		t.Fatal(err)
	}
	return client, obs
}

func seed(t *testing.T, client *quark.Client) []*Product {
	t.Helper()
	products := []*Product{
		{SKU: "A-1", Name: "anvil", Price: 1000, Active: true},
		{SKU: "B-2", Name: "bellows", Price: 500, Active: true},
		{SKU: "C-3", Name: "chisel", Price: 300, Active: false},
	}
	if err := Import(context.Background(), client, products); err != nil {
		t.Fatal(err)
	}
	return products
}

// The catalogue is read from the database once and from the cache after,
// until a write drops it: Reprice drops the table tag and the row tag, so
// both the catalogue and the detail page see the new price on their next
// read, and the other product's detail page stays cached.
func TestWritesInvalidateWhatTheyChange(t *testing.T) {
	ctx := context.Background()
	store := &Counting{LockingStore: memory.New()}
	defer store.LockingStore.(*memory.Store).Close()
	client, obs := open(t, store)
	products := seed(t, client)
	anvil, bellows := products[0], products[1]

	for i := 0; i < 3; i++ {
		got, err := ActiveProducts(ctx, client)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("catalogue read %d: %d active products, want 2", i, len(got))
		}
	}
	if n := obs.Selects.Load(); n != 1 {
		t.Fatalf("three catalogue reads ran %d SELECTs, want 1: the cache did not serve the other two", n)
	}
	if h, m := store.Hits.Load(), store.Misses.Load(); h != 2 || m != 1 {
		t.Fatalf("store saw %d hits and %d misses, want 2 and 1", h, m)
	}

	if _, err := ProductByID(ctx, client, anvil.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ProductByID(ctx, client, bellows.ID); err != nil {
		t.Fatal(err)
	}
	before := obs.Selects.Load()

	if err := Reprice(ctx, client, anvil.ID, 1200); err != nil {
		t.Fatal(err)
	}
	got, err := ProductByID(ctx, client, anvil.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Price != 1200 {
		t.Fatalf("anvil after Reprice: price %d, want 1200 — the row tag was not dropped", got.Price)
	}
	list, err := ActiveProducts(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Price != 1200 {
		t.Fatalf("catalogue after Reprice: anvil at %d, want 1200 — the table tag was not dropped", list[0].Price)
	}
	if _, err := ProductByID(ctx, client, bellows.ID); err != nil {
		t.Fatal(err)
	}
	if n := obs.Selects.Load() - before; n != 2 {
		t.Fatalf("after Reprice the three reads ran %d SELECTs, want 2: anvil's page and the catalogue re-read, bellows' page stayed cached", n)
	}

	// The price of the row tag alone: a write that does not know the key
	// drops the table tag only, and bellows' page keeps the old price.
	if _, err := quark.For[Product](ctx, client).Where("sku", "=", "B-2").UpdateMap(map[string]any{"price_cents": 700}); err != nil {
		t.Fatal(err)
	}
	got, err = ProductByID(ctx, client, bellows.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Price != 500 {
		t.Fatalf("bellows' page after an UpdateMap: price %d; this test expects the stale 500 — the row tag is not dropped by a WHERE-based write", got.Price)
	}
}

// A write in a transaction drops its tags twice: when the statement runs
// and when the transaction commits. A read made from outside between the
// two cached the old rows; the commit drops them again, so the next read
// sees the imported rows.
func TestTransactionDropsTagsOnCommit(t *testing.T) {
	ctx := context.Background()
	store := &Counting{LockingStore: memory.New()}
	defer store.LockingStore.(*memory.Store).Close()
	client, _ := open(t, store)
	seed(t, client)
	if _, err := ActiveProducts(ctx, client); err != nil {
		t.Fatal(err)
	}

	var readInside int
	err := client.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[Product](ctx, tx).Create(&Product{SKU: "D-4", Name: "drill", Price: 900, Active: true}); err != nil {
			return err
		}
		// Another request, outside the transaction, reads and caches what is
		// committed so far: two products.
		got, err := ActiveProducts(ctx, client)
		readInside = len(got)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if readInside != 2 {
		t.Fatalf("read outside the transaction saw %d products, want the 2 committed before it", readInside)
	}
	got, err := ActiveProducts(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("after the commit the catalogue has %d products, want 3: the commit did not drop the tag again", len(got))
	}
}

// Raw SQL drops nothing. Deactivate drops the table tag itself; without
// that line the catalogue keeps the deactivated product until its TTL ends.
func TestRawSQLDropsNothing(t *testing.T) {
	ctx := context.Background()
	store := &Counting{LockingStore: memory.New()}
	defer store.LockingStore.(*memory.Store).Close()
	client, _ := open(t, store)
	seed(t, client)
	if _, err := ActiveProducts(ctx, client); err != nil {
		t.Fatal(err)
	}

	if err := client.Exec(ctx, "UPDATE products SET active = 0 WHERE sku = ?", "B-2"); err != nil {
		t.Fatal(err)
	}
	stale, err := ActiveProducts(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 2 {
		t.Fatalf("after a raw UPDATE the cached catalogue has %d products; this test expects the stale 2", len(stale))
	}

	if err := Deactivate(ctx, client, store, "A-1"); err != nil {
		t.Fatal(err)
	}
	fresh, err := ActiveProducts(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 0 {
		t.Fatalf("after Deactivate dropped the tag the catalogue has %d products, want 0", len(fresh))
	}
}

// With the backend down every read answers from the database, correctly
// and without an error; nothing is cached.
func TestBackendDownReadsFromTheDatabase(t *testing.T) {
	ctx := context.Background()
	client, obs := open(t, Down{})
	seed(t, client)
	for i := 0; i < 3; i++ {
		got, err := ActiveProducts(ctx, client)
		if err != nil {
			t.Fatalf("read %d with the cache down: %v", i, err)
		}
		if len(got) != 2 {
			t.Fatalf("read %d with the cache down: %d products, want 2", i, len(got))
		}
	}
	if n := obs.Selects.Load(); n != 3 {
		t.Fatalf("three reads with the cache down ran %d SELECTs, want 3", n)
	}
}

// With invalidation down the write succeeds and the cached reads keep the
// old rows: Quark does not check the error InvalidateTags returns.
func TestInvalidationDownServesStaleRows(t *testing.T) {
	ctx := context.Background()
	inner := memory.New()
	defer inner.Close()
	client, _ := open(t, HalfDown{CacheStore: inner})
	products := seed(t, client)
	if _, err := ProductByID(ctx, client, products[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := Reprice(ctx, client, products[0].ID, 1200); err != nil {
		t.Fatalf("Reprice with invalidation down returned %v; Quark does not surface that error", err)
	}
	got, err := ProductByID(ctx, client, products[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Price != 1000 {
		t.Fatalf("detail page after Reprice with invalidation down: price %d; this test expects the stale 1000", got.Price)
	}
}

// What a hit gives back is the JSON of the rows: a field the JSON leaves
// out comes back zero, however the database has it.
func TestHitReturnsTheJSONOfTheRows(t *testing.T) {
	type Secret struct {
		ID    int64  `db:"id" pk:"true" json:"id"`
		Token string `db:"token" json:"-"`
	}
	ctx := context.Background()
	inner := memory.New()
	defer inner.Close()
	client, _ := open(t, inner)
	if err := client.Migrate(ctx, &Secret{}); err != nil {
		t.Fatal(err)
	}
	if err := quark.For[Secret](ctx, client).Create(&Secret{Token: "t-1"}); err != nil {
		t.Fatal(err)
	}
	first, err := quark.For[Secret](ctx, client).Cache(time.Minute).List()
	if err != nil {
		t.Fatal(err)
	}
	second, err := quark.For[Secret](ctx, client).Cache(time.Minute).List()
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Token != "t-1" || second[0].Token != "" {
		t.Fatalf("miss gave token %q and hit gave %q: want the row's value on the miss and the zero value on the hit", first[0].Token, second[0].Token)
	}
}

func TestDownIsAStore(t *testing.T) {
	var s quark.CacheStore = Down{}
	if _, err := s.Get(context.Background(), "k"); !errors.Is(err, ErrCacheDown) {
		t.Fatal(err)
	}
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.CheckPage(t, Page, "Opening the client", "caching.go")
	guide.CheckPage(t, Page, "Choosing what to cache", "caching.go")
	guide.CheckPage(t, Page, "Invalidation on writes", "caching.go")
	guide.CheckPage(t, Page, "Observing hits and misses", "caching.go")
	guide.CheckPage(t, Page, "When the cache backend is down", "caching.go")
}
