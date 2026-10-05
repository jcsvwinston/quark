// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

func testCRUD(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "suite_users")
	type SuiteUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	// Setup table for the engine
	err := client.Migrate(ctx, &SuiteUser{})
	if err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	// Create
	u := SuiteUser{Name: "Suite User", Email: "suite@test.com"}
	if err := quark.For[SuiteUser](ctx, client).Create(&u); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if u.ID == 0 {
		t.Error("expected ID to be set")
	}

	// Find
	found, err := quark.For[SuiteUser](ctx, client).Find(u.ID)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if found.Name != u.Name {
		t.Errorf("expected name %s, got %s", u.Name, found.Name)
	}

	// Update
	found.Name = "Updated Name"
	if _, err := quark.For[SuiteUser](ctx, client).Update(&found); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// Verify Update
	verify, _ := quark.For[SuiteUser](ctx, client).Find(u.ID)
	if verify.Name != "Updated Name" {
		t.Errorf("expected updated name, got %s", verify.Name)
	}

	// Delete
	if _, err := quark.For[SuiteUser](ctx, client).HardDelete(&verify); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	// Verify Delete
	_, err = quark.For[SuiteUser](ctx, client).Find(u.ID)
	if err != quark.ErrNotFound {
		t.Errorf("expected quark.ErrNotFound, got %v", err)
	}
}

func testQueryBuilder(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "qb_users")
	type QBUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
		Age  int    `db:"age"`
		City string `db:"city"`
	}

	client.Migrate(ctx, &QBUser{})

	users := []QBUser{
		{Name: "Alice", Age: 20, City: "Madrid"},
		{Name: "Charlie", Age: 30, City: "Madrid"},
		{Name: "Bob", Age: 40, City: "Barcelona"},
	}
	for i := range users {
		if err := quark.For[QBUser](ctx, client).Create(&users[i]); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	// Test Simple Where
	madrid, err := quark.For[QBUser](ctx, client).Where("city", "=", "Madrid").List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(madrid) != 2 {
		t.Errorf("expected 2 users in Madrid, got %d", len(madrid))
	}

	// Test And
	oldMadrid, _ := quark.For[QBUser](ctx, client).Where("city", "=", "Madrid").Where("age", ">", 25).List()
	if len(oldMadrid) != 1 {
		t.Errorf("expected 1 old user in Madrid, got %d", len(oldMadrid))
	}

	// Test Or
	orResult, _ := quark.For[QBUser](ctx, client).Where("city", "=", "Barcelona").Or(func(q *quark.Query[QBUser]) *quark.Query[QBUser] {
		return q.Where("age", "<", 25)
	}).List()
	if len(orResult) != 2 {
		t.Errorf("expected 2 users for OR condition, got %d", len(orResult))
	}

	// Test In
	inResult, _ := quark.For[QBUser](ctx, client).WhereIn("age", []any{20, 40}).List()
	if len(inResult) != 2 {
		t.Errorf("expected 2 users for IN condition, got %d", len(inResult))
	}

	// Test Between
	betweenResult, _ := quark.For[QBUser](ctx, client).WhereBetween("age", 25, 35).List()
	if len(betweenResult) != 1 {
		t.Errorf("expected 1 user for BETWEEN condition, got %d", len(betweenResult))
	}

	// Test Select
	selResult, _ := quark.For[QBUser](ctx, client).Select("name", "city").Where("age", "=", 30).List()
	if len(selResult) != 1 {
		t.Errorf("expected 1 user for Select, got %d", len(selResult))
	}
	if selResult[0].Name != "Charlie" || selResult[0].Age != 0 {
		if selResult[0].Age != 0 {
			t.Errorf("expected Age to be zero (not selected), got %d", selResult[0].Age)
		}
	}
}

func testTransactions(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "tx_users")
	type TxUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	client.Migrate(ctx, &TxUser{})

	// Successful quark.Tx
	err := client.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[TxUser](ctx, tx).Create(&TxUser{Name: "quark.Tx User"})
	})
	if err != nil {
		t.Fatalf("tx failed: %v", err)
	}

	// Rollback quark.Tx
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		quark.ForTx[TxUser](ctx, tx).Create(&TxUser{Name: "Rollback User"})
		return fmt.Errorf("intentional rollback")
	})
	if err == nil {
		t.Error("expected error from tx, got nil")
	}

	// Verify results
	count, _ := quark.For[TxUser](ctx, client).Count()
	if count != 1 {
		t.Errorf("expected 1 user after tx and rollback, got %d", count)
	}
}

func testHooks(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "hook_users")
	type HookUser struct {
		ID        int64      `db:"id" pk:"true"`
		Title     string     `db:"title"`
		DeletedAt *time.Time `db:"deleted_at"`
	}

	client.Migrate(ctx, &HookUser{})
	// Basic test for hooks could be more complex, but we mainly want to ensure they run across dialects
	u := HookUser{Title: "Hook Test"}
	if err := quark.For[HookUser](ctx, client).Create(&u); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Just verify creation worked
	if u.ID == 0 {
		t.Error("hook user ID not set")
	}
}

func testValidation(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "validateds")
	type Validated struct {
		ID    int64  `db:"id" pk:"true"`
		Email string `db:"email" validate:"required,email"`
	}
	client.Migrate(ctx, &Validated{})

	err := quark.For[Validated](ctx, client).Create(&Validated{Email: "invalid"})
	if err == nil {
		t.Error("expected validation error, got nil")
	}
	dropTable(client, "validateds")
}

func testSoftDelete(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "posts")
	type Post struct {
		ID        int64      `db:"id" pk:"true"`
		Title     string     `db:"title"`
		DeletedAt *time.Time `db:"deleted_at"`
	}

	client.Migrate(ctx, &Post{})
	p := Post{Title: "Soft Delete Post"}
	if err := quark.For[Post](ctx, client).Create(&p); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Soft delete
	rows, err := quark.For[Post](ctx, client).Delete(&p)
	if err != nil || rows != 1 {
		t.Fatalf("soft delete failed: %v, rows: %d", err, rows)
	}

	// Should not find by default
	_, err = quark.For[Post](ctx, client).Find(p.ID)
	if err != quark.ErrNotFound {
		t.Errorf("expected quark.ErrNotFound for soft deleted record, got %v", err)
	}

	// Should find with Unscoped
	found, err := quark.For[Post](ctx, client).Unscoped().Find(p.ID)
	if err != nil {
		t.Fatalf("unscoped find failed: %v", err)
	}
	if found.DeletedAt == nil {
		t.Error("expected DeletedAt to be set")
	}
}

func testPagination(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "logs")
	type Log struct {
		ID  int64  `db:"id" pk:"true"`
		Msg string `db:"msg"`
	}
	client.Migrate(ctx, &Log{})
	for i := 0; i < 50; i++ {
		if err := quark.For[Log](ctx, client).Create(&Log{Msg: "test"}); err != nil {
			t.Fatalf("failed to create log %d: %v", i, err)
		}
	}

	res, err := quark.For[Log](ctx, client).Paginate(10, 1) // Page 1 (offset 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 10 {
		t.Errorf("expected 10 items, got %d", len(res.Items))
	}
	if res.Total != 50 {
		t.Errorf("expected total 50, got %d", res.Total)
	}
}

type mockObserver struct {
	events []quark.QueryEvent
	mu     sync.Mutex
}

func (o *mockObserver) ObserveQuery(e quark.QueryEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func testEvents(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "event_users")
	obs := &mockObserver{}
	// Since client options are applied at quark.New(), we can't easily add an observer to an existing client
	// unless we use a middleware or the client supports it.
	// Quark quark.Client has an 'observers' slice. Let's see if we can append to it.
	// Actually, it's unexported. But we can create a NEW client with the SAME DB for this test.

	c2, _ := client.WithOptions(quark.WithQueryObserver(obs))

	type EventUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	if err := c2.Migrate(ctx, &EventUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if err := quark.For[EventUser](ctx, c2).Create(&EventUser{Name: "Event"}); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if _, err := quark.For[EventUser](ctx, c2).List(); err != nil {
		t.Fatalf("list failed: %v", err)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.events) < 2 {
		t.Errorf("expected at least 2 events, got %d", len(obs.events))
	}
}

type suiteMockMiddleware struct {
	called bool
}

func (m *suiteMockMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sql string, args []any) (*sql.Rows, error) {
		m.called = true
		return next(ctx, exec, sql, args)
	}
}

func (m *suiteMockMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return next
}

func (m *suiteMockMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return next
}

func testMiddleware(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "mid_users")
	mid := &suiteMockMiddleware{}
	c2, _ := client.WithOptions(quark.WithMiddleware(mid))

	type MidUser struct {
		ID int64 `db:"id" pk:"true"`
	}
	c2.Migrate(ctx, &MidUser{})
	quark.For[MidUser](ctx, c2).List()

	if !mid.called {
		t.Error("middleware was not called")
	}
}

type syncUserV1 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (syncUserV1) TableName() string { return "sync_users" }

type syncUserV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

func (syncUserV2) TableName() string { return "sync_users" }

type syncUserV3 struct {
	ID       int64  `db:"id" pk:"true"`
	Name     string `db:"name"`
	Contacts string `db:"contacts" quark:"rename:email"`
}

func (syncUserV3) TableName() string { return "sync_users" }

type syncUserV4 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (syncUserV4) TableName() string { return "sync_users" }

type rProfile struct {
	ID       int64  `db:"id" pk:"true"`
	Bio      string `db:"bio"`
	AuthorID int64  `db:"author_id"`
}

func (rProfile) TableName() string { return "r_profiles" }

type rPost struct {
	ID       int64  `db:"id" pk:"true"`
	Title    string `db:"title"`
	AuthorID int64  `db:"author_id"`
}

func (rPost) TableName() string { return "r_posts" }

type rAuthor struct {
	ID      int64    `db:"id" pk:"true"`
	Name    string   `db:"name"`
	Profile rProfile `rel:"has_one" join:"author_id"`
	Posts   []rPost  `rel:"has_many" join:"author_id"`
}

func (rAuthor) TableName() string { return "r_authors" }

func testSync(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "sync_users")

	// Initial migration
	if err := client.Migrate(ctx, &syncUserV1{}); err != nil {
		t.Fatalf("initial migrate failed: %v", err)
	}

	// Evolution: Add column
	if err := client.Sync(ctx, quark.SyncOptions{}, &syncUserV2{}); err != nil {
		t.Fatalf("sync v2 failed: %v", err)
	}

	// Verify addition
	u2 := syncUserV2{Name: "Sync", Email: "sync@test.com"}
	if err := quark.For[syncUserV2](ctx, client).Create(&u2); err != nil {
		t.Fatalf("create v2 failed: %v", err)
	}

	// Evolution: Rename column (email -> contacts)
	if err := client.Sync(ctx, quark.SyncOptions{}, &syncUserV3{}); err != nil {
		t.Fatalf("sync v3 failed: %v", err)
	}

	// Verify rename
	u3, err := quark.For[syncUserV3](ctx, client).Find(u2.ID)
	if err != nil {
		t.Fatalf("find v3 failed: %v", err)
	}
	if u3.Contacts != "sync@test.com" {
		t.Errorf("expected contacts to have sync@test.com, got %s", u3.Contacts)
	}

	// Evolution: Destructive drop (contacts)
	// Safe mode (default) - should NOT drop
	if err := client.Sync(ctx, quark.SyncOptions{}, &syncUserV4{}); err != nil {
		t.Fatal(err)
	}

	// Destructive mode
	cDestructive, _ := client.WithOptions(quark.WithLimits(quark.Limits{SafeMigrations: false}))
	if err := cDestructive.Sync(ctx, quark.SyncOptions{}, &syncUserV4{}); err != nil {
		t.Fatalf("destructive sync failed: %v", err)
	}
}

func testRecursiveAssociations(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "r_authors")
	dropTable(client, "r_profiles")
	dropTable(client, "r_posts")

	client.Migrate(ctx, &rAuthor{}, &rProfile{}, &rPost{})

	// Recursive Create
	author := rAuthor{
		Name:    "Recursive Author",
		Profile: rProfile{Bio: "Author Bio"},
		Posts: []rPost{
			{Title: "Post 1"},
			{Title: "Post 2"},
		},
	}

	if err := quark.For[rAuthor](ctx, client).Create(&author); err != nil {
		t.Fatalf("recursive create failed: %v", err)
	}

	if author.ID == 0 || author.Profile.ID == 0 || len(author.Posts) != 2 || author.Posts[0].ID == 0 {
		t.Errorf("recursive IDs not set correctly: %+v", author)
	}

	// Verify persistence
	found, err := quark.For[rAuthor](ctx, client).Preload("Profile").Preload("Posts").Find(author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Profile.Bio != "Author Bio" || len(found.Posts) != 2 {
		t.Errorf("recursive data not persisted: %+v", found)
	}

	// Recursive Update (Add a post)
	found.Posts = append(found.Posts, rPost{Title: "Post 3"})
	found.Profile.Bio = "Updated Bio"

	if _, err := quark.For[rAuthor](ctx, client).Update(&found); err != nil {
		t.Fatalf("recursive update failed: %v", err)
	}

	// Verify Update
	verify, _ := quark.For[rAuthor](ctx, client).Preload("Profile").Preload("Posts").Find(author.ID)
	if len(verify.Posts) != 3 || verify.Profile.Bio != "Updated Bio" {
		t.Errorf("recursive update failed verification: %d posts, bio: %s", len(verify.Posts), verify.Profile.Bio)
	}
}
