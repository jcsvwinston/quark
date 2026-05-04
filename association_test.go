package quark_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "github.com/mattn/go-sqlite3"
)

type AssocUser struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Posts []Post `rel:"has_many" join:"user_id"`
}

type Post struct {
	ID     int64  `db:"id" pk:"true"`
	Title  string `db:"title"`
	UserID int64  `db:"user_id"`
}

func TestAssociationSaving(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := client.Migrate(ctx, &AssocUser{}, &Post{}); err != nil {
		t.Fatal(err)
	}

	user := &AssocUser{
		Name: "Juan",
		Posts: []Post{
			{Title: "Post 1"},
			{Title: "Post 2"},
		},
	}

	err = quark.For[AssocUser](ctx, client).Create(user)
	if err != nil {
		t.Fatal(err)
	}

	if user.ID == 0 {
		t.Error("expected user ID to be set")
	}

	// Verify posts were saved
	count, err := quark.For[Post](ctx, client).Count()
	if err != nil {
		t.Fatal(err)
	}

	if count != 2 {
		t.Errorf("expected 2 posts to be saved, got %d", count)
	}

	// Verify BelongsTo
	type Profile struct {
		ID     int64      `db:"id" pk:"true"`
		Bio    string     `db:"bio"`
		UserID int64      `db:"user_id"`
		User   *AssocUser `rel:"belongs_to" join:"user_id"`
	}

	if err := client.Migrate(ctx, &Profile{}); err != nil {
		t.Fatal(err)
	}

	profile := &Profile{
		Bio:  "Bio 1",
		User: &AssocUser{Name: "Recursive User"},
	}

	err = quark.For[Profile](ctx, client).Create(profile)
	if err != nil {
		t.Fatal(err)
	}

	if profile.UserID == 0 {
		t.Error("expected profile UserID to be set from recursive user save")
	}

	if profile.User.ID == 0 {
		t.Error("expected profile.User.ID to be set")
	}
}
