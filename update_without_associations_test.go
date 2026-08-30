// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

type waAuthor struct {
	ID    int64    `db:"id" pk:"true"`
	Name  string   `db:"name"`
	Posts []waPost `rel:"has_many" join:"author_id"`
}

type waPost struct {
	ID       int64  `db:"id" pk:"true"`
	AuthorID int64  `db:"author_id"`
	Title    string `db:"title"`
}

// TestUpdateWithoutAssociations covers AQ-03: Update recursively re-saved
// every preloaded association from the in-memory snapshot, silently
// clobbering concurrent changes to the children, with no opt-out. Now:
//   - the historical recursive save is unchanged by default, but logs a
//     WARN naming the associations it is about to write;
//   - WithoutAssociations() writes only the entity's own row.
func TestUpdateWithoutAssociations(t *testing.T) {
	ctx := context.Background()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	client, err := quark.New("sqlite", "file:withoutassoc?mode=memory&cache=shared",
		quark.WithLogger(logger))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &waAuthor{}, &waPost{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	seed := func(t *testing.T) (waAuthor, waPost) {
		t.Helper()
		a := waAuthor{Name: "ada"}
		if err := quark.For[waAuthor](ctx, client).Create(&a); err != nil {
			t.Fatalf("seed author: %v", err)
		}
		p := waPost{AuthorID: a.ID, Title: "v1"}
		if err := quark.For[waPost](ctx, client).Create(&p); err != nil {
			t.Fatalf("seed post: %v", err)
		}
		return a, p
	}

	t.Run("DefaultRecursiveSaveWarnsAndWrites", func(t *testing.T) {
		_, post := seed(t)

		got, err := quark.For[waAuthor](ctx, client).Preload("Posts").Find(post.AuthorID)
		if err != nil {
			t.Fatalf("find+preload: %v", err)
		}

		// Concurrent writer changes the child AFTER our read.
		if _, err := quark.For[waPost](ctx, client).UpdateFields(&waPost{ID: post.ID, Title: "concurrent"}, "title"); err != nil {
			t.Fatalf("concurrent child update: %v", err)
		}

		buf.Reset()
		got.Name = "ada lovelace"
		if _, err := quark.For[waAuthor](ctx, client).Update(&got); err != nil {
			t.Fatalf("update: %v", err)
		}

		// The historical trap, now at least visible: the WARN names the
		// association and the opt-out.
		out := buf.String()
		if !strings.Contains(out, "recursive_association_save") || !strings.Contains(out, "Posts") {
			t.Errorf("Update must WARN naming the associations it re-writes; log: %q", out)
		}
		if !strings.Contains(out, "WithoutAssociations") {
			t.Errorf("the WARN must point at the opt-out; log: %q", out)
		}

		// And the write really happened (behaviour unchanged): the
		// concurrent title was clobbered back to the snapshot.
		child, err := quark.For[waPost](ctx, client).Find(post.ID)
		if err != nil {
			t.Fatalf("re-read child: %v", err)
		}
		if child.Title != "v1" {
			t.Errorf("default Update must keep the recursive save (compat); child title = %q", child.Title)
		}
	})

	t.Run("WithoutAssociationsWritesOnlyTheRow", func(t *testing.T) {
		_, post := seed(t)

		got, err := quark.For[waAuthor](ctx, client).Preload("Posts").Find(post.AuthorID)
		if err != nil {
			t.Fatalf("find+preload: %v", err)
		}

		if _, err := quark.For[waPost](ctx, client).UpdateFields(&waPost{ID: post.ID, Title: "concurrent"}, "title"); err != nil {
			t.Fatalf("concurrent child update: %v", err)
		}

		buf.Reset()
		got.Name = "ada byron"
		rows, err := quark.For[waAuthor](ctx, client).WithoutAssociations().Update(&got)
		if err != nil {
			t.Fatalf("update without associations: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected rows=1, got %d", rows)
		}

		if strings.Contains(buf.String(), "recursive_association_save") {
			t.Errorf("no WARN expected under WithoutAssociations; log: %q", buf.String())
		}

		// The parent row was written…
		parent, err := quark.For[waAuthor](ctx, client).Find(post.AuthorID)
		if err != nil {
			t.Fatalf("re-read parent: %v", err)
		}
		if parent.Name != "ada byron" {
			t.Errorf("parent not updated: %+v", parent)
		}

		// …and the concurrent child change SURVIVED.
		child, err := quark.For[waPost](ctx, client).Find(post.ID)
		if err != nil {
			t.Fatalf("re-read child: %v", err)
		}
		if child.Title != "concurrent" {
			t.Errorf("WithoutAssociations must not touch children; child title = %q (clobbered)", child.Title)
		}
	})

	t.Run("CreateWithoutAssociationsSkipsChildren", func(t *testing.T) {
		a := waAuthor{Name: "solo", Posts: []waPost{{Title: "never written"}}}
		if err := quark.For[waAuthor](ctx, client).WithoutAssociations().Create(&a); err != nil {
			t.Fatalf("create: %v", err)
		}
		n, err := quark.For[waPost](ctx, client).Where("author_id", "=", a.ID).Count()
		if err != nil {
			t.Fatalf("count children: %v", err)
		}
		if n != 0 {
			t.Errorf("expected 0 children written, got %d", n)
		}
	})
}
