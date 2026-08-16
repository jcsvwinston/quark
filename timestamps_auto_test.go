// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for DX-20 (DX audit 2026-08-16): a model with the
// conventional created_at/updated_at columns required 18 hand-written hook
// methods in the reference app just to stamp timestamps. The ORM now stamps
// them by column convention: Create fills both when zero (an explicit value
// wins), Update refreshes updated_at.
package quark_test

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

type stamped struct {
	ID        int64     `db:"id" pk:"true"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func newStampedClient(t *testing.T) *quark.Client {
	t.Helper()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Migrate(context.Background(), &stamped{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client
}

func TestCreateStampsTimestampsByConvention(t *testing.T) {
	client := newStampedClient(t)
	ctx := context.Background()

	e := &stamped{Name: "a"}
	if err := quark.For[stamped](ctx, client).Create(e); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := quark.For[stamped](ctx, client).Find(e.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("created_at/updated_at must be stamped on Create, got created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}
}

func TestCreateRespectsExplicitTimestamps(t *testing.T) {
	client := newStampedClient(t)
	ctx := context.Background()

	explicit := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	e := &stamped{Name: "b", CreatedAt: explicit, UpdatedAt: explicit}
	if err := quark.For[stamped](ctx, client).Create(e); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := quark.For[stamped](ctx, client).Find(e.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !got.CreatedAt.Equal(explicit) {
		t.Errorf("an explicit created_at must win over the convention: got %v", got.CreatedAt)
	}
}

func TestUpdateRefreshesUpdatedAt(t *testing.T) {
	client := newStampedClient(t)
	ctx := context.Background()

	e := &stamped{Name: "c"}
	if err := quark.For[stamped](ctx, client).Create(e); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := quark.For[stamped](ctx, client).Find(e.ID)
	if err != nil {
		t.Fatal(err)
	}

	origCreated, origUpdated := first.CreatedAt, first.UpdatedAt
	time.Sleep(1100 * time.Millisecond) // sqlite second precision on some paths
	first.Name = "c2"
	if _, err := quark.For[stamped](ctx, client).Update(&first); err != nil {
		t.Fatalf("update: %v", err)
	}
	second, err := quark.For[stamped](ctx, client).Find(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdatedAt.After(origUpdated) {
		t.Errorf("updated_at must refresh on Update: before=%v after=%v", origUpdated, second.UpdatedAt)
	}
	if !second.CreatedAt.Equal(origCreated) {
		t.Errorf("created_at must not move on Update: before=%v after=%v", origCreated, second.CreatedAt)
	}
}
