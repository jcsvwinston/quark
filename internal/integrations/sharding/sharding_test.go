// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package sharding

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jcsvwinston/quark"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
)

// Page is the guide this package is the code of.
const Page = "website/docs/guides/sharding-in-production.mdx"

var names = []string{"eu-1", "eu-2"}

func open(t *testing.T, names []string) (Shards, *quark.ShardRouter) {
	t.Helper()
	dir := t.TempDir()
	dsns := make(map[string]string, len(names))
	for _, n := range names {
		dsns[n] = "file:" + filepath.Join(dir, n+".db")
	}
	shards, router, err := Open(dsns, names)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shards.Close() })
	if err := shards.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return shards, router
}

// tenants returns k tenant names and the shard each hashes to, with at
// least two on the same shard.
func tenants(k int) map[string]string {
	shardFor := quark.HashShardFunc(names)
	out := make(map[string]string, k)
	for i := 0; len(out) < k; i++ {
		tenant := fmt.Sprintf("tenant-%d", i)
		out[tenant] = shardFor(tenant)
	}
	return out
}

func TestRowsLandOnTheShardTheKeyRoutesTo(t *testing.T) {
	ctx := context.Background()
	shards, router := open(t, names)
	placed := tenants(6)
	for tenant := range placed {
		for i := 1; i <= 3; i++ {
			if err := Place(ctx, router, &Order{Tenant: tenant, Customer: "c", Total: int64(i * 100)}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Each shard holds exactly the rows of the tenants that hash to it.
	for tenant, shard := range placed {
		n, err := quark.For[Order](ctx, shards[shard]).Where("tenant_id", "=", tenant).Count()
		if err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("shard %s has %d orders of %s, want 3", shard, n, tenant)
		}
		for other, c := range shards {
			if other == shard {
				continue
			}
			if n, _ := quark.For[Order](ctx, c).Where("tenant_id", "=", tenant).Count(); n != 0 {
				t.Fatalf("shard %s has %d orders of %s, which routes to %s", other, n, tenant, shard)
			}
		}
	}
	if misplaced, err := Audit(ctx, shards, quark.HashShardFunc(names)); err != nil || len(misplaced) != 0 {
		t.Fatalf("Audit: %v %v", misplaced, err)
	}

	for tenant := range placed {
		got, err := OrdersOf(ctx, router, tenant)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("OrdersOf(%s) = %d orders, want 3", tenant, len(got))
		}
	}

	// A read with no key fails; it does not search every shard.
	if _, err := quark.For[Order](ctx, router).Limit(1).List(); !errors.Is(err, quark.ErrInvalidQuery) {
		t.Fatalf("a read with no shard key: %v, want ErrInvalidQuery", err)
	}

	total, err := TotalOrders(ctx, router)
	if err != nil {
		t.Fatal(err)
	}
	if total != 18 {
		t.Fatalf("TotalOrders = %d, want 18", total)
	}
	largest, err := LargestOrders(ctx, router, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(largest) != 4 {
		t.Fatalf("LargestOrders(4) returned %d rows", len(largest))
	}
	for _, o := range largest {
		if o.Total != 300 {
			t.Fatalf("LargestOrders(4) = %+v, want four orders of 300 from across the shards", largest)
		}
	}
}

// A per-shard query with no Limit is a List, and a List with no Limit
// returns at most 100 rows: a scatter over more than that per shard is
// quietly short unless the build sets a Limit.
func TestScatterWithoutLimitIsCappedPerShard(t *testing.T) {
	ctx := context.Background()
	_, router := open(t, names)
	placed := tenants(2)
	for tenant := range placed {
		rows := make([]*Order, 0, 120)
		for i := 0; i < 120; i++ {
			rows = append(rows, &Order{Tenant: tenant, Customer: "c", Total: 1})
		}
		if err := quark.For[Order](quark.WithShardKey(ctx, tenant), router).CreateBatch(rows); err != nil {
			t.Fatal(err)
		}
	}
	total, err := TotalOrders(ctx, router)
	if err != nil {
		t.Fatal(err)
	}
	all, err := quark.ScatterGather(ctx, router, func(q *quark.Query[Order]) *quark.Query[Order] { return q }, quark.ScatterMerge[Order]{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 240 || len(all) != 200 {
		t.Fatalf("ScatterCount says %d rows and a ScatterGather without a Limit returned %d; this test expects 240 and 200 (100 per shard)", total, len(all))
	}
}

func TestRefundStaysOnOneShard(t *testing.T) {
	ctx := context.Background()
	shards, router := open(t, names)
	o := Order{Tenant: "tenant-0", Customer: "c", Total: 900}
	if err := Place(ctx, router, &o); err != nil {
		t.Fatal(err)
	}
	if err := Refund(ctx, router, o.Tenant, o.ID); err != nil {
		t.Fatal(err)
	}
	got, err := quark.For[Order](ctx, shards[quark.HashShardFunc(names)(o.Tenant)]).Find(o.ID)
	if err != nil || got.Total != 0 {
		t.Fatalf("after Refund the order is %+v (%v), want total 0 on its shard", got, err)
	}
}

// Adding a shard with the hash alone moves most tenants; the Directory
// pins the tenants already placed and sends only new ones to the new list.
func TestAddingAShardWithoutMovingData(t *testing.T) {
	ctx := context.Background()
	shards, router := open(t, names)
	for tenant := range tenants(12) {
		if err := Place(ctx, router, &Order{Tenant: tenant, Customer: "c", Total: 1}); err != nil {
			t.Fatal(err)
		}
	}

	grown := append(append([]string(nil), names...), "eu-3")
	moved, err := Audit(ctx, shards, quark.HashShardFunc(grown))
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) == 0 {
		t.Fatal("re-hashing over three names moved no tenant; this test expects the hash to move most of them")
	}

	pinned, err := Placements(ctx, shards)
	if err != nil {
		t.Fatal(err)
	}
	dir := Directory{Pinned: pinned, Hash: quark.HashShardFunc(grown)}
	if misplaced, err := Audit(ctx, shards, dir.ShardFor); err != nil || len(misplaced) != 0 {
		t.Fatalf("Audit with the Directory: %v %v, want no misplaced row", misplaced, err)
	}

	// The new router serves the old tenants from their shards, and the new
	// shard takes the new tenants the hash sends to it.
	third, err := quark.New("sqlite", "file:"+filepath.Join(t.TempDir(), "eu-3.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if err := third.Migrate(ctx, &Order{}); err != nil {
		t.Fatal(err)
	}
	shards["eu-3"] = third
	router, err = quark.NewShardRouter(shards, quark.DefaultShardResolver, dir.ShardFor)
	if err != nil {
		t.Fatal(err)
	}
	for tenant := range pinned {
		got, err := OrdersOf(ctx, router, tenant)
		if err != nil || len(got) != 1 {
			t.Fatalf("OrdersOf(%s) after the shard was added: %d rows, %v", tenant, len(got), err)
		}
	}
	newcomer := ""
	for i := 1000; newcomer == ""; i++ {
		if tenant := fmt.Sprintf("tenant-%d", i); dir.ShardFor(tenant) == "eu-3" {
			newcomer = tenant
		}
	}
	if err := Place(ctx, router, &Order{Tenant: newcomer, Customer: "c", Total: 1}); err != nil {
		t.Fatal(err)
	}
	if n, _ := quark.For[Order](ctx, third).Count(); n != 1 {
		t.Fatalf("the new shard has %d orders after placing %s on it, want 1", n, newcomer)
	}
}

func TestConfinedKeepsTenantsApartInsideAShard(t *testing.T) {
	ctx := context.Background()
	shards, router := open(t, names)
	shardFor := quark.HashShardFunc(names)

	// Two tenants that share a shard.
	var a, b string
	placed := tenants(8)
	keys := make([]string, 0, len(placed))
	for k := range placed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, x := range keys {
		for _, y := range keys {
			if x < y && placed[x] == placed[y] {
				a, b = x, y
			}
		}
	}
	if a == "" {
		t.Fatal("no two tenants share a shard")
	}
	for _, tenant := range []string{a, b} {
		if err := Place(ctx, router, &Order{Tenant: tenant, Customer: "c", Total: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Through the shard router the shard's client answers with every tenant
	// on the shard: a forgotten WHERE is a leak.
	both, err := quark.For[Order](quark.WithShardKey(ctx, a), router).Limit(10).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 2 {
		t.Fatalf("the shard router returned %d orders for the shard of %s and %s; this test expects both tenants' rows", len(both), a, b)
	}

	// Through the tenant router of the shard, only the tenant's.
	confined := NewConfined(shards, shardFor)
	only, err := TenantOrders(WithTenant(ctx, a), confined)
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].Tenant != a {
		t.Fatalf("TenantOrders(%s) = %+v, want the one order of %s", a, only, a)
	}
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.CheckPage(t, Page, "Opening the shards", "sharding.go")
	guide.CheckPage(t, Page, "Routing writes and reads", "sharding.go")
	guide.CheckPage(t, Page, "Scatter queries and their cost", "sharding.go")
	guide.CheckPage(t, Page, "Transactions stay on one shard", "sharding.go")
	guide.CheckPage(t, Page, "Verifying where the rows are", "sharding.go")
	guide.CheckPage(t, Page, "Adding a shard", "sharding.go")
	guide.CheckPage(t, Page, "Several tenants on one shard", "sharding.go")
}
