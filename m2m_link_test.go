package quark_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testM2MLinkErrors is the regression test for P0-3. Before the fix linkM2M
// returned nil for ANY driver error, masking corruption. After the fix only
// unique-key violations surface as nil (idempotent re-link); every other
// error propagates wrapped.
func testM2MLinkErrors(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type LinkAuthor struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}

	type LinkBook struct {
		ID      int64        `db:"id" pk:"true"`
		Title   string       `db:"title"`
		Authors []LinkAuthor `rel:"many_to_many" m2m:"link_book_author:book_id:author_id"`
	}

	dropTable(baseClient, "link_book_author")
	dropTable(baseClient, "link_books")
	dropTable(baseClient, "link_authors")
	if err := baseClient.Migrate(ctx, &LinkAuthor{}, &LinkBook{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	defer dropTable(baseClient, "link_book_author")
	defer dropTable(baseClient, "link_books")
	defer dropTable(baseClient, "link_authors")

	// Seed an author.
	a := &LinkAuthor{Name: "alice"}
	if err := quark.For[LinkAuthor](ctx, baseClient).Create(a); err != nil {
		t.Fatalf("create author: %v", err)
	}
	if a.ID == 0 {
		t.Fatal("expected author.ID assigned")
	}

	// Create a book that links to the author. Populates link_book_author with
	// one row (book_id=b.ID, author_id=a.ID).
	b := &LinkBook{Title: "Quark", Authors: []LinkAuthor{*a}}
	if err := quark.For[LinkBook](ctx, baseClient).Create(b); err != nil {
		t.Fatalf("create book: %v", err)
	}

	t.Run("IdempotentRelink", func(t *testing.T) {
		// Re-saving with the same author must NOT error: the unique-key
		// violation in the join table is interpreted as "already linked".
		// Pre-fix this passed for the wrong reason (every error was
		// swallowed). Post-fix it passes for the right reason
		// (isUniqueViolation matched).
		b.Title = "Quark (2nd ed.)"
		if _, err := quark.For[LinkBook](ctx, baseClient).Update(b); err != nil {
			t.Fatalf("idempotent re-link should succeed, got: %v", err)
		}

		// Verify only one link row exists — no duplicate accumulated.
		var n int
		row := baseClient.Raw().QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = %s",
				baseClient.Dialect().Quote("link_book_author"),
				baseClient.Dialect().Quote("book_id"),
				baseClient.Dialect().Placeholder(1)),
			b.ID,
		)
		if err := row.Scan(&n); err != nil {
			t.Fatalf("count link rows: %v", err)
		}
		if n != 1 {
			t.Errorf("expected exactly 1 link row, got %d", n)
		}
	})

	t.Run("MissingJoinTablePropagates", func(t *testing.T) {
		// Drop the join table; the next Update triggers linkM2M whose INSERT
		// fails with "no such table" (or the engine's equivalent). Pre-fix:
		// linkM2M swallowed and Update returned nil. Post-fix: the error
		// propagates and Update returns it.
		dropTable(baseClient, "link_book_author")

		b.Title = "Quark (3rd ed.)"
		_, err := quark.For[LinkBook](ctx, baseClient).Update(b)
		if err == nil {
			t.Fatal("Update should have failed after the join table was dropped, got nil — linkM2M is swallowing errors again")
		}

		// Recreate the join table for the next subtest / cleanup. Use Migrate
		// which handles per-dialect DDL.
		if err := baseClient.Migrate(ctx, &LinkBook{}); err != nil {
			t.Fatalf("re-migrate after drop: %v", err)
		}
	})
}
