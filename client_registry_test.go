// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

type registryFixtureA struct {
	ID int64 `db:"id" pk:"true"`
}
type registryFixtureB struct {
	ID    int64  `db:"id" pk:"true"`
	Label string `db:"label" quark:"not_null"`
}

func newRegistryClient(t *testing.T) *quark.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestClient_RegisterModel_BasicHappyPath: registering two models
// returns nil; RegisteredModels returns them in order.
func TestClient_RegisterModel_BasicHappyPath(t *testing.T) {
	c := newRegistryClient(t)
	if err := c.RegisterModel(&registryFixtureA{}, &registryFixtureB{}); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	got := c.RegisteredModels()
	if len(got) != 2 {
		t.Fatalf("want 2 registered, got %d", len(got))
	}
	// Type-check the registration order.
	if _, ok := got[0].(*registryFixtureA); !ok {
		t.Errorf("got[0]: want *registryFixtureA, got %T", got[0])
	}
	if _, ok := got[1].(*registryFixtureB); !ok {
		t.Errorf("got[1]: want *registryFixtureB, got %T", got[1])
	}
}

// TestClient_RegisterModel_AppendsAcrossCalls: multiple
// RegisterModel calls APPEND rather than replace. Documented
// behaviour — pin it.
func TestClient_RegisterModel_AppendsAcrossCalls(t *testing.T) {
	c := newRegistryClient(t)
	_ = c.RegisterModel(&registryFixtureA{})
	_ = c.RegisterModel(&registryFixtureB{})
	if got := len(c.RegisteredModels()); got != 2 {
		t.Errorf("want 2 after two RegisterModel calls, got %d", got)
	}
}

// TestClient_RegisterModel_DoesNotDeduplicate: same model
// registered twice shows up twice. Documented design choice — pin
// it so a future "smart" dedup doesn't silently change behaviour.
func TestClient_RegisterModel_DoesNotDeduplicate(t *testing.T) {
	c := newRegistryClient(t)
	_ = c.RegisterModel(&registryFixtureA{})
	_ = c.RegisterModel(&registryFixtureA{}) // same type
	if got := len(c.RegisteredModels()); got != 2 {
		t.Errorf("registry should NOT dedupe; want 2 after registering twice, got %d", got)
	}
}

// TestClient_RegisterModel_RejectsNil: untyped nil → error.
func TestClient_RegisterModel_RejectsNil(t *testing.T) {
	c := newRegistryClient(t)
	err := c.RegisterModel(nil)
	if err == nil {
		t.Fatalf("RegisterModel(nil): want error, got nil")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error should mention 'nil', got %q", err)
	}
}

// TestClient_RegisterModel_RejectsNonStruct: int → error, and the
// registry is unchanged (no partial registration on failure).
func TestClient_RegisterModel_RejectsNonStruct(t *testing.T) {
	c := newRegistryClient(t)
	// Mix a valid model with an invalid one — the invalid one
	// should fail the whole call AND leave the registry empty
	// (no partial state).
	err := c.RegisterModel(&registryFixtureA{}, 42)
	if err == nil {
		t.Fatalf("RegisterModel(struct, int): want error, got nil")
	}
	if !strings.Contains(err.Error(), "struct") {
		t.Errorf("error should mention 'struct', got %q", err)
	}
	if got := len(c.RegisteredModels()); got != 0 {
		t.Errorf("after partial-failure register, registry should be empty; got %d", got)
	}
}

// TestClient_RegisterModel_ConcurrentSafe: race-detector smoke
// test. Spin up 50 goroutines, each registering one model;
// final count must be 50.
func TestClient_RegisterModel_ConcurrentSafe(t *testing.T) {
	c := newRegistryClient(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.RegisterModel(&registryFixtureA{})
		}()
	}
	wg.Wait()
	if got := len(c.RegisteredModels()); got != 50 {
		t.Errorf("after 50 concurrent registers, want 50; got %d", got)
	}
}

// TestClient_RegisteredModels_ReturnsSnapshot: mutating the
// returned slice doesn't affect the internal registry. Important
// because users may post-process the list and we don't want to
// invite accidental corruption.
func TestClient_RegisteredModels_ReturnsSnapshot(t *testing.T) {
	c := newRegistryClient(t)
	_ = c.RegisterModel(&registryFixtureA{})
	snap := c.RegisteredModels()
	snap[0] = nil // mutate the snapshot
	if c.RegisteredModels()[0] == nil {
		t.Errorf("mutating snapshot should NOT affect internal registry")
	}
}

// TestClient_MigrateRegistered_EmptyRegistryIsNoop: no models
// registered → MigrateRegistered returns nil immediately.
func TestClient_MigrateRegistered_EmptyRegistryIsNoop(t *testing.T) {
	ctx := context.Background()
	c := newRegistryClient(t)
	if err := c.MigrateRegistered(ctx); err != nil {
		t.Errorf("empty registry: want nil, got %v", err)
	}
}

// TestClient_MigrateRegistered_AppliesAllRegistered: register two
// models, MigrateRegistered, then verify both tables exist via
// introspection. The end-to-end happy path.
func TestClient_MigrateRegistered_AppliesAllRegistered(t *testing.T) {
	ctx := context.Background()
	c := newRegistryClient(t)
	if err := c.RegisterModel(&registryFixtureA{}, &registryFixtureB{}); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	if err := c.MigrateRegistered(ctx); err != nil {
		t.Fatalf("MigrateRegistered: %v", err)
	}
	schema, err := c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("IntrospectSchema: %v", err)
	}
	var sawA, sawB bool
	for _, tbl := range schema.Tables {
		if tbl.Name == "registry_fixture_as" {
			sawA = true
		}
		if tbl.Name == "registry_fixture_bs" {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Errorf("after MigrateRegistered, want both tables; sawA=%v sawB=%v", sawA, sawB)
	}
}

// TestClient_PlanMigrationRegistered_EmptyRegistryReturnsEmptyPlan:
// no models → empty Plan, no error. Plays nicely with IsEmpty()
// callers.
func TestClient_PlanMigrationRegistered_EmptyRegistryReturnsEmptyPlan(t *testing.T) {
	ctx := context.Background()
	c := newRegistryClient(t)
	plan, err := c.PlanMigrationRegistered(ctx)
	if err != nil {
		t.Errorf("empty registry: want nil error, got %v", err)
	}
	if !plan.IsEmpty() {
		t.Errorf("empty registry: want empty plan, got %d ops", len(plan.Ops))
	}
}

// TestClient_PlanMigrationRegistered_DelegatesToPlanMigration: with
// models registered, the plan should match what PlanMigration
// would return. Verifies the convenience wrapper isn't dropping
// or duplicating ops.
func TestClient_PlanMigrationRegistered_DelegatesToPlanMigration(t *testing.T) {
	ctx := context.Background()
	c := newRegistryClient(t)
	_ = c.RegisterModel(&registryFixtureA{})

	planA, err := c.PlanMigrationRegistered(ctx)
	if err != nil {
		t.Fatalf("PlanMigrationRegistered: %v", err)
	}
	planB, err := c.PlanMigration(ctx, &registryFixtureA{})
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	// Compare by hash — equivalent plans hash equally (F3-3-types
	// + F3-4-resumable both rely on this).
	if planA.Hash() != planB.Hash() {
		t.Errorf("PlanMigrationRegistered should produce the same plan as PlanMigration with the registered models;\n  registered hash: %s\n  direct hash:     %s",
			planA.Hash(), planB.Hash())
	}
}
