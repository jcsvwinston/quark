// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extdriver_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"

	_ "example.com/extdriver"
	"example.com/extdriver/errs"
)

type note struct {
	ID    int64  `db:"id" pk:"true"`
	Title string `db:"title"`
}

func (note) TableName() string { return "notes" }

// open opens the engine BY NAME, with no WithDialect: the dialect has to be
// found in the registry the driver wrote to.
func open(t *testing.T, name string) *quark.Client {
	t.Helper()
	c, err := quark.New(errs.Engine, "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("quark.New(%q): %v", errs.Engine, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if got := c.Dialect().Name(); got != errs.Engine {
		t.Fatalf("the client resolved dialect %q, not the driver's %q", got, errs.Engine)
	}
	// The table is created by hand, in the engine's own DDL. Migrate is not
	// used on purpose: what it writes for an engine name Quark does not know
	// is measured separately (internal/extbench, DRV-04), and this fixture
	// measures the driver contract.
	if _, err := c.Raw().ExecContext(context.Background(),
		`CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT UNIQUE)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return c
}

// uniqueErr is a real duplicate-key error from the driver.
func uniqueErr(t *testing.T) error {
	t.Helper()
	c := open(t, "unique_err")
	ctx := context.Background()
	if err := quark.For[note](ctx, c).Create(&note{Title: "a"}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := quark.For[note](ctx, c).Create(&note{Title: "a"})
	if err == nil {
		t.Fatal("a duplicate title was accepted")
	}
	return err
}

func TestConformance(t *testing.T) {
	drivertest.Verify(t, drivertest.Case{
		Engine:     errs.Engine,
		Classifier: errs.Classifier,
		Unique:     uniqueErr(t),
	})
}

func TestEndToEnd(t *testing.T) {
	ctx := context.Background()
	c := open(t, "end_to_end")
	n := note{Title: "first"}
	if err := quark.For[note](ctx, c).Create(&n); err != nil || n.ID == 0 {
		t.Fatalf("create: id=%d err=%v", n.ID, err)
	}
	got, err := quark.For[note](ctx, c).Find(n.ID)
	if err != nil || got.Title != "first" {
		t.Fatalf("find: %+v %v", got, err)
	}
	err = quark.For[note](ctx, c).Create(&note{Title: "first"})
	if !quark.IsUniqueViolation(err) {
		t.Fatalf("a duplicate key from the driver is not classified as a unique violation: %v", err)
	}
}
