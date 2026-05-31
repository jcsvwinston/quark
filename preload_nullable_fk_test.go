// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// treeNode is a self-referential model with a NULLABLE foreign key
// (ParentID *int64). It is the regression fixture for the bug where a
// belongs_to / has_many whose join column maps to a pointer field never
// matched parents to children: the parent-key map was keyed by *int64 while
// the related row's PK scanned to int64, so the keys compared unequal and the
// relation loaded as nil/empty. See normalizeKey in preload_loaders.go.
type treeNode struct {
	ID       int64      `db:"id" pk:"true"`
	ParentID *int64     `db:"parent_id"` // nullable: roots have no parent
	Name     string     `db:"name"`
	Parent   *treeNode  `rel:"belongs_to" join:"parent_id"`
	Children []treeNode `rel:"has_many" join:"parent_id"`
}

// TestPreloadNullableFK covers both directions of a relation whose FK column
// is a *int64: belongs_to (the FK lives on the owner) and has_many (the FK
// lives on the child). Both must resolve despite the pointer indirection.
func TestPreloadNullableFK(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	if err := client.Migrate(ctx, &treeNode{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	root := &treeNode{Name: "root"}
	if err := quark.For[treeNode](ctx, client).Create(root); err != nil {
		t.Fatalf("create root: %v", err)
	}
	child := &treeNode{Name: "child", ParentID: &root.ID}
	if err := quark.For[treeNode](ctx, client).Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}
	grand := &treeNode{Name: "grand", ParentID: &child.ID}
	if err := quark.For[treeNode](ctx, client).Create(grand); err != nil {
		t.Fatalf("create grand: %v", err)
	}

	t.Run("BelongsToPointerFK", func(t *testing.T) {
		// child.Parent must load root via the nullable parent_id FK.
		got, err := quark.For[treeNode](ctx, client).Preload("Parent").Find(child.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Parent == nil {
			t.Fatal("Parent not loaded (nil) — nullable belongs_to FK regression")
		}
		if got.Parent.ID != root.ID {
			t.Errorf("Parent.ID = %d, want %d", got.Parent.ID, root.ID)
		}
	})

	t.Run("HasManyPointerFK", func(t *testing.T) {
		// root.Children must load via the children's nullable parent_id FK.
		got, err := quark.For[treeNode](ctx, client).Preload("Children").Find(root.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if len(got.Children) != 1 || got.Children[0].ID != child.ID {
			t.Fatalf("root.Children = %d rows, want exactly [child]; got %+v", len(got.Children), got.Children)
		}
	})

	t.Run("NullFKIsRootWithNoParent", func(t *testing.T) {
		// A NULL parent_id must not spuriously match any parent.
		got, err := quark.For[treeNode](ctx, client).Preload("Parent").Find(root.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Parent != nil {
			t.Errorf("root.Parent = %+v, want nil (NULL FK matches no parent)", got.Parent)
		}
	})

	t.Run("NestedDottedThroughPointerFK", func(t *testing.T) {
		// root → Children → Children walks two pointer-FK has_many hops.
		got, err := quark.For[treeNode](ctx, client).Preload("Children.Children").Find(root.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if len(got.Children) != 1 {
			t.Fatalf("L1 children = %d, want 1", len(got.Children))
		}
		if len(got.Children[0].Children) != 1 || got.Children[0].Children[0].ID != grand.ID {
			t.Errorf("L2 children = %+v, want [grand]", got.Children[0].Children)
		}
	})

	t.Run("NestedBelongsToChainThroughPointerFK", func(t *testing.T) {
		// grand → Parent → Parent walks two pointer-FK belongs_to hops
		// (the genealogy direction), the mirror of the has_many nest above.
		got, err := quark.For[treeNode](ctx, client).Preload("Parent.Parent").Find(grand.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Parent == nil || got.Parent.ID != child.ID {
			t.Fatalf("L1 Parent = %+v, want child", got.Parent)
		}
		if got.Parent.Parent == nil || got.Parent.Parent.ID != root.ID {
			t.Errorf("L2 Parent.Parent = %+v, want root", got.Parent.Parent)
		}
	})
}
