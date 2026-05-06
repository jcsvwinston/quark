package quark_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// User is an example model.
type User struct {
	ID        int64     `db:"id" json:"id"`
	Email     string    `db:"email" json:"email"`
	Name      string    `db:"name" json:"name"`
	Active    bool      `db:"active" json:"active"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func setupTestDB(t *testing.T) (*quark.Client, func()) {
	// Create quark client (sql.Open is handled internally)
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	// Create table
	ctx := context.Background()
	err = client.Exec(ctx, `
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL,
			name TEXT,
			active BOOLEAN DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}

	cleanup := func() {
		client.Close()
	}

	return client, cleanup
}

func TestCreate(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	user := User{Email: "alice@example.com", Name: "Alice", Active: true}
	err := quark.For[User](ctx, client).Create(&user)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if user.ID == 0 {
		t.Error("expected ID to be set after create")
	}

	fmt.Printf("✓ Created user with ID: %d\n", user.ID)
}

func TestFind(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create user
	user := User{Email: "bob@example.com", Name: "Bob", Active: true}
	err := quark.For[User](ctx, client).Create(&user)
	if err != nil {
		t.Fatal(err)
	}

	// Find by ID
	found, err := quark.For[User](ctx, client).Find(user.ID)
	if err != nil {
		t.Fatalf("find user: %v", err)
	}

	if found.Email != user.Email {
		t.Errorf("expected email %s, got %s", user.Email, found.Email)
	}

	fmt.Printf("✓ Found user by ID: %d (%s)\n", found.ID, found.Email)
}

func TestList(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create users
	users := []User{
		{Email: "alice@example.com", Name: "Alice", Active: true},
		{Email: "bob@example.com", Name: "Bob", Active: true},
		{Email: "charlie@example.com", Name: "Charlie", Active: false},
	}

	for i := range users {
		err := quark.For[User](ctx, client).Create(&users[i])
		if err != nil {
			t.Fatal(err)
		}
	}

	// List all
	all, err := quark.For[User](ctx, client).Limit(100).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 users, got %d", len(all))
	}

	// List active only
	active, err := quark.For[User](ctx, client).Where("active", "=", true).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active users, got %d", len(active))
	}

	fmt.Printf("✓ Listed %d total users, %d active\n", len(all), len(active))
}

func TestUpdate(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create user
	user := User{Email: "dave@example.com", Name: "Dave", Active: true}
	err := quark.For[User](ctx, client).Create(&user)
	if err != nil {
		t.Fatal(err)
	}

	// Update (only non-zero fields)
	user.Name = "David" // Only changing name
	rows, err := quark.For[User](ctx, client).Update(&user)
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row affected, got %d", rows)
	}

	// Verify update
	found, err := quark.For[User](ctx, client).Find(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Name != "David" {
		t.Errorf("expected name David, got %s", found.Name)
	}
	if found.Email != "dave@example.com" {
		t.Errorf("email should not change, got %s", found.Email)
	}

	fmt.Printf("✓ Updated user: name changed to %s\n", found.Name)
}

func TestUpdateMap(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create user
	user := User{Email: "eve@example.com", Name: "Eve", Active: true}
	err := quark.For[User](ctx, client).Create(&user)
	if err != nil {
		t.Fatal(err)
	}

	// Bulk update with map
	rows, err := quark.For[User](ctx, client).
		Where("id", "=", user.ID).
		UpdateMap(map[string]any{
			"name":   "Evelyn",
			"active": false,
		})
	if err != nil {
		t.Fatalf("update map: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row affected, got %d", rows)
	}

	// Verify update
	found, err := quark.For[User](ctx, client).Find(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Name != "Evelyn" {
		t.Errorf("expected name Evelyn, got %s", found.Name)
	}
	if found.Active != false {
		t.Errorf("expected active false, got %v", found.Active)
	}

	fmt.Printf("✓ Bulk updated user via map\n")
}

func TestDelete(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create user
	user := User{Email: "frank@example.com", Name: "Frank", Active: true}
	err := quark.For[User](ctx, client).Create(&user)
	if err != nil {
		t.Fatal(err)
	}

	// Hard delete (no deleted_at field = hard delete)
	rows, err := quark.For[User](ctx, client).HardDelete(&user)
	if err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row deleted, got %d", rows)
	}

	// Verify deletion
	_, err = quark.For[User](ctx, client).Find(user.ID)
	if err != quark.ErrNotFound {
		t.Errorf("expected quark.ErrNotFound after delete, got %v", err)
	}

	fmt.Printf("✓ Deleted user (hard delete)\n")
}

func TestDeleteBy(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create users
	for i := 0; i < 3; i++ {
		user := User{Email: fmt.Sprintf("user%d@test.com", i), Active: i < 2}
		err := quark.For[User](ctx, client).Create(&user)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Delete inactive users
	rows, err := quark.For[User](ctx, client).
		Where("active", "=", false).
		DeleteBy()
	if err != nil {
		t.Fatalf("delete by: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row deleted, got %d", rows)
	}

	// Verify
	remaining, err := quark.For[User](ctx, client).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Errorf("expected 2 remaining users, got %d", len(remaining))
	}

	fmt.Printf("✓ Deleted %d inactive users\n", rows)
}

func TestIter(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create 1000 users
	for i := 0; i < 1000; i++ {
		user := User{Email: fmt.Sprintf("user%d@test.com", i), Name: fmt.Sprintf("User %d", i)}
		if err := quark.For[User](ctx, client).Create(&user); err != nil {
			t.Fatal(err)
		}
	}

	// Use Iter to count without loading all into memory
	count := 0
	err := quark.For[User](ctx, client).Iter(func(user User) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("iter failed: %v", err)
	}
	if count != 1000 {
		t.Errorf("expected 1000 users, got %d", count)
	}

	fmt.Printf("✓ Iter() processed %d users without OOM\n", count)
}

func TestCursor(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create users
	for i := 0; i < 100; i++ {
		user := User{Email: fmt.Sprintf("cursor%d@test.com", i), Name: fmt.Sprintf("Cursor %d", i)}
		if err := quark.For[User](ctx, client).Create(&user); err != nil {
			t.Fatal(err)
		}
	}

	// Use Cursor for manual iteration
	cursor, err := quark.For[User](ctx, client).Where("email", "LIKE", "cursor%").Cursor()
	if err != nil {
		t.Fatal(err)
	}
	defer cursor.Close()

	count := 0
	for cursor.Next() {
		var user User
		if err := cursor.Scan(&user); err != nil {
			t.Fatal(err)
		}
		count++
	}

	if err := cursor.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 100 {
		t.Errorf("expected 100 users, got %d", count)
	}

	fmt.Printf("✓ Cursor() processed %d users\n", count)
}

func TestPaginate(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create 250 users
	for i := 0; i < 250; i++ {
		user := User{Email: fmt.Sprintf("page%d@test.com", i), Name: fmt.Sprintf("Page %d", i)}
		if err := quark.For[User](ctx, client).Create(&user); err != nil {
			t.Fatal(err)
		}
	}

	// Page 0, 100 per page
	page0, err := quark.For[User](ctx, client).OrderBy("id", "ASC").Paginate(100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page0.Items) != 100 {
		t.Errorf("expected 100 items, got %d", len(page0.Items))
	}
	if page0.Total != 250 {
		t.Errorf("expected total 250, got %d", page0.Total)
	}
	if page0.TotalPages != 3 {
		t.Errorf("expected 3 pages, got %d", page0.TotalPages)
	}

	// Page 2 (last page, only 50 items)
	page2, err := quark.For[User](ctx, client).OrderBy("id", "ASC").Paginate(100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 50 {
		t.Errorf("expected 50 items on last page, got %d", len(page2.Items))
	}

	fmt.Printf("✓ Paginate() works: %d items, %d pages\n", page0.Total, page0.TotalPages)
}

func TestCount(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create users with different active status
	for i := 0; i < 100; i++ {
		user := User{
			Email:  fmt.Sprintf("count%d@test.com", i),
			Name:   fmt.Sprintf("Count %d", i),
			Active: i%2 == 0, // 50 active, 50 inactive
		}
		if err := quark.For[User](ctx, client).Create(&user); err != nil {
			t.Fatal(err)
		}
	}

	// Count all
	total, err := quark.For[User](ctx, client).Count()
	if err != nil {
		t.Fatal(err)
	}
	if total != 100 {
		t.Errorf("expected 100 total, got %d", total)
	}

	// Count active only
	active, err := quark.For[User](ctx, client).Where("active", "=", true).Count()
	if err != nil {
		t.Fatal(err)
	}
	if active != 50 {
		t.Errorf("expected 50 active, got %d", active)
	}

	fmt.Printf("✓ Count() works: %d total, %d active\n", total, active)
}

func TestQueryBuilder(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create users with different created_at
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	users := []User{
		{Email: "old1@test.com", Name: "Old 1", CreatedAt: baseTime},
		{Email: "old2@test.com", Name: "Old 2", CreatedAt: baseTime.Add(24 * time.Hour)},
		{Email: "new@test.com", Name: "New", CreatedAt: baseTime.Add(7 * 24 * time.Hour)},
	}

	for i := range users {
		err := quark.For[User](ctx, client).Create(&users[i])
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test OrderBy and Limit
	results, err := quark.For[User](ctx, client).
		OrderBy("created_at", "DESC").
		Limit(2).
		List()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	// Should be newest first
	if results[0].Name != "New" {
		t.Errorf("expected first to be 'New', got %s", results[0].Name)
	}

	fmt.Printf("✓ Query builder with OrderBy and Limit works\n")
}

func TestTxNested(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	err := client.Tx(ctx, func(tx *quark.Tx) error {
		// Create a user in the outer tx
		u1 := User{Email: "outer@test.com", Name: "Outer", Active: true}
		if err := quark.ForTx[User](ctx, tx).Create(&u1); err != nil {
			return err
		}

		// Nested transaction that succeeds
		err := tx.Tx(ctx, func(nestedTx *quark.Tx) error {
			u2 := User{Email: "nested_success@test.com", Name: "Nested Success", Active: true}
			return quark.ForTx[User](ctx, nestedTx).Create(&u2)
		})
		if err != nil {
			return err
		}

		// Nested transaction that fails and rolls back
		_ = tx.Tx(ctx, func(nestedTx *quark.Tx) error {
			u3 := User{Email: "nested_fail@test.com", Name: "Nested Fail", Active: true}
			_ = quark.ForTx[User](ctx, nestedTx).Create(&u3)
			return fmt.Errorf("intentional failure")
		})

		return nil
	})

	if err != nil {
		t.Fatalf("tx failed: %v", err)
	}

	users, _ := quark.For[User](ctx, client).List()
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}

	foundEmails := make(map[string]bool)
	for _, u := range users {
		foundEmails[u.Email] = true
	}
	if !foundEmails["outer@test.com"] || !foundEmails["nested_success@test.com"] {
		t.Errorf("missing expected users: %v", foundEmails)
	}
	if foundEmails["nested_fail@test.com"] {
		t.Errorf("nested failed user should not exist")
	}

	fmt.Printf("✓ Nested transactions via savepoints work\n")
}

type ShopCustomer struct {
	ID     int64           `db:"id" pk:"true"`
	Name   string          `db:"name"`
	Orders []CustomerOrder `rel:"has_many" join:"customer_id"`
}

type CustomerOrder struct {
	ID         int64 `db:"id" pk:"true"`
	CustomerID int64 `db:"customer_id"`
	Total      int   `db:"total"`
}

func TestEagerLoading(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Create tables
	ctx := context.Background()
	err = client.Exec(ctx, `
		CREATE TABLE shop_customers (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT);
		CREATE TABLE customer_orders (id INTEGER PRIMARY KEY AUTOINCREMENT, customer_id INTEGER, total INTEGER);
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Create data
	c1 := ShopCustomer{Name: "Alice"}
	quark.For[ShopCustomer](ctx, client).Create(&c1)

	c2 := ShopCustomer{Name: "Bob"}
	quark.For[ShopCustomer](ctx, client).Create(&c2)

	quark.For[CustomerOrder](ctx, client).Create(&CustomerOrder{CustomerID: c1.ID, Total: 100})
	quark.For[CustomerOrder](ctx, client).Create(&CustomerOrder{CustomerID: c1.ID, Total: 200})
	quark.For[CustomerOrder](ctx, client).Create(&CustomerOrder{CustomerID: c2.ID, Total: 300})

	// Test Preload
	customers, err := quark.For[ShopCustomer](ctx, client).Preload("Orders").List()
	if err != nil {
		t.Fatalf("preload list failed: %v", err)
	}

	if len(customers) != 2 {
		t.Fatalf("expected 2 customers, got %d", len(customers))
	}

	for _, c := range customers {
		if c.Name == "Alice" {
			if len(c.Orders) != 2 {
				t.Errorf("expected Alice to have 2 orders, got %d", len(c.Orders))
			} else {
				if c.Orders[0].Total != 100 && c.Orders[1].Total != 100 {
					t.Errorf("missing order 100 for Alice")
				}
			}
		} else if c.Name == "Bob" {
			if len(c.Orders) != 1 {
				t.Errorf("expected Bob to have 1 order, got %d", len(c.Orders))
			} else {
				if c.Orders[0].Total != 300 {
					t.Errorf("missing order 300 for Bob")
				}
			}
		}
	}

	fmt.Printf("✓ Eager loading (has_many) works\n")
}

type HookUser struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Hooks string `db:"hooks"` // Store which hooks ran
}

func (h *HookUser) BeforeCreate(ctx context.Context) error {
	h.Hooks += "BeforeCreate,"
	return nil
}

func (h *HookUser) AfterCreate(ctx context.Context) error {
	h.Hooks += "AfterCreate,"
	return nil
}

func (h *HookUser) BeforeUpdate(ctx context.Context) error {
	h.Hooks += "BeforeUpdate,"
	return nil
}

func (h *HookUser) AfterUpdate(ctx context.Context) error {
	h.Hooks += "AfterUpdate,"
	return nil
}

func (h *HookUser) BeforeDelete(ctx context.Context) error {
	h.Hooks += "BeforeDelete,"
	return nil
}

func (h *HookUser) AfterDelete(ctx context.Context) error {
	h.Hooks += "AfterDelete,"
	return nil
}

func TestHooks(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()
	err = client.Exec(ctx, `CREATE TABLE hook_users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, hooks TEXT);`)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Test Create Hooks
	u := HookUser{Name: "Alice"}
	if err := quark.For[HookUser](ctx, client).Create(&u); err != nil {
		t.Fatal(err)
	}

	if u.Hooks != "BeforeCreate,AfterCreate," {
		t.Errorf("expected create hooks, got: %s", u.Hooks)
	}

	// 2. Test Update Hooks
	u.Hooks = "" // Reset
	u.Name = "Alice Updated"
	if _, err := quark.For[HookUser](ctx, client).Update(&u); err != nil {
		t.Fatal(err)
	}

	if u.Hooks != "BeforeUpdate,AfterUpdate," {
		t.Errorf("expected update hooks, got: %s", u.Hooks)
	}

	// 3. Test Delete Hooks
	u.Hooks = "" // Reset
	if _, err := quark.For[HookUser](ctx, client).HardDelete(&u); err != nil {
		t.Fatal(err)
	}

	if u.Hooks != "BeforeDelete,AfterDelete," {
		t.Errorf("expected delete hooks, got: %s", u.Hooks)
	}

	fmt.Printf("✓ Hooks (Before/After Create/Update/Delete) work\n")
}

type ValidatedUser struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name" validate:"required"`
	Email string `db:"email" validate:"required,email"`
}

func TestMigrationsAndValidation(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	ctx := context.Background()

	// 1. Test Migrations
	err = client.Migrate(ctx, ValidatedUser{})
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	// 2. Test Validation Failure (Missing Email & Name)
	badUser := ValidatedUser{}
	err = quark.For[ValidatedUser](ctx, client).Create(&badUser)
	if err == nil {
		t.Fatal("expected validation error for empty fields, got nil")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("expected validation error, got: %v", err)
	}

	// 3. Test Validation Failure (Invalid Email)
	badEmailUser := ValidatedUser{Name: "Bob", Email: "not-an-email"}
	err = quark.For[ValidatedUser](ctx, client).Create(&badEmailUser)
	if err == nil {
		t.Fatal("expected validation error for bad email, got nil")
	}

	// 4. Test Validation Success
	goodUser := ValidatedUser{Name: "Alice", Email: "alice@example.com"}
	err = quark.For[ValidatedUser](ctx, client).Create(&goodUser)
	if err != nil {
		t.Fatalf("expected successful creation, got: %v", err)
	}

	// Check if created successfully
	if goodUser.ID == 0 {
		t.Error("expected ID to be set after creation")
	}

	fmt.Printf("✓ Migrations and Validation work\n")
}

func TestTenantRouter(t *testing.T) {
	baseClient, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer baseClient.Close()
	baseClient.Migrate(context.Background(), &User{})

	// Test RowLevelSecurity Strategy
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurity
	cfg.BaseClient = baseClient

	resolver := func(ctx context.Context) string {
		if tid, ok := ctx.Value("tenant_id").(string); ok {
			return tid
		}
		return ""
	}

	router := quark.NewTenantRouter(cfg, resolver, nil)

	ctxA := context.WithValue(context.Background(), "tenant_id", "tenant_a")

	// Verify the router can create a query without error
	_ = quark.For[User](ctxA, router)

	// Test SchemaPerTenant Strategy
	cfg.Strategy = quark.SchemaPerTenant
	router = quark.NewTenantRouter(cfg, resolver, nil)
	_ = quark.For[User](ctxA, router)
}

func TestCall(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// Test Call directly via "SELECT abs(?)" which is generated by BuildProcedureCall for SQLite.
	// We pass a normal scalar to ensure execution works.
	err = quark.Call(context.Background(), client, "abs", -42)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestNotify(t *testing.T) {
	// Notify is only fully supported in Postgres via pg_notify,
	// we just test it returns an error in SQLite
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	err = quark.Notify(context.Background(), client, "my_channel", "hello")
	if err == nil {
		t.Error("expected error for SQLite notify, got nil")
	}
}
