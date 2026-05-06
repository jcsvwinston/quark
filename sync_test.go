package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "github.com/mattn/go-sqlite3"
)

type UserV1 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (UserV1) TableName() string { return "users" }

type UserV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

func (UserV2) TableName() string { return "users" }

type UserV3 struct {
	ID       int64  `db:"id" pk:"true"`
	Name     string `db:"name"`
	Contacts string `db:"contacts" quark:"rename:email"`
}

func (UserV3) TableName() string { return "users" }

type UserV4 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (UserV4) TableName() string { return "users" }

func TestSync(t *testing.T) {
	client, err := quark.New("sqlite3", "file:synctest?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()

	// Create table with V1
	if err := client.Migrate(ctx, &UserV1{}); err != nil {
		t.Fatal(err)
	}

	// Sync with V2 (should add email column)
	if err := client.Sync(ctx, quark.SyncOptions{}, &UserV2{}); err != nil {
		t.Fatal(err)
	}

	// Verify column exists by inserting and retrieving
	u2 := &UserV2{Name: "Juan", Email: "juan@example.com"}
	if err := quark.For[UserV2](ctx, client).Create(u2); err != nil {
		t.Fatal(err)
	}

	found, err := quark.For[UserV2](ctx, client).Find(u2.ID)
	if err != nil {
		t.Fatal(err)
	}

	if found.Email != "juan@example.com" {
		t.Errorf("expected email to be juan@example.com, got %s", found.Email)
	}

	if found.Email != "juan@example.com" {
		t.Errorf("expected email to be juan@example.com, got %s", found.Email)
	}

	// Test Rename
	if err := client.Sync(ctx, quark.SyncOptions{}, &UserV3{}); err != nil {
		t.Fatal(err)
	}

	// Verify contacts has the old email data
	u3, err := quark.For[UserV3](ctx, client).Find(u2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u3.Contacts != "juan@example.com" {
		t.Errorf("expected contacts to have juan@example.com, got %s", u3.Contacts)
	}

	// First sync in safe mode (default) - should NOT drop contacts
	if err := client.Sync(ctx, quark.SyncOptions{}, &UserV4{}); err != nil {
		t.Fatal(err)
	}

	// Second sync with SafeMigrations = false - should drop contacts
	client, _ = quark.New("sqlite3", "file:synctest?mode=memory&cache=shared", quark.WithLimits(quark.Limits{SafeMigrations: false}))
	defer client.Close()
	if err := client.Sync(ctx, quark.SyncOptions{}, &UserV4{}); err != nil {
		t.Fatal(err)
	}
}
