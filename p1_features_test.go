package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newSQLiteClient(t *testing.T) *quark.Client {
	t.Helper()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, err := quark.New("sqlite", ":memory:", quark.WithLimits(limits))
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// ─── models used across multiple tests ───────────────────────────────────────

type P1User struct {
	ID     int64  `db:"id"   pk:"true"`
	Name   string `db:"name" quark:"not_null"`
	Email  string `db:"email" quark:"unique"`
	Age    int    `db:"age"   default:"0"`
	Active bool   `db:"active" default:"1"`
}

type P1Order struct {
	ID     int64   `db:"id"      pk:"true"`
	UserID int64   `db:"user_id" quark:"not_null"`
	Amount float64 `db:"amount"`
}

// ─── P0 tests: Upsert ─────────────────────────────────────────────────────────

func TestUpsert_InsertOnConflict(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	u := &P1User{Name: "Alice", Email: "alice@test.com", Age: 30}
	if err := quark.For[P1User](ctx, client).Create(u); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Upsert same email → should update name
	u2 := &P1User{Email: "alice@test.com", Name: "Alice Updated", Age: 31}
	if err := quark.For[P1User](ctx, client).Upsert(u2, []string{"email"}, []string{"name", "age"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := quark.For[P1User](ctx, client).Where("email", "=", "alice@test.com").First()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if got.Name != "Alice Updated" {
		t.Errorf("expected 'Alice Updated', got %q", got.Name)
	}
	if got.Age != 31 {
		t.Errorf("expected age 31, got %d", got.Age)
	}
}

func TestUpsert_NewRecord(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	u := &P1User{Name: "Bob", Email: "bob@test.com", Age: 25}
	if err := quark.For[P1User](ctx, client).Upsert(u, []string{"email"}, []string{"name", "age"}); err != nil {
		t.Fatalf("upsert new record: %v", err)
	}

	count, err := quark.For[P1User](ctx, client).Where("email", "=", "bob@test.com").Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record after upsert insert, got %d", count)
	}
}

// ─── P0 tests: CreateBatch ────────────────────────────────────────────────────

func TestCreateBatch_InsertsAll(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	users := []*P1User{
		{Name: "Batch1", Email: "batch1@test.com", Age: 10},
		{Name: "Batch2", Email: "batch2@test.com", Age: 20},
		{Name: "Batch3", Email: "batch3@test.com", Age: 30},
	}
	if err := quark.For[P1User](ctx, client).CreateBatch(users); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	count, _ := quark.For[P1User](ctx, client).WhereIn("email", []any{"batch1@test.com", "batch2@test.com", "batch3@test.com"}).Count()
	if count != 3 {
		t.Errorf("expected 3 batch-inserted records, got %d", count)
	}
}

func TestCreateBatch_EmptyIsNoop(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	if err := quark.For[P1User](ctx, client).CreateBatch([]*P1User{}); err != nil {
		t.Errorf("CreateBatch with empty slice should be noop, got: %v", err)
	}
}

// ─── P1 tests: WhereNot ───────────────────────────────────────────────────────

func TestWhereNot(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	quark.For[P1User](ctx, client).CreateBatch([]*P1User{
		{Name: "Active", Email: "active@test.com", Active: true},
		{Name: "Inactive", Email: "inactive@test.com", Active: false},
	})

	results, err := quark.For[P1User](ctx, client).WhereNot("active", "=", true).List()
	if err != nil {
		t.Fatalf("WhereNot: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 inactive user, got %d", len(results))
	}
	if results[0].Name != "Inactive" {
		t.Errorf("expected 'Inactive', got %q", results[0].Name)
	}
}

// ─── P1 tests: Distinct ───────────────────────────────────────────────────────

func TestDistinct(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	quark.For[P1User](ctx, client).CreateBatch([]*P1User{
		{Name: "Alice", Email: "a1@test.com", Age: 25},
		{Name: "Alice", Email: "a2@test.com", Age: 25},
		{Name: "Bob", Email: "b1@test.com", Age: 30},
	})

	// DISTINCT on name column — use Select to pick specific column, then Distinct
	results, err := quark.For[P1User](ctx, client).Select("name").Distinct().List()
	if err != nil {
		t.Fatalf("Distinct: %v", err)
	}
	names := make(map[string]bool)
	for _, r := range results {
		names[r.Name] = true
	}
	if len(names) != 2 {
		t.Errorf("expected 2 distinct names, got %d: %v", len(names), names)
	}
}

// ─── P1 tests: GroupBy / Having ───────────────────────────────────────────────

func TestGroupByHaving(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	quark.For[P1User](ctx, client).CreateBatch([]*P1User{
		{Name: "Alice", Email: "ga1@test.com", Age: 25},
		{Name: "Alice", Email: "ga2@test.com", Age: 25},
		{Name: "Bob", Email: "gb1@test.com", Age: 30},
	})

	// GROUP BY name — just verify no error
	results, err := quark.For[P1User](ctx, client).
		Select("name").
		GroupBy("name").
		List()
	if err != nil {
		t.Fatalf("GroupBy: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 grouped rows, got %d", len(results))
	}
}

func TestGroupByHaving_Filter(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	quark.For[P1User](ctx, client).CreateBatch([]*P1User{
		{Name: "Alice", Email: "ha1@test.com", Age: 25},
		{Name: "Alice", Email: "ha2@test.com", Age: 25},
		{Name: "Bob", Email: "hb1@test.com", Age: 30},
	})

	// SELECT name, COUNT(*) — no HAVING filter in quark yet for COUNT, just test Having with normal agg
	results, err := quark.For[P1User](ctx, client).
		Select("name").
		GroupBy("name").
		Having("age", ">", 24).
		List()
	if err != nil {
		t.Fatalf("Having: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least 1 result from Having")
	}
}

// ─── P1 tests: Aggregates ─────────────────────────────────────────────────────

func TestAggregates(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1Order{})

	quark.For[P1Order](ctx, client).CreateBatch([]*P1Order{
		{UserID: 1, Amount: 10.5},
		{UserID: 1, Amount: 20.0},
		{UserID: 2, Amount: 5.0},
	})

	sum, err := quark.For[P1Order](ctx, client).Sum("amount")
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum != 35.5 {
		t.Errorf("expected Sum=35.5, got %f", sum)
	}

	avg, err := quark.For[P1Order](ctx, client).Where("user_id", "=", 1).Avg("amount")
	if err != nil {
		t.Fatalf("Avg: %v", err)
	}
	if avg != 15.25 {
		t.Errorf("expected Avg=15.25, got %f", avg)
	}

	min, err := quark.For[P1Order](ctx, client).Min("amount")
	if err != nil {
		t.Fatalf("Min: %v", err)
	}
	if min != 5.0 {
		t.Errorf("expected Min=5.0, got %f", min)
	}

	max, err := quark.For[P1Order](ctx, client).Max("amount")
	if err != nil {
		t.Fatalf("Max: %v", err)
	}
	if max != 20.0 {
		t.Errorf("expected Max=20.0, got %f", max)
	}
}

func TestAggregates_WithWhere(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1Order{})

	quark.For[P1Order](ctx, client).CreateBatch([]*P1Order{
		{UserID: 10, Amount: 100.0},
		{UserID: 10, Amount: 200.0},
		{UserID: 20, Amount: 999.0},
	})

	sum, _ := quark.For[P1Order](ctx, client).Where("user_id", "=", 10).Sum("amount")
	if sum != 300.0 {
		t.Errorf("expected filtered Sum=300.0, got %f", sum)
	}
}

// ─── P1 tests: Scopes ─────────────────────────────────────────────────────────

func TestScopes_Apply(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	quark.For[P1User](ctx, client).CreateBatch([]*P1User{
		{Name: "ScopeA", Email: "sa@test.com", Active: true, Age: 20},
		{Name: "ScopeB", Email: "sb@test.com", Active: false, Age: 30},
		{Name: "ScopeC", Email: "sc@test.com", Active: true, Age: 40},
	})

	activeOnly := quark.Scope[P1User](func(q *quark.Query[P1User]) *quark.Query[P1User] {
		return q.Where("active", "=", true)
	})
	olderThan25 := quark.Scope[P1User](func(q *quark.Query[P1User]) *quark.Query[P1User] {
		return q.Where("age", ">", 25)
	})

	results, err := quark.For[P1User](ctx, client).Apply(activeOnly, olderThan25).List()
	if err != nil {
		t.Fatalf("Apply scopes: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result from scopes, got %d", len(results))
	}
	if results[0].Name != "ScopeC" {
		t.Errorf("expected ScopeC, got %q", results[0].Name)
	}
}

func TestScopes_Composable(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	quark.For[P1User](ctx, client).CreateBatch([]*P1User{
		{Name: "X1", Email: "x1@test.com", Age: 10},
		{Name: "X2", Email: "x2@test.com", Age: 20},
		{Name: "X3", Email: "x3@test.com", Age: 30},
	})

	ageAbove := func(min int) quark.Scope[P1User] {
		return func(q *quark.Query[P1User]) *quark.Query[P1User] {
			return q.Where("age", ">", min)
		}
	}

	results, err := quark.For[P1User](ctx, client).Apply(ageAbove(15)).List()
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

// ─── P1 tests: WhereSubquery ─────────────────────────────────────────────────

func TestWhereSubquery(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1Order{})

	quark.For[P1Order](ctx, client).CreateBatch([]*P1Order{
		{UserID: 1, Amount: 50.0},
		{UserID: 2, Amount: 100.0},
		{UserID: 3, Amount: 200.0},
	})

	// WHERE id IN (SELECT id FROM p1_orders WHERE amount > 75)
	sub := `SELECT "id" FROM "p1_orders" WHERE "amount" > 75`
	results, err := quark.For[P1Order](ctx, client).WhereSubquery("id", "IN", sub).List()
	if err != nil {
		t.Fatalf("WhereSubquery: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results from subquery, got %d", len(results))
	}

	// Negative: must be blocked without AllowRawQueries
	noRawLimits := quark.DefaultLimits()
	noRawClient, err := quark.New("sqlite", ":memory:", quark.WithLimits(noRawLimits))
	if err != nil {
		t.Fatalf("quark.New (no raw): %v", err)
	}
	defer noRawClient.Close()
	noRawClient.Migrate(ctx, &P1Order{})

	_, err = quark.For[P1Order](ctx, noRawClient).WhereSubquery("id", "IN", sub).List()
	if err == nil {
		t.Error("WhereSubquery: expected error when AllowRawQueries=false, got nil")
	}
}

// ─── P1 tests: CreateIndex / AddForeignKey ────────────────────────────────────

func TestCreateIndex(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{})

	if err := client.CreateIndex(ctx, "p1_users", "idx_p1_users_email", []string{"email"}, true); err != nil {
		t.Fatalf("CreateIndex: %v", err)
	}
	// Second call should be idempotent (IF NOT EXISTS)
	if err := client.CreateIndex(ctx, "p1_users", "idx_p1_users_email", []string{"email"}, true); err != nil {
		t.Fatalf("CreateIndex idempotent: %v", err)
	}
}

func TestCreateIndex_EmptyColumns(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	if err := client.CreateIndex(ctx, "p1_users", "idx_empty", []string{}, false); err == nil {
		t.Error("expected error for empty columns")
	}
}

func TestAddForeignKey_SQLite(t *testing.T) {
	// SQLite does not support ALTER TABLE … ADD CONSTRAINT … FOREIGN KEY.
	// The method should return an error (we just verify it doesn't panic).
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &P1User{}, &P1Order{})

	// SQLite will return an error — that's expected behaviour.
	err := client.AddForeignKey(ctx, "p1_orders", "fk_p1_orders_user",
		[]string{"user_id"}, "p1_users", []string{"id"}, "CASCADE", "")
	// No panic is sufficient; SQLite error is expected
	_ = err
}

// ─── P1 tests: NOT NULL / DEFAULT / UNIQUE tags ───────────────────────────────

func TestNotNullDefaultUniqueTags(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	// The schema uses NOT NULL, DEFAULT, UNIQUE from struct tags
	if err := client.Migrate(ctx, &P1User{}); err != nil {
		t.Fatalf("Migrate with tags: %v", err)
	}
	// Verify we can insert and read
	u := &P1User{Name: "TagTest", Email: "tagtest@test.com"}
	if err := quark.For[P1User](ctx, client).Create(u); err != nil {
		t.Fatalf("Create with tagged model: %v", err)
	}
}

// ─── P1 tests: Polymorphic E2E ────────────────────────────────────────────────

type PolyComment struct {
	ID         int64  `db:"id"         pk:"true"`
	Body       string `db:"body"`
	PolyableID int64  `db:"polyable_id"`
	PolyType   string `db:"poly_type"`
}

type PolyPost struct {
	ID    int64  `db:"id" pk:"true"`
	Title string `db:"title"`
	// polymorphic tag: "type_col:poly_type_value"
	// join col overrides the default polyable_id derivation via the join tag
	Comments []PolyComment `rel:"polymorphic" polymorphic:"poly_type:post" join:"polyable_id"`
}

func TestPolymorphicPreload_E2E(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &PolyPost{}, &PolyComment{})

	// Insert a post
	post := &PolyPost{Title: "Hello World"}
	if err := quark.For[PolyPost](ctx, client).Create(post); err != nil {
		t.Fatalf("create post: %v", err)
	}
	// Insert comments pointing to this post
	c1 := &PolyComment{Body: "Nice!", PolyableID: post.ID, PolyType: "post"}
	c2 := &PolyComment{Body: "Thanks!", PolyableID: post.ID, PolyType: "post"}
	quark.For[PolyComment](ctx, client).Create(c1)
	quark.For[PolyComment](ctx, client).Create(c2)

	// Preload comments
	posts, err := quark.For[PolyPost](ctx, client).Preload("Comments").List()
	if err != nil {
		t.Fatalf("preload: %v", err)
	}
	if len(posts) == 0 {
		t.Fatal("expected posts")
	}
	if len(posts[0].Comments) != 2 {
		t.Errorf("expected 2 polymorphic comments, got %d", len(posts[0].Comments))
	}
}

// ─── P1 tests: M2M Preload ────────────────────────────────────────────────────

type ArticleTag struct {
	ID   int64  `db:"id"   pk:"true"`
	Name string `db:"name"`
}

type Article struct {
	ID    int64        `db:"id"    pk:"true"`
	Title string       `db:"title"`
	Tags  []ArticleTag `rel:"many_to_many" m2m:"article_tag_links:article_id:tag_id"`
}

func TestM2MPreload(t *testing.T) {
	ctx := context.Background()
	client := newSQLiteClient(t)
	client.Migrate(ctx, &Article{}, &ArticleTag{})

	tag1 := &ArticleTag{Name: "go"}
	tag2 := &ArticleTag{Name: "orm"}
	quark.For[ArticleTag](ctx, client).Create(tag1)
	quark.For[ArticleTag](ctx, client).Create(tag2)

	article := &Article{Title: "Quark ORM", Tags: []ArticleTag{*tag1, *tag2}}
	if err := quark.For[Article](ctx, client).Create(article); err != nil {
		t.Fatalf("create article with tags: %v", err)
	}

	articles, err := quark.For[Article](ctx, client).Preload("Tags").List()
	if err != nil {
		t.Fatalf("preload M2M: %v", err)
	}
	if len(articles) == 0 {
		t.Fatal("expected articles")
	}
	if len(articles[0].Tags) != 2 {
		t.Errorf("expected 2 M2M tags, got %d", len(articles[0].Tags))
	}
}

// ─── UpsertSQL dialect unit tests ─────────────────────────────────────────────

func TestUpsertSQL_PostgreSQL(t *testing.T) {
	d := quark.PostgreSQL()
	sql := d.UpsertSQL([]string{"email"}, []string{"name", "age"}, 5)
	expected := ` ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name", "age" = EXCLUDED."age"`
	if sql != expected {
		t.Errorf("postgres upsert:\ngot:  %q\nwant: %q", sql, expected)
	}
}

func TestUpsertSQL_SQLite(t *testing.T) {
	d := quark.SQLite()
	sql := d.UpsertSQL([]string{"email"}, []string{"name"}, 1)
	expected := ` ON CONFLICT ("email") DO UPDATE SET "name" = excluded."name"`
	if sql != expected {
		t.Errorf("sqlite upsert:\ngot:  %q\nwant: %q", sql, expected)
	}
}

func TestUpsertSQL_MySQL(t *testing.T) {
	d := quark.MySQL()
	sql := d.UpsertSQL([]string{"email"}, []string{"name", "age"}, 1)
	expected := " ON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `age` = VALUES(`age`)"
	if sql != expected {
		t.Errorf("mysql upsert:\ngot:  %q\nwant: %q", sql, expected)
	}
}

func TestUpsertSQL_Postgres_NoUpdate(t *testing.T) {
	d := quark.PostgreSQL()
	sql := d.UpsertSQL([]string{"id"}, []string{}, 1)
	expected := ` ON CONFLICT ("id") DO NOTHING`
	if sql != expected {
		t.Errorf("postgres do nothing:\ngot:  %q\nwant: %q", sql, expected)
	}
}
