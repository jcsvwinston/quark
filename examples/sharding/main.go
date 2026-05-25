// Sharding example (F6-7, ADR-0016).
//
// A ShardRouter partitions data horizontally across N shard databases. Each
// row lives in exactly one shard, chosen by a shard key supplied per query via
// context. The router implements quark.ClientProvider, so quark.For[T](ctx,
// router) routes the query to the owning shard's *Client and runs unchanged —
// the rest of the ORM is unaware sharding exists.
//
// This example is self-contained: it uses two file-based SQLite databases as
// shards so it runs with `go run ./examples/sharding/main.go` and no Docker.
// Sharding is engine-agnostic — to shard across real Postgres/MySQL instances,
// open each shard with that driver/DSN; the routing code is identical.
//
// Hard limits (ADR-0016): no implicit cross-shard fan-out (a query without a
// shard key is an error), no cross-shard joins, and no cross-shard transactions
// (a Tx is bound to one shard's Client). Design the model so each operation
// stays inside a single shard.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

// Account lives in exactly one shard, chosen by its owner (the shard key here).
type Account struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Balance int64  `db:"balance"`
}

func main() {
	ctx := context.Background()

	// Stable, sorted shard names: HashShardFunc maps a key to a shard by index,
	// so a deterministic order keeps the key -> shard assignment reproducible.
	shardNames := []string{"shard-a", "shard-b"}
	sort.Strings(shardNames)

	// Two shard databases. In production these are separate Postgres/MySQL
	// instances; here two SQLite files keep the example self-contained.
	shards := make(map[string]*quark.Client, len(shardNames))
	var closers []func()
	defer func() {
		for _, fn := range closers {
			fn()
		}
	}()

	for _, name := range shardNames {
		file := name + ".db"
		_ = os.Remove(file) // deterministic re-runs
		c, err := quark.New("sqlite", file)
		if err != nil {
			log.Fatalf("open %s: %v", name, err)
		}
		// Close the client first, then remove its files (order matters).
		closers = append(closers, func() {
			_ = c.Close()
			_ = os.Remove(file)
		})
		if err := c.Migrate(ctx, &Account{}); err != nil {
			log.Fatalf("migrate %s: %v", name, err)
		}
		shards[name] = c
	}

	// Build the router: the shard key is read from context (WithShardKey) and
	// mapped to a shard by FNV-1a hash mod N. Supply your own ShardFunc for
	// range / geo / lookup-table partitioning instead of HashShardFunc.
	router, err := quark.NewShardRouter(shards, quark.DefaultShardResolver, quark.HashShardFunc(shardNames))
	if err != nil {
		log.Fatal(err)
	}

	// Write one account per owner. Each write carries its owner's shard key, so
	// the router sends it to exactly one shard.
	owners := []string{"alice", "bob", "carol", "dave"}
	fmt.Println("Creating accounts (routed by owner shard key):")
	for _, owner := range owners {
		shardCtx := quark.WithShardKey(ctx, owner)
		acct := &Account{Owner: owner, Balance: 100}
		if err := quark.For[Account](shardCtx, router).Create(acct); err != nil {
			log.Fatalf("create %s: %v", owner, err)
		}
		fmt.Printf("  created %-6s (id=%d)\n", owner, acct.ID)
	}

	// Read back per shard key — each read routes to the owning shard.
	fmt.Println("Reading each owner back through the router:")
	for _, owner := range owners {
		shardCtx := quark.WithShardKey(ctx, owner)
		got, err := quark.For[Account](shardCtx, router).Where("owner", "=", owner).List()
		if err != nil {
			log.Fatalf("read %s: %v", owner, err)
		}
		fmt.Printf("  %-6s -> %d row(s) on its shard\n", owner, len(got))
	}

	// Prove the partitioning: count rows directly on each shard. The four owners
	// are distributed across the two shards by the hash.
	fmt.Println("Rows per shard (data is disjoint):")
	total := int64(0)
	for _, name := range shardNames {
		// Direct client (not the router): counting one shard needs no shard key.
		n, err := quark.For[Account](ctx, shards[name]).Count()
		if err != nil {
			log.Fatal(err)
		}
		total += n
		fmt.Printf("  %-8s %d\n", name, n)
	}
	fmt.Printf("  total    %d (across %d shards)\n", total, len(shards))

	// A request WITHOUT a shard key is rejected — there is no implicit
	// cross-shard fan-out (ADR-0016). The router surfaces this at routing time:
	if _, err := router.GetClient(ctx); err != nil {
		fmt.Printf("no shard key -> router refuses to route:\n  %v\n", err)
	} else {
		log.Fatal("expected routing without a shard key to fail, but it succeeded")
	}
	// (quark.For[Account](ctx, router).<op>() against a keyless context also
	// returns an error rather than touching every shard.)
}
