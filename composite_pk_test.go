package quark_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/internal/schema"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test models
// ---------------------------------------------------------------------------

// OrderItem is a join-table model with a 2-column composite primary key.
// Both order_id and product_id together uniquely identify a row.
type OrderItem struct {
	OrderID   int64  `db:"order_id"   pk:"true"`
	ProductID int64  `db:"product_id" pk:"true"`
	Qty       int    `db:"qty"`
	Note      string `db:"note"`
}

// RolePermission uses string composite keys.
type RolePermission struct {
	RoleID       string `db:"role_id"       pk:"true"`
	PermissionID string `db:"permission_id" pk:"true"`
	Granted      bool   `db:"granted"`
}

// ---------------------------------------------------------------------------
// Unit tests — schema layer (no DB required)
// ---------------------------------------------------------------------------

func TestCompositePK_SchemaDetection(t *testing.T) {
	meta := schema.GetModelMeta[OrderItem]()

	if !meta.HasCompositePK {
		t.Fatal("expected HasCompositePK = true for OrderItem")
	}
	if len(meta.CompositePK) != 2 {
		t.Fatalf("expected 2 composite PK columns, got %d", len(meta.CompositePK))
	}

	cols := []string{meta.CompositePK[0].Column, meta.CompositePK[1].Column}
	if cols[0] != "order_id" || cols[1] != "product_id" {
		t.Errorf("unexpected composite PK columns: %v", cols)
	}
}

func TestCompositePK_SinglePKNotComposite(t *testing.T) {
	type SinglePK struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	meta := schema.GetModelMeta[SinglePK]()

	if meta.HasCompositePK {
		t.Error("single-PK model should NOT have HasCompositePK = true")
	}
	if len(meta.CompositePK) != 1 {
		t.Errorf("expected CompositePK len 1 for single-PK model, got %d", len(meta.CompositePK))
	}
}

func TestCompositePK_FindPKs_ReturnsBoth(t *testing.T) {
	meta := schema.GetModelMeta[OrderItem]()
	if len(meta.CompositePK) != 2 {
		t.Fatalf("expected 2 PKs, got %d", len(meta.CompositePK))
	}

	if meta.CompositePK[0].Column != "order_id" {
		t.Errorf("first PK column should be order_id, got %s", meta.CompositePK[0].Column)
	}
	if meta.CompositePK[1].Column != "product_id" {
		t.Errorf("second PK column should be product_id, got %s", meta.CompositePK[1].Column)
	}
}

func TestCompositePK_StringKeys(t *testing.T) {
	meta := schema.GetModelMeta[RolePermission]()

	if !meta.HasCompositePK {
		t.Fatal("expected HasCompositePK = true for RolePermission")
	}
	if len(meta.CompositePK) != 2 {
		t.Fatalf("expected 2 PK columns, got %d", len(meta.CompositePK))
	}
}

// ---------------------------------------------------------------------------
// Integration tests — SQLite in-memory
// ---------------------------------------------------------------------------

func setupCompositePKDB(t *testing.T) (*quark.Client, func()) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`
		CREATE TABLE order_items (
			order_id   INTEGER NOT NULL,
			product_id INTEGER NOT NULL,
			qty        INTEGER NOT NULL DEFAULT 0,
			note       TEXT    NOT NULL DEFAULT '',
			PRIMARY KEY (order_id, product_id)
		)
	`)
	if err != nil {
		db.Close()
		t.Fatalf("create table: %v", err)
	}

	_, err = db.Exec(`
		CREATE TABLE role_permissions (
			role_id       TEXT NOT NULL,
			permission_id TEXT NOT NULL,
			granted       INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (role_id, permission_id)
		)
	`)
	if err != nil {
		db.Close()
		t.Fatalf("create table: %v", err)
	}

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}

	return client, func() { client.Close() }
}

func TestCompositePK_Create(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	item := OrderItem{OrderID: 1, ProductID: 10, Qty: 5, Note: "first"}
	if err := quark.For[OrderItem](ctx, client).Create(&item); err != nil {
		t.Fatalf("create failed: %v", err)
	}
}

func TestCompositePK_DuplicateReturnsError(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	item := OrderItem{OrderID: 1, ProductID: 10, Qty: 5}
	quark.For[OrderItem](ctx, client).Create(&item)

	// Same composite key → must fail
	dup := OrderItem{OrderID: 1, ProductID: 10, Qty: 99}
	err := quark.For[OrderItem](ctx, client).Create(&dup)
	if err == nil {
		t.Fatal("expected error on duplicate composite PK insert, got nil")
	}
}

func TestCompositePK_Update(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	item := OrderItem{OrderID: 2, ProductID: 20, Qty: 3, Note: "original"}
	if err := quark.For[OrderItem](ctx, client).Create(&item); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	item.Qty = 99
	item.Note = "updated"
	rows, err := quark.For[OrderItem](ctx, client).Update(&item)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row affected, got %d", rows)
	}

	// Verify via raw query
	var qty int
	if err := client.Raw().QueryRow(
		`SELECT qty FROM order_items WHERE order_id = 2 AND product_id = 20`,
	).Scan(&qty); err != nil {
		t.Fatalf("verify scan failed: %v", err)
	}
	if qty != 99 {
		t.Errorf("expected qty=99 after update, got %d", qty)
	}
}

func TestCompositePK_HardDelete(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	item := OrderItem{OrderID: 3, ProductID: 30, Qty: 1}
	quark.For[OrderItem](ctx, client).Create(&item)

	rows, err := quark.For[OrderItem](ctx, client).HardDelete(&item)
	if err != nil {
		t.Fatalf("hard delete failed: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 row deleted, got %d", rows)
	}

	// Verify deletion
	var count int
	client.Raw().QueryRow(
		`SELECT COUNT(*) FROM order_items WHERE order_id = 3 AND product_id = 30`,
	).Scan(&count)
	if count != 0 {
		t.Errorf("expected row to be deleted, count=%d", count)
	}
}

func TestCompositePK_DeleteBy(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	items := []OrderItem{
		{OrderID: 4, ProductID: 40, Qty: 1},
		{OrderID: 4, ProductID: 41, Qty: 2},
		{OrderID: 5, ProductID: 50, Qty: 3},
	}
	for i := range items {
		quark.For[OrderItem](ctx, client).Create(&items[i])
	}

	rows, err := quark.For[OrderItem](ctx, client).
		Where("order_id", "=", 4).
		DeleteBy()
	if err != nil {
		t.Fatalf("DeleteBy failed: %v", err)
	}
	if rows != 2 {
		t.Errorf("expected 2 rows deleted, got %d", rows)
	}
}

func TestCompositePK_List(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	for _, item := range []OrderItem{
		{OrderID: 6, ProductID: 60, Qty: 1},
		{OrderID: 6, ProductID: 61, Qty: 2},
		{OrderID: 7, ProductID: 70, Qty: 3},
	} {
		quark.For[OrderItem](ctx, client).Create(&item)
	}

	results, err := quark.For[OrderItem](ctx, client).Where("order_id", "=", 6).List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 items for order 6, got %d", len(results))
	}
}

func TestCompositePK_StringKeys_CreateAndDelete(t *testing.T) {
	client, cleanup := setupCompositePKDB(t)
	defer cleanup()

	ctx := context.Background()

	rp := RolePermission{RoleID: "admin", PermissionID: "write", Granted: true}
	if err := quark.For[RolePermission](ctx, client).Create(&rp); err != nil {
		t.Fatalf("create RolePermission failed: %v", err)
	}

	rows, err := quark.For[RolePermission](ctx, client).HardDelete(&rp)
	if err != nil {
		t.Fatalf("hard delete RolePermission failed: %v", err)
	}
	if rows != 1 {
		t.Errorf("expected 1 deleted row, got %d", rows)
	}
}

// ---------------------------------------------------------------------------
// SharedSuite integration — composite PK test added to the shared suite
// ---------------------------------------------------------------------------

// testCompositePK is called from SharedSuite to run composite PK scenarios
// against any supported engine.
func testCompositePK(ctx context.Context, t *testing.T, client *quark.Client) {
	type CPKItem struct {
		TenantID int64  `db:"tenant_id" pk:"true"`
		ItemID   int64  `db:"item_id"   pk:"true"`
		Label    string `db:"label"`
	}

	dropTable(client, "cpk_items")
	if err := client.Migrate(ctx, &CPKItem{}); err != nil {
		t.Fatalf("migrate cpk_items: %v", err)
	}

	// Create
	a := CPKItem{TenantID: 1, ItemID: 100, Label: "alpha"}
	if err := quark.For[CPKItem](ctx, client).Create(&a); err != nil {
		t.Fatalf("create a: %v", err)
	}
	b := CPKItem{TenantID: 1, ItemID: 200, Label: "beta"}
	if err := quark.For[CPKItem](ctx, client).Create(&b); err != nil {
		t.Fatalf("create b: %v", err)
	}

	// Update — change label via composite PK WHERE
	a.Label = "alpha-updated"
	rows, err := quark.For[CPKItem](ctx, client).Update(&a)
	if err != nil {
		t.Fatalf("update a: %v", err)
	}
	if rows != 1 {
		t.Errorf("update: expected 1 row affected, got %d", rows)
	}

	// List
	all, err := quark.For[CPKItem](ctx, client).Where("tenant_id", "=", 1).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 items, got %d", len(all))
	}

	// HardDelete
	if _, err := quark.For[CPKItem](ctx, client).HardDelete(&b); err != nil {
		t.Fatalf("hard delete b: %v", err)
	}
	remaining, _ := quark.For[CPKItem](ctx, client).Where("tenant_id", "=", 1).List()
	if len(remaining) != 1 {
		t.Errorf("expected 1 item after delete, got %d", len(remaining))
	}
}
