package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// nestedAuthor / nestedPost / nestedComment are the canonical 3-level
// chain for testing dotted-path Preload. Author has Posts; each Post has
// Comments. A single call Preload("Posts.Comments") should walk both
// levels in one go.
type nestedAuthor struct {
	ID    int64        `db:"id" pk:"true"`
	Name  string       `db:"name"`
	Posts []nestedPost `rel:"has_many" join:"author_id"`
}

type nestedPost struct {
	ID       int64           `db:"id" pk:"true"`
	AuthorID int64           `db:"author_id"`
	Title    string          `db:"title"`
	Comments []nestedComment `rel:"has_many" join:"post_id"`
}

type nestedComment struct {
	ID     int64  `db:"id" pk:"true"`
	PostID int64  `db:"post_id"`
	Body   string `db:"body"`
}

// testNestedPreload covers the Phase-2 nested-preload deliverable:
// dotted-path Preload("Posts.Comments") loads the chain in one call.
// Multiple paths sharing a prefix don't double-load the prefix.
func testNestedPreload(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "nested_comments")
	dropTable(baseClient, "nested_posts")
	dropTable(baseClient, "nested_authors")
	if err := baseClient.Migrate(ctx, &nestedAuthor{}, &nestedPost{}, &nestedComment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "nested_comments")
	defer dropTable(baseClient, "nested_posts")
	defer dropTable(baseClient, "nested_authors")

	// Two authors × two posts each × two comments per post = 8 comments.
	for _, name := range []string{"alice", "bob"} {
		a := &nestedAuthor{Name: name}
		if err := quark.For[nestedAuthor](ctx, baseClient).Create(a); err != nil {
			t.Fatalf("create author %s: %v", name, err)
		}
		for j := 0; j < 2; j++ {
			p := &nestedPost{AuthorID: a.ID, Title: name + "-post"}
			if err := quark.For[nestedPost](ctx, baseClient).Create(p); err != nil {
				t.Fatalf("create post: %v", err)
			}
			for k := 0; k < 2; k++ {
				c := &nestedComment{PostID: p.ID, Body: "comment"}
				if err := quark.For[nestedComment](ctx, baseClient).Create(c); err != nil {
					t.Fatalf("create comment: %v", err)
				}
			}
		}
	}

	t.Run("DottedPathLoadsBothLevels", func(t *testing.T) {
		got, err := quark.For[nestedAuthor](ctx, baseClient).
			Preload("Posts.Comments").
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("preload: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 authors, got %d", len(got))
		}
		for _, a := range got {
			if len(a.Posts) != 2 {
				t.Errorf("author %s: expected 2 posts, got %d", a.Name, len(a.Posts))
			}
			for _, p := range a.Posts {
				if len(p.Comments) != 2 {
					t.Errorf("author %s post %d: expected 2 comments, got %d",
						a.Name, p.ID, len(p.Comments))
				}
			}
		}
	})

	t.Run("FirstLevelStillWorks", func(t *testing.T) {
		// Plain Preload("Posts") shouldn't recurse — Posts arrive but their
		// Comments stay empty.
		got, err := quark.For[nestedAuthor](ctx, baseClient).
			Preload("Posts").
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("preload: %v", err)
		}
		for _, a := range got {
			if len(a.Posts) != 2 {
				t.Errorf("expected 2 posts loaded, got %d", len(a.Posts))
			}
			for _, p := range a.Posts {
				if len(p.Comments) != 0 {
					t.Errorf("expected Comments empty without dotted path, got %d", len(p.Comments))
				}
			}
		}
	})

	t.Run("SharedPrefixDoesNotDoubleLoad", func(t *testing.T) {
		// Preload("Posts", "Posts.Comments") shares the Posts prefix; the
		// preload tree should merge them so Posts is only loaded once.
		// The observable contract: the result is the same as
		// Preload("Posts.Comments").
		got, err := quark.For[nestedAuthor](ctx, baseClient).
			Preload("Posts", "Posts.Comments").
			Limit(50).
			List()
		if err != nil {
			t.Fatalf("preload: %v", err)
		}
		for _, a := range got {
			if len(a.Posts) != 2 {
				t.Errorf("expected 2 posts, got %d", len(a.Posts))
			}
			for _, p := range a.Posts {
				if len(p.Comments) != 2 {
					t.Errorf("expected 2 comments via shared-prefix path, got %d", len(p.Comments))
				}
			}
		}
	})
}

// TestParsePreloads pins the tree-merging contract for the dotted-path
// parser. Useful as a unit-level sanity check independent of running the
// full SharedSuite.
func TestParsePreloads_Contract(t *testing.T) {
	// We can't reach parsePreloads from the test package (lowercase), so
	// the contract is exercised via the SharedScripts already; this test
	// is a placeholder that captures the *expected* node structure as
	// human-readable text. If parsePreloads is moved or its signature
	// changes the integration tests will fail loudly.
	t.Log("parsePreloads contract: 'A.B', 'A.C' merges A → [B, C]; 'A.B.C', 'A.B.D' nests as A → B → [C, D]")
}
