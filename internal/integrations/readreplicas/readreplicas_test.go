// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package readreplicas

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jcsvwinston/quark"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
)

// Page is the guide this package is the code of.
const Page = "website/docs/guides/read-replicas-in-production.mdx"

// The replica is a second SQLite file that nothing replicates into: a
// replica behind by every write, which is the worst case of lag and makes
// where each read went visible from its result.
func topology(t *testing.T, migrateReplica bool) (primary, replica string) {
	t.Helper()
	dir := t.TempDir()
	primary = "file:" + filepath.Join(dir, "primary.db")
	replica = "file:" + filepath.Join(dir, "replica.db")
	for dsn, migrate := range map[string]bool{primary: true, replica: migrateReplica} {
		c, err := quark.New("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		if migrate {
			if err := c.Migrate(context.Background(), &Order{}); err != nil {
				t.Fatal(err)
			}
		}
		_ = c.Close()
	}
	return primary, replica
}

func TestWritesLandOnThePrimaryAndReadsRoute(t *testing.T) {
	ctx := context.Background()
	primary, replica := topology(t, true)
	client, err := Open(primary, replica)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	o := Order{Customer: "ada", Total: 4200, Status: "placed"}
	if err := Place(ctx, client, &o); err != nil {
		t.Fatal(err)
	}

	// The confirmation pins its read to the primary and sees the order.
	got, err := Confirmation(ctx, client, o.ID)
	if err != nil {
		t.Fatalf("Confirmation right after Place: %v — Sticky did not pin the read to the primary", err)
	}
	if got.Total != 4200 {
		t.Fatalf("Confirmation returned %+v", got)
	}

	// The history routes to the replica, which has nothing yet: lag is
	// visible as a missing order, not as an error.
	history, err := History(ctx, client, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("History read %d orders from the replica, want 0: the read did not route", len(history))
	}

	// Settle reads inside the transaction, on the primary, and sees its own
	// write.
	settled, err := Settle(ctx, client, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != "settled" {
		t.Fatalf("Settle read back %q, want settled", settled.Status)
	}
}

// A replica that does not answer when the client opens is an error, not a
// degraded start.
func TestReplicaDownAtOpenIsAnError(t *testing.T) {
	primary, _ := topology(t, true)
	client, err := Open(primary, "file:"+filepath.Join(t.TempDir(), "missing", "dir", "replica.db"))
	if err == nil {
		_ = client.Close()
		t.Fatal("Open with an unreachable replica returned a client, want an error")
	}
	if !errors.Is(err, quark.ErrConnection) {
		t.Fatalf("Open with an unreachable replica: %v, want quark.ErrConnection", err)
	}
}

// A read that fails on a replica with anything but a connection failure
// is returned as it failed: a replica whose schema is behind the primary
// surfaces as errors, not as reads quietly served by the primary.
func TestSchemaDriftOnAReplicaIsAnError(t *testing.T) {
	ctx := context.Background()
	primary, replica := topology(t, false)
	client, err := Open(primary, replica)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := Place(ctx, client, &Order{Customer: "ada", Total: 1, Status: "placed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := History(ctx, client, "ada"); err == nil {
		t.Fatal("History against a replica without the table returned rows, want the replica's error")
	}
	if _, err := Confirmation(ctx, client, 1); err != nil {
		t.Fatalf("the pinned read on the primary: %v", err)
	}
}

func TestSplitCountsWhatCouldRoute(t *testing.T) {
	ctx := context.Background()
	primary, replica := topology(t, true)
	split := &Split{}
	client, err := quark.New("sqlite", primary, quark.WithReplicas(replica), quark.WithQueryObserver(split))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	o := Order{Customer: "ada", Total: 1, Status: "placed"}
	if err := Place(ctx, client, &o); err != nil {
		t.Fatal(err)
	}
	if _, err := History(ctx, client, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := Confirmation(ctx, client, o.ID); err != nil {
		t.Fatal(err)
	}
	if r, w := split.Reads.Load(), split.Writes.Load(); r != 2 || w != 0 {
		t.Fatalf("Split counted %d reads and %d writes, want 2 reads; the INSERT reads its key back through a query on SQLite and is not an EXEC", r, w)
	}
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.CheckPage(t, Page, "Opening the client", "readreplicas.go")
	guide.CheckPage(t, Page, "Reads that must see their own writes", "readreplicas.go")
	guide.CheckPage(t, Page, "Transactions stay on the primary", "readreplicas.go")
	guide.CheckPage(t, Page, "Measuring the split", "readreplicas.go")
}
