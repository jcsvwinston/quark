// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression for the divergence FuzzParseDBTag surfaced: a db tag whose column
// name carries surrounding whitespace was read two different ways. The
// migration path (parseDBTag) trimmed it and created the column `id`; the
// primary-key path (ColumnFromDBTag, via FindPKs) did not, and looked the key
// up as ` id `. The model migrated cleanly and then every operation by primary
// key failed with an error naming neither the field nor the tag.
package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

type paddedTagUser struct {
	ID   int64  `db:" id " pk:"true"`
	Name string `db:" name "`
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

	u := &paddedTagUser{Name: "alice"}
	if err := quark.For[paddedTagUser](ctx, client).Create(u); err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("create did not populate the primary key")
	}

	// The read path resolves the primary key through FindPKs / columnFromDBTag.
	got, err := quark.For[paddedTagUser](ctx, client).Find(u.ID)
	if err != nil {
		t.Fatalf("find by primary key: %v", err)
	}
	if got.Name != "alice" {
		t.Fatalf("find returned %q, want %q", got.Name, "alice")
	}

	// And so do update and delete.
	got.Name = "bob"
	if n, err := quark.For[paddedTagUser](ctx, client).Update(&got); err != nil || n != 1 {
		t.Fatalf("update by primary key: rows=%d err=%v", n, err)
	}
	if n, err := quark.For[paddedTagUser](ctx, client).Delete(&got); err != nil || n != 1 {
		t.Fatalf("delete by primary key: rows=%d err=%v", n, err)
	}
}
