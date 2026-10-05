// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extdriver_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest/suite"

	_ "example.com/extdriver"
	"example.com/extdriver/dialect"
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

// TestDialectConformance runs the dialect kit against the fixture's own
// dialect, on the engine the driver registers, with foreign keys enforced so
// the kit's foreign-key checks are judged by the engine and not only by the
// catalog.
func TestDialectConformance(t *testing.T) {
	db, err := sql.Open(errs.Engine, "file:dialect_kit?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	drivertest.VerifyDialect(t, drivertest.DialectCase{
		Dialect:    dialect.New(errs.Engine),
		DB:         db,
		DriverName: errs.Engine,
	})
}

// TestEngineSuite runs the engine suite the in-repo engines run
// (internal/enginesuite calls the same suite.Run on all six), on the engine
// this driver registers, opened by name.
func TestEngineSuite(t *testing.T) {
	c, err := quark.New(errs.Engine, "file:engine_suite?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("quark.New(%q): %v", errs.Engine, err)
	}
	defer c.Close()
	suite.Run(t, c)
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

	// The dialect's sentinel, built from quarkdriver, is the application's
	// quark.ErrUnsupportedFeature: one value under two names.
	_, err = quark.For[note](ctx, c).ForUpdate().List()
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("a lock the engine cannot take is not ErrUnsupportedFeature: %v", err)
	}

	// The schema the dialect reads, in quarkdriver's model, is the
	// application's quark.Schema.
	var schema quark.Schema
	schema, err = c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if len(schema.Tables) != 1 || schema.Tables[0].Name != "notes" || len(schema.Tables[0].Columns) != 2 ||
		!schema.Tables[0].Columns[0].PrimaryKey || schema.Tables[0].Columns[1].Name != "title" {
		t.Fatalf("introspected schema: %+v", schema)
	}
}
