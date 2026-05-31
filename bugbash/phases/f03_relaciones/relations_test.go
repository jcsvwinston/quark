// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f03_relaciones is bug-bash phase F3: relations. Per engine it
// seeds a small deterministic graph (org → customer → orders → lines →
// product → category → parent; user → profile/roles; order → audit events)
// and exercises every relation kind Quark supports — belongs_to, has_one,
// has_many, many_to_many, and the owner-side polymorphic has-many — through
// Preload, including deep dotted paths and a self-referential tree. A
// query-counting middleware proves preload batches (no N+1). Failures
// aggregate via reporter.Fail.
//
// Scope notes (what F3 does NOT cover, and why):
//   - Constrained preload (a WHERE on the relation): Quark's Preload takes
//     only relation names — there is no callback/where variant — so there is
//     nothing to exercise. Documented as a gap in this phase's README.
//   - has-many cascade Create (Create(&order) with order.Lines populated):
//     Quark auto-saves belongs_to parents on Create, not has-many children
//     (query_crud.go). F3 verifies the belongs_to cascade that DOES exist and
//     documents the has-many side as out of scope.
//   - Tenant propagation through loads and the Oracle-1000 IN-chunk boundary:
//     squarely F5 (multi-tenancy) and F4 (volume) territory; F3 keeps the
//     graph small and single-tenant.
package f03_relaciones

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/domain"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f03_relaciones"

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// rec binds reporter.Fail to a fixed engine + category for one group.
type rec struct {
	t   *testing.T
	eng string
	cat reporter.Category
}

func newRec(t *testing.T, eng string, cat reporter.Category) rec {
	return rec{t: t, eng: eng, cat: cat}
}

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase:    phase,
		Test:     name,
		Engine:   r.eng,
		Category: r.cat,
		Severity: sev,
		Error:    fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestRelations ./phases/f03_relaciones/... -engines=" + r.eng,
		},
	})
}

// queryCounter is a Quark middleware that tallies read round-trips (Query +
// QueryRow). It is the dep-free instrument F3 uses to prove preload batches
// instead of issuing one child query per parent (the N+1 criterion).
type queryCounter struct {
	quark.BaseMiddleware
	n atomic.Int64
}

func (c *queryCounter) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		c.n.Add(1)
		return next(ctx, exec, sqlStr, args)
	}
}

func (c *queryCounter) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) *sql.Row {
		c.n.Add(1)
		return next(ctx, exec, sqlStr, args)
	}
}

func (c *queryCounter) reset()       { c.n.Store(0) }
func (c *queryCounter) count() int64 { return c.n.Load() }

// fixture holds the IDs the relation groups query against.
type fixture struct {
	orgID      int64
	customerID int64
	userID     int64
	rootCatID  int64
	childCatID int64
	productID  int64
	order0ID   int64 // has 2 lines, 1 payment+refund, 2 Order audit events
	order1ID   int64 // has 1 line
	extraCust  []int64
}

func TestRelations(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var containerEngines []string
		for _, e := range engines {
			if e != tools.SQLite {
				containerEngines = append(containerEngines, e)
			}
		}
		tools.Down(containerEngines...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			counter := &queryCounter{}
			client, err := quark.New(conn.Driver, conn.DSN, quark.WithMiddleware(counter))
			if err != nil {
				t.Fatalf("quark.New(%q): %v", conn.Driver, err)
			}
			t.Cleanup(func() {
				_ = client.Close()
				if eng == tools.SQLite {
					_ = os.Remove(conn.DSN)
				}
			})
			if err := client.Migrate(ctx, domain.AllModels()...); err != nil {
				t.Fatalf("migrate domain on %s: %v", eng, err)
			}

			fx := seed(t, ctx, client, eng)

			t.Run("BelongsTo", func(t *testing.T) { belongsTo(t, ctx, client, eng, fx) })
			t.Run("HasMany", func(t *testing.T) { hasMany(t, ctx, client, eng, fx) })
			t.Run("HasOne", func(t *testing.T) { hasOne(t, ctx, client, eng, fx) })
			t.Run("ManyToMany", func(t *testing.T) { manyToMany(t, ctx, client, eng, fx) })
			t.Run("Polymorphic", func(t *testing.T) { polymorphic(t, ctx, client, eng, fx) })
			t.Run("NestedDotted", func(t *testing.T) { nestedDotted(t, ctx, client, eng, fx) })
			t.Run("SelfRefTree", func(t *testing.T) { selfRefTree(t, ctx, client, eng, fx) })
			t.Run("NoNPlusOne", func(t *testing.T) { noNPlusOne(t, ctx, client, eng, fx, counter) })
			// Mutating group last (cascade create adds rows).
			t.Run("CascadeCreateBelongsTo", func(t *testing.T) { cascadeCreate(t, ctx, client, eng, fx) })
		})
	}
}

// seed builds the relation graph. Setup errors are blocking — the relation
// groups cannot run without it.
func seed(t *testing.T, ctx context.Context, c *quark.Client, eng string) fixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	dec := decimal.RequireFromString

	org := &domain.Organization{
		Name: "F3 " + eng, Slug: "f3-" + eng, Plan: "pro",
		Settings:  quark.JSON[domain.OrgSettings]{V: domain.OrgSettings{DefaultLocale: "es-ES", SLAHours: 24}},
		CreatedAt: now, UpdatedAt: now,
	}
	mustCreate(t, ctx, c, eng, org)

	// Category tree: root → childA → grandchild (3 levels for self-ref tree
	// and for the deep dotted path's Category.Parent leaf).
	root := &domain.Category{OrganizationID: org.ID, Name: "Root", Slug: "root", Depth: 0, CreatedAt: now}
	mustCreate(t, ctx, c, eng, root)
	childA := &domain.Category{OrganizationID: org.ID, ParentID: &root.ID, Name: "A", Slug: "cat-a", Depth: 1, CreatedAt: now}
	mustCreate(t, ctx, c, eng, childA)
	grand := &domain.Category{OrganizationID: org.ID, ParentID: &childA.ID, Name: "A1", Slug: "cat-a1", Depth: 2, CreatedAt: now}
	mustCreate(t, ctx, c, eng, grand)

	product := &domain.Product{
		OrganizationID: org.ID, CategoryID: childA.ID,
		SKU: "SKU-" + eng, Name: "Widget", Price: dec("10.00"), Currency: "EUR",
		Weight: dec("1.0"), Active: true, CreatedAt: now,
	}
	mustCreate(t, ctx, c, eng, product)

	// Inventory (has_many on Product) in one warehouse.
	wh := &domain.Warehouse{OrganizationID: org.ID, Code: "WH-" + eng, Location: "Madrid", CreatedAt: now}
	mustCreate(t, ctx, c, eng, wh)
	inv := &domain.Inventory{ProductID: product.ID, WarehouseID: wh.ID, Quantity: 100, UpdatedAt: now}
	mustCreate(t, ctx, c, eng, inv)

	cust := &domain.Customer{OrganizationID: org.ID, Name: "Acme", CreditLimit: dec("1000.00"), CreatedAt: now}
	mustCreate(t, ctx, c, eng, cust)

	fx := fixture{
		orgID: org.ID, customerID: cust.ID,
		rootCatID: root.ID, childCatID: childA.ID, productID: product.ID,
	}

	// Two orders for the customer; line counts differ so the nested path is
	// not symmetric (catches index/grouping bugs).
	order0 := &domain.Order{
		OrganizationID: org.ID, CustomerID: cust.ID, Number: "ORD-" + eng + "-0", Status: "paid",
		Subtotal: dec("20.00"), Tax: dec("0.00"), Total: dec("20.00"), Currency: "EUR", PlacedAt: now, CreatedAt: now,
	}
	mustCreate(t, ctx, c, eng, order0)
	order1 := &domain.Order{
		OrganizationID: org.ID, CustomerID: cust.ID, Number: "ORD-" + eng + "-1", Status: "pending",
		Subtotal: dec("10.00"), Tax: dec("0.00"), Total: dec("10.00"), Currency: "EUR", PlacedAt: now, CreatedAt: now,
	}
	mustCreate(t, ctx, c, eng, order1)
	fx.order0ID, fx.order1ID = order0.ID, order1.ID

	for i, oid := range []int64{order0.ID, order0.ID, order1.ID} { // 2 lines on order0, 1 on order1
		line := &domain.OrderLine{
			OrderID: oid, ProductID: product.ID, Quantity: 1,
			UnitPrice: dec("10.00"), Discount: dec("0.00"), LineTotal: dec("10.00"), Sequence: i + 1,
		}
		mustCreate(t, ctx, c, eng, line)
	}

	// Payment + Refund on order0 (Order.Payments has_many, Payment.Refunds has_many).
	pay := &domain.Payment{
		OrderID: order0.ID, Method: "card", Amount: dec("20.00"), Currency: "EUR",
		Reference: "PAY-" + eng, Provider: "stripe", Status: "captured", ProcessedAt: now,
		Metadata: quark.JSON[map[string]any]{V: map[string]any{"ok": true}},
	}
	mustCreate(t, ctx, c, eng, pay)
	ref := &domain.Refund{PaymentID: pay.ID, Amount: dec("5.00"), Reason: "partial", ProcessedAt: now}
	mustCreate(t, ctx, c, eng, ref)

	// Polymorphic audit events: two for the Order subject, one for a User
	// subject carrying the SAME numeric id — the Order preload must exclude
	// it (type literal filter, not just the join id).
	for _, ae := range []*domain.AuditEvent{
		{OrgID: org.ID, SubjectType: "Order", SubjectID: order0.ID, Action: "created", OccurredAt: now,
			Diff: quark.JSON[map[string]any]{V: map[string]any{"status": "paid"}}},
		{OrgID: org.ID, SubjectType: "Order", SubjectID: order0.ID, Action: "updated", OccurredAt: now,
			Diff: quark.JSON[map[string]any]{V: map[string]any{}}},
		{OrgID: org.ID, SubjectType: "User", SubjectID: order0.ID, Action: "created", OccurredAt: now,
			Diff: quark.JSON[map[string]any]{V: map[string]any{}}},
	} {
		mustCreate(t, ctx, c, eng, ae)
	}

	// User with belongs_to(org), has_one(profile), m2m(roles).
	user := &domain.User{OrganizationID: org.ID, Email: "u@" + eng + ".test", Locale: "es", CreatedAt: now}
	mustCreate(t, ctx, c, eng, user)
	fx.userID = user.ID
	profile := &domain.UserProfile{
		UserID: user.ID, Bio: "hi",
		// Avatar is left NULL (zero Nullable[[]byte]) on purpose: it doubles as
		// the cross-engine regression for BB-6 — inserting a NULL Nullable[[]byte]
		// used to fail on MSSQL (bound as nvarchar against varbinary(max)).
		Prefs: quark.JSON[domain.ProfilePrefs]{V: domain.ProfilePrefs{Theme: "dark"}},
		Tags:  quark.Array[string]{V: []string{"a", "b"}}, CreatedAt: now,
	}
	mustCreate(t, ctx, c, eng, profile)
	for i := 0; i < 2; i++ {
		role := &domain.Role{
			OrganizationID: org.ID, Name: fmt.Sprintf("role-%s-%d", eng, i),
			Permissions: quark.Array[string]{V: []string{"read"}}, CreatedAt: now,
		}
		mustCreate(t, ctx, c, eng, role)
		// Link explicitly (the join table carries GrantedAt); m2m auto-link
		// on Create is not relied upon here.
		mustCreate(t, ctx, c, eng, &domain.UserRole{UserID: user.ID, RoleID: role.ID, GrantedAt: now})
	}

	// Extra customers (each with 2 orders) so the N+1 group can distinguish
	// a batched IN-load (2 queries) from a per-parent load (1 + N).
	for i := 0; i < 3; i++ {
		ec := &domain.Customer{OrganizationID: org.ID, Name: fmt.Sprintf("Extra-%d", i), CreditLimit: dec("0.00"), CreatedAt: now}
		mustCreate(t, ctx, c, eng, ec)
		fx.extraCust = append(fx.extraCust, ec.ID)
		for j := 0; j < 2; j++ {
			o := &domain.Order{
				OrganizationID: org.ID, CustomerID: ec.ID, Number: fmt.Sprintf("ORD-%s-e%d-%d", eng, i, j),
				Status: "paid", Subtotal: dec("1.00"), Tax: dec("0.00"), Total: dec("1.00"), Currency: "EUR",
				PlacedAt: now, CreatedAt: now,
			}
			mustCreate(t, ctx, c, eng, o)
		}
	}
	return fx
}

func mustCreate[E any](t *testing.T, ctx context.Context, c *quark.Client, eng string, e *E) {
	t.Helper()
	if err := quark.For[E](ctx, c).Create(e); err != nil {
		t.Fatalf("seed %T on %s: %v", e, eng, err)
	}
}

// belongsTo: Order→Customer, User→Organization, Category→Parent, Product→Category.
func belongsTo(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)

	ord, err := quark.For[domain.Order](ctx, c).Preload("Customer").Find(fx.order0ID)
	if err != nil {
		r.fail("Order.Customer/Find", reporter.SeverityP1, "find: %v", err)
	} else if ord.Customer == nil || ord.Customer.ID != fx.customerID {
		r.fail("Order.Customer", reporter.SeverityP1, "Customer not loaded (got %+v)", ord.Customer)
	}

	usr, err := quark.For[domain.User](ctx, c).Preload("Organization").Find(fx.userID)
	if err != nil {
		r.fail("User.Organization/Find", reporter.SeverityP1, "find: %v", err)
	} else if usr.Organization == nil || usr.Organization.ID != fx.orgID {
		r.fail("User.Organization", reporter.SeverityP1, "Organization not loaded (got %+v)", usr.Organization)
	}

	child, err := quark.For[domain.Category](ctx, c).Preload("Parent").Find(fx.childCatID)
	if err != nil {
		r.fail("Category.Parent/Find", reporter.SeverityP1, "find: %v", err)
	} else if child.Parent == nil || child.Parent.ID != fx.rootCatID {
		r.fail("Category.Parent", reporter.SeverityP1, "Parent not loaded (got %+v)", child.Parent)
	}

	prod, err := quark.For[domain.Product](ctx, c).Preload("Category").Find(fx.productID)
	if err != nil {
		r.fail("Product.Category/Find", reporter.SeverityP1, "find: %v", err)
	} else if prod.Category == nil || prod.Category.ID != fx.childCatID {
		r.fail("Product.Category", reporter.SeverityP1, "Category not loaded (got %+v)", prod.Category)
	}
}

// hasMany: Customer.Orders, Order.Lines/Payments, Product.Inventory, Payment.Refunds.
func hasMany(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)

	cust, err := quark.For[domain.Customer](ctx, c).Preload("Orders").Find(fx.customerID)
	if err != nil {
		r.fail("Customer.Orders/Find", reporter.SeverityP1, "find: %v", err)
	} else if len(cust.Orders) != 2 {
		r.fail("Customer.Orders", reporter.SeverityP1, "loaded %d orders, want 2", len(cust.Orders))
	}

	ord, err := quark.For[domain.Order](ctx, c).Preload("Lines").Preload("Payments").Find(fx.order0ID)
	if err != nil {
		r.fail("Order.Lines/Find", reporter.SeverityP1, "find: %v", err)
	} else {
		if len(ord.Lines) != 2 {
			r.fail("Order.Lines", reporter.SeverityP1, "loaded %d lines, want 2", len(ord.Lines))
		}
		if len(ord.Payments) != 1 {
			r.fail("Order.Payments", reporter.SeverityP1, "loaded %d payments, want 1", len(ord.Payments))
		}
	}

	prod, err := quark.For[domain.Product](ctx, c).Preload("Inventory").Find(fx.productID)
	if err != nil {
		r.fail("Product.Inventory/Find", reporter.SeverityP1, "find: %v", err)
	} else if len(prod.Inventory) != 1 {
		r.fail("Product.Inventory", reporter.SeverityP1, "loaded %d inventory, want 1", len(prod.Inventory))
	}

	pays, err := quark.For[domain.Payment](ctx, c).Where("order_id", "=", fx.order0ID).Preload("Refunds").List()
	if err != nil {
		r.fail("Payment.Refunds/List", reporter.SeverityP1, "list: %v", err)
	} else if len(pays) != 1 || len(pays[0].Refunds) != 1 {
		r.fail("Payment.Refunds", reporter.SeverityP1, "got %d payments; refunds on first = %d, want 1/1",
			len(pays), refundCount(pays))
	}
}

func refundCount(pays []domain.Payment) int {
	if len(pays) == 0 {
		return -1
	}
	return len(pays[0].Refunds)
}

// hasOne: User.Profile (1:1).
func hasOne(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	usr, err := quark.For[domain.User](ctx, c).Preload("Profile").Find(fx.userID)
	if err != nil {
		r.fail("User.Profile/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if usr.Profile == nil {
		r.fail("User.Profile", reporter.SeverityP1, "Profile not loaded (nil)")
		return
	}
	if usr.Profile.UserID != fx.userID {
		r.fail("User.Profile", reporter.SeverityP1, "Profile.UserID = %d, want %d", usr.Profile.UserID, fx.userID)
	}
}

// manyToMany: User.Roles via the user_roles join table.
func manyToMany(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	usr, err := quark.For[domain.User](ctx, c).Preload("Roles").Find(fx.userID)
	if err != nil {
		r.fail("User.Roles/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if len(usr.Roles) != 2 {
		r.fail("User.Roles", reporter.SeverityP1, "loaded %d roles, want 2", len(usr.Roles))
	}
}

// polymorphic: Order.AuditEvents — must load only subject_type="Order" rows,
// excluding the User-typed row that shares the same numeric subject_id.
func polymorphic(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryGap)
	ord, err := quark.For[domain.Order](ctx, c).Preload("AuditEvents").Find(fx.order0ID)
	if err != nil {
		r.fail("Order.AuditEvents/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if len(ord.AuditEvents) != 2 {
		r.fail("Order.AuditEvents", reporter.SeverityP1,
			"loaded %d events, want 2 (the User-typed event with the same id must be excluded)", len(ord.AuditEvents))
		return
	}
	for _, ae := range ord.AuditEvents {
		if ae.SubjectType != "Order" {
			r.fail("Order.AuditEvents/typefilter", reporter.SeverityP1,
				"loaded an event with subject_type=%q, want only \"Order\"", ae.SubjectType)
		}
	}
}

// nestedDotted: the deep path Orders.Lines.Product.Category.Parent (5 levels,
// mixing has_many and belongs_to) off a single Customer.
func nestedDotted(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	cust, err := quark.For[domain.Customer](ctx, c).
		Preload("Orders.Lines.Product.Category.Parent").
		Find(fx.customerID)
	if err != nil {
		r.fail("Nested/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if len(cust.Orders) != 2 {
		r.fail("Nested/Orders", reporter.SeverityP1, "got %d orders, want 2", len(cust.Orders))
		return
	}
	totalLines := 0
	for _, o := range cust.Orders {
		for _, ln := range o.Lines {
			totalLines++
			if ln.Product == nil {
				r.fail("Nested/Line.Product", reporter.SeverityP1, "line %d: Product nil", ln.ID)
				continue
			}
			if ln.Product.Category == nil {
				r.fail("Nested/Product.Category", reporter.SeverityP1, "line %d: Category nil", ln.ID)
				continue
			}
			if ln.Product.Category.Parent == nil || ln.Product.Category.Parent.ID != fx.rootCatID {
				r.fail("Nested/Category.Parent", reporter.SeverityP1,
					"line %d: deepest Parent not loaded (got %+v)", ln.ID, ln.Product.Category.Parent)
			}
		}
	}
	if totalLines != 3 { // 2 on order0 + 1 on order1
		r.fail("Nested/lineCount", reporter.SeverityP1, "walked %d lines across orders, want 3", totalLines)
	}
}

// selfRefTree: Category.Children recursing two levels (root → A → A1).
func selfRefTree(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	root, err := quark.For[domain.Category](ctx, c).Preload("Children.Children").Find(fx.rootCatID)
	if err != nil {
		r.fail("SelfRef/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if len(root.Children) != 1 {
		r.fail("SelfRef/L1", reporter.SeverityP1, "root has %d children, want 1 (A)", len(root.Children))
		return
	}
	if len(root.Children[0].Children) != 1 {
		r.fail("SelfRef/L2", reporter.SeverityP1, "child A has %d children, want 1 (A1)", len(root.Children[0].Children))
	}
}

// noNPlusOne: listing all customers with Preload("Orders") must batch the
// child load (1 customers query + 1 orders IN-query = 2), not issue one
// orders query per customer. With 4 customers a per-parent load would be 5.
func noNPlusOne(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture, counter *queryCounter) {
	r := newRec(t, eng, reporter.CategoryRegression)
	counter.reset()
	custs, err := quark.For[domain.Customer](ctx, c).Preload("Orders").List()
	if err != nil {
		r.fail("NPlusOne/List", reporter.SeverityP1, "list: %v", err)
		return
	}
	got := counter.count()
	if len(custs) < 4 {
		r.fail("NPlusOne/setup", reporter.SeverityP2, "expected >=4 customers, got %d", len(custs))
	}
	// Ideal is 2 (1 customers query + 1 batched orders IN-query). The bound is
	// a fixed small constant, independent of the parent count: with >=4
	// customers a per-parent load would be >=5, so anything <=3 proves
	// batching regardless of how the fixture grows.
	if got > 3 {
		r.fail("NPlusOne", reporter.SeverityP1,
			"Preload(Orders) over %d customers issued %d read queries — looks like N+1 (want ~2 batched)",
			len(custs), got)
	}
}

// cascadeCreate: Create with a belongs_to populated saves the parent and sets
// the FK (the cascade Quark DOES support). A fresh User pointing at a fresh
// Organization value: after Create, the user's organization_id must match the
// newly assigned org id.
func cascadeCreate(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	now := time.Now().UTC().Truncate(time.Second)
	newOrg := &domain.Organization{
		Name: "Cascade " + eng, Slug: "cascade-" + eng, Plan: "free",
		Settings:  quark.JSON[domain.OrgSettings]{V: domain.OrgSettings{DefaultLocale: "en"}},
		CreatedAt: now, UpdatedAt: now,
	}
	usr := &domain.User{
		Email: "cascade@" + eng + ".test", Locale: "en", CreatedAt: now,
		Organization: newOrg, // belongs_to populated, OrganizationID left zero
	}
	if err := quark.For[domain.User](ctx, c).Create(usr); err != nil {
		r.fail("Cascade/Create", reporter.SeverityP1, "create: %v", err)
		return
	}
	if newOrg.ID == 0 {
		r.fail("Cascade/parentSaved", reporter.SeverityP1, "parent Organization was not assigned an id")
		return
	}
	if usr.OrganizationID != newOrg.ID {
		r.fail("Cascade/fkSet", reporter.SeverityP1,
			"belongs_to FK not back-filled: user.OrganizationID=%d, org.ID=%d", usr.OrganizationID, newOrg.ID)
	}
	// Round-trip: the persisted row carries the FK.
	reloaded, err := quark.For[domain.User](ctx, c).Find(usr.ID)
	if err != nil {
		r.fail("Cascade/reload", reporter.SeverityP1, "reload: %v", err)
		return
	}
	if reloaded.OrganizationID != newOrg.ID {
		r.fail("Cascade/persisted", reporter.SeverityP1,
			"persisted organization_id=%d, want %d", reloaded.OrganizationID, newOrg.ID)
	}
}
