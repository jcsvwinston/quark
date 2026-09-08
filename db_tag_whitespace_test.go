// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression for the divergence FuzzColumnNaming surfaced: a db tag whose
// column name carries surrounding whitespace was read two different ways.
// parseDBTag trimmed it and computeModelMeta cached the trimmed name, so the
// migration created the column `name` and by-PK WHERE clauses stayed
// well-formed; ColumnFromDBTag trimmed only after a comma, and it is the
// reader behind the call sites that build column lists from the raw struct
// tag. So the model migrated cleanly and then every write that spells its
// columns out failed — Create on the INSERT list, Update on the SET list —
// with `invalid identifier: identifier " name " contains invalid characters`,
// an error naming neither the field nor the tag.
package quark_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

type paddedTagUser struct {
	ID   int64  `db:" id " pk:"true"`
	Name string `db:" name "`
}

type paddedStringPKUser struct {
	Code string `db:" code " pk:"true"`
	Name string `db:"name"`
}

func TestDBTagWhitespaceIsTrimmedOnEveryPath(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &paddedTagUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Create builds the INSERT column list straight from the struct tags.
	// This is where the divergence used to surface, on the padded non-PK
	// column: `identifier " name " contains invalid characters`.
	u := &paddedTagUser{Name: "alice"}
	if err := quark.For[paddedTagUser](ctx, client).Create(u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("create did not populate the primary key")
	}

	got, err := quark.For[paddedTagUser](ctx, client).Find(u.ID)
	if err != nil {
		t.Fatalf("find by primary key: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("find returned %q, want %q", got.Name, "alice")
	}

	// Update spells its columns out in the SET clause and failed the same way.
	got.Name = "bob"
	if n, err := quark.For[paddedTagUser](ctx, client).Update(&got); err != nil || n != 1 {
		t.Fatalf("update by primary key: rows=%d err=%v", n, err)
	}
	if n, err := quark.For[paddedTagUser](ctx, client).Delete(&got); err != nil || n != 1 {
		t.Fatalf("delete by primary key: rows=%d err=%v", n, err)
	}
}

// A padded tag on the primary key reaches the same guard only when the PK
// enters the INSERT column list — as a string PK does, and as an auto-increment
// int64 PK does not while it is still zero. Pinned separately so the
// distinction the godoc draws is one the suite actually checks.
func TestDBTagWhitespaceIsTrimmedOnAStringPrimaryKey(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &paddedStringPKUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := quark.For[paddedStringPKUser](ctx, client).Create(&paddedStringPKUser{
		Code: "x1",
		Name: "alice",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := quark.For[paddedStringPKUser](ctx, client).Find("x1")
	if err != nil {
		t.Fatalf("find by primary key: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("find returned %q, want %q", got.Name, "alice")
	}
}

// The other half of the divergence: the fallback that adopts a column literally
// named "id" as the primary key compares against ColumnFromDBTag's answer. When
// that answer kept the padding it never matched, and a model that migrated a
// perfectly good `id` column was reported as having no primary key at all.
func TestPaddedIDTagStillResolvesTheImplicitPrimaryKey(t *testing.T) {
	type paddedImplicitPKUser struct {
		ID   int64  `db:" id "`
		Name string `db:"name"`
	}

	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &paddedImplicitPKUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	u := &paddedImplicitPKUser{Name: "alice"}
	if err := quark.For[paddedImplicitPKUser](ctx, client).Create(u); err != nil {
		if errors.Is(err, quark.ErrInvalidQuery) && strings.Contains(err.Error(), "no primary key") {
			t.Fatalf("padded db:\" id \" lost the implicit primary key: %v", err)
		}
		t.Fatalf("create: %v", err)
	}
	if _, err := quark.For[paddedImplicitPKUser](ctx, client).Find(u.ID); err != nil {
		t.Fatalf("find by implicit primary key: %v", err)
	}
}
