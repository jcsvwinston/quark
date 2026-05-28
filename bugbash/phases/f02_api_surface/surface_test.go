// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f02_api_surface is bug-bash phase F2: query-builder surface
// coverage. Per engine it seeds a small deterministic fixture and
// exercises the builder, Expr AST, subqueries, aggregates, joins, set ops,
// locking, soft delete, batches, optimistic locking, preload, and window/
// CTE SQL generation. Failures aggregate via reporter.Fail.
package f02_api_surface

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
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

const phase = "f02_api_surface"

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

// rec binds reporter.Fail to a fixed engine + category for one group, so a
// failure is filed with the category that matches the group's nature (the
// bugbash-reporter subagent triages on it).
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
			Command: "go test -tags=bugbash -run TestAPISurface ./phases/f02_api_surface/... -engines=" + r.eng,
		},
	})
}

// countEq files a failure if q.Count() errors or differs from want.
func (r rec) countEq(name string, q *quark.Query[domain.Order], want int64) {
	r.t.Helper()
	got, err := q.Count()
	if err != nil {
		r.fail(name, reporter.SeverityP1, "Count: %v", err)
		return
	}
	if got != want {
		r.fail(name, reporter.SeverityP1, "Count = %d, want %d", got, want)
	}
}

// fixture holds the IDs of the seeded rows the subtests query against.
type fixture struct {
	orgID      int64
	customerID int64
	userID     int64
	childAID   int64
	orderIDs   []int64
}

func TestAPISurface(t *testing.T) {
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
			client, err := quark.New(conn.Driver, conn.DSN)
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

			// Read-only groups first (stable fixture), mutations last.
			t.Run("Predicates", func(t *testing.T) { predicates(t, ctx, client, eng, fx) })
			t.Run("Aggregates", func(t *testing.T) { aggregates(t, ctx, client, eng, fx) })
			t.Run("GroupByHaving", func(t *testing.T) { groupByHaving(t, ctx, client, eng, fx) })
			t.Run("OrderPaginate", func(t *testing.T) { orderPaginate(t, ctx, client, eng, fx) })
			t.Run("Streaming", func(t *testing.T) { streaming(t, ctx, client, eng, fx) })
			t.Run("Joins", func(t *testing.T) { joins(t, ctx, client, eng, fx) })
			t.Run("SetOps", func(t *testing.T) { setOps(t, ctx, client, eng, fx) })
			t.Run("Locking", func(t *testing.T) { locking(t, ctx, client, eng, fx) })
			t.Run("Preload", func(t *testing.T) { preload(t, ctx, client, eng, fx) })
			t.Run("WindowCTE", func(t *testing.T) { windowCTE(t, ctx, client, eng, fx) })
			// Mutating groups.
			t.Run("SoftDelete", func(t *testing.T) { softDelete(t, ctx, client, eng, fx) })
			t.Run("Batches", func(t *testing.T) { batches(t, ctx, client, eng, fx) })
			t.Run("OptimisticLock", func(t *testing.T) { optimisticLock(t, ctx, client, eng, fx) })
		})
	}
}

// seed inserts the deterministic fixture. Setup errors are blocking
// (t.Fatalf) — the query groups cannot run without it.
func seed(t *testing.T, ctx context.Context, c *quark.Client, eng string) fixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	dec := decimal.RequireFromString

	org := &domain.Organization{
		Name: "F2 " + eng, Slug: "f2-" + eng, Plan: "pro",
		Settings:  quark.JSON[domain.OrgSettings]{V: domain.OrgSettings{DefaultLocale: "es-ES", SLAHours: 24}},
		CreatedAt: now, UpdatedAt: now,
	}
	mustCreate(t, ctx, c, eng, org)

	root := &domain.Category{OrganizationID: org.ID, Name: "Root", Slug: "root", CreatedAt: now}
	mustCreate(t, ctx, c, eng, root)
	childA := &domain.Category{OrganizationID: org.ID, ParentID: &root.ID, Name: "A", Slug: "cat-a", Depth: 1, CreatedAt: now}
	mustCreate(t, ctx, c, eng, childA)

	var firstProductID int64
	for i, price := range []string{"10.00", "20.00", "30.00"} {
		p := &domain.Product{
			OrganizationID: org.ID, CategoryID: childA.ID,
			SKU: fmt.Sprintf("SKU-%s-%d", eng, i), Name: fmt.Sprintf("P%d", i),
			Price: dec(price), Currency: "EUR", Weight: dec("1.0"), Active: i < 2,
			CreatedAt: now,
		}
		mustCreate(t, ctx, c, eng, p)
		if i == 0 {
			firstProductID = p.ID
		}
	}

	cust := &domain.Customer{OrganizationID: org.ID, Name: "Acme", CreditLimit: dec("1000.00"), CreatedAt: now}
	mustCreate(t, ctx, c, eng, cust)

	fx := fixture{orgID: org.ID, customerID: cust.ID, childAID: childA.ID}
	statuses := []string{"paid", "paid", "pending", "cancelled"}
	totals := []string{"100.00", "200.00", "300.00", "400.00"}
	for i := range statuses {
		o := &domain.Order{
			OrganizationID: org.ID, CustomerID: cust.ID,
			Number: fmt.Sprintf("ORD-%s-%d", eng, i), Status: statuses[i],
			Subtotal: dec(totals[i]), Tax: dec("0.00"), Total: dec(totals[i]),
			Currency: "EUR", PlacedAt: now, CreatedAt: now,
		}
		mustCreate(t, ctx, c, eng, o)
		fx.orderIDs = append(fx.orderIDs, o.ID)
	}

	// One line each on the first two orders, for the join group. order_lines
	// has no deleted_at, so joining it does not trip BB-2.
	for _, oid := range fx.orderIDs[:2] {
		line := &domain.OrderLine{
			OrderID: oid, ProductID: firstProductID, Quantity: 1,
			UnitPrice: dec("10.00"), Discount: dec("0.00"), LineTotal: dec("10.00"), Sequence: 1,
		}
		mustCreate(t, ctx, c, eng, line)
	}

	user := &domain.User{OrganizationID: org.ID, Email: "u@" + eng + ".test", Locale: "es", CreatedAt: now}
	mustCreate(t, ctx, c, eng, user)
	fx.userID = user.ID
	return fx
}

func mustCreate[E any](t *testing.T, ctx context.Context, c *quark.Client, eng string, e *E) {
	t.Helper()
	if err := quark.For[E](ctx, c).Create(e); err != nil {
		t.Fatalf("seed %T on %s: %v", e, eng, err)
	}
}

func predicates(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	ord := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID)
	}
	r.countEq("Where", ord().Where("status", "=", "paid"), 2)
	r.countEq("WhereIn", ord().WhereIn("status", []any{"paid", "pending"}), 3)
	r.countEq("WhereBetween", ord().WhereBetween("total", 150, 350), 2)
	r.countEq("WhereNot", ord().WhereNot("status", "=", "cancelled"), 3)
	r.countEq("WhereExpr/Eq", ord().WhereExpr(quark.Eq(quark.Col("status"), quark.Lit("paid"))), 2)
	r.countEq("WhereExpr/And",
		ord().WhereExpr(quark.And(quark.Gt(quark.Col("total"), quark.Lit(150)), quark.Ne(quark.Col("status"), quark.Lit("cancelled")))), 2)
	r.countEq("Or", ord().Where("status", "=", "paid").Or(func(s *quark.Query[domain.Order]) *quark.Query[domain.Order] {
		return s.Where("status", "=", "pending")
	}), 3)

	// WhereJSON on a STRING JSON field (path bound per dialect). Not the int
	// sla_hours: jsonb_extract_path_text returns TEXT, so on PG an int param
	// cannot be encoded against it ("cannot find encode plan", inferred
	// inconsistently across plan-cache states → flaky). Text-to-text is
	// unambiguous on all six engines.
	if n, err := quark.For[domain.Organization](ctx, c).WhereJSON("settings", "default_locale", "=", "es-ES").Count(); err != nil {
		r.fail("WhereJSON", reporter.SeverityP1, "Count: %v", err)
	} else if n != 1 {
		r.fail("WhereJSON", reporter.SeverityP1, "Count = %d, want 1", n)
	}

	// Subquery: orders whose customer belongs to the org (InSub, no correlation).
	custSub, err := quark.For[domain.Customer](ctx, c).Where("organization_id", "=", fx.orgID).Select("id").AsSubquery()
	if err != nil {
		r.fail("InSub/AsSubquery", reporter.SeverityP1, "AsSubquery: %v", err)
	} else {
		r.countEq("InSub", quark.For[domain.Order](ctx, c).WhereExpr(quark.InSub(quark.Col("customer_id"), custSub)), 4)
	}
}

func aggregates(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	ord := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID)
	}
	for _, a := range []struct {
		name string
		fn   func() (float64, error)
		want float64
	}{
		{"Sum", func() (float64, error) { return ord().Sum("total") }, 1000},
		{"Avg", func() (float64, error) { return ord().Avg("total") }, 250},
		{"Min", func() (float64, error) { return ord().Min("total") }, 100},
		{"Max", func() (float64, error) { return ord().Max("total") }, 400},
	} {
		got, err := a.fn()
		if err != nil {
			r.fail("Aggregate/"+a.name, reporter.SeverityP1, "%s: %v", a.name, err)
			continue
		}
		if diff := got - a.want; diff > 0.001 || diff < -0.001 {
			r.fail("Aggregate/"+a.name, reporter.SeverityP1, "%s = %v, want %v", a.name, got, a.want)
		}
	}
	r.countEq("Count", ord(), 4)
}

// groupByHaving selects only the grouped column: a GROUP BY with the default
// SELECT * is invalid SQL on strict engines (MySQL only_full_group_by, MSSQL,
// Oracle ORA-00979) — that is SQL semantics, not a Quark bug. Execute-only:
// the grouped row does not map onto the Order struct.
func groupByHaving(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryDialectSpecific)
	base := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).Select("status").GroupBy("status")
	}
	if _, err := base().Having("status", "!=", "x").List(); err != nil {
		r.fail("GroupBy/Having", reporter.SeverityP1, "List: %v", err)
	}
	if _, err := base().HavingAggregate("COUNT", "id", ">", 0).List(); err != nil {
		r.fail("GroupBy/HavingAggregate", reporter.SeverityP1, "List: %v", err)
	}
	if _, err := base().HavingExpr(quark.Gt(quark.Func("COUNT", quark.Col("id")), quark.Lit(0))).List(); err != nil {
		r.fail("GroupBy/HavingExpr", reporter.SeverityP1, "List: %v", err)
	}
}

func orderPaginate(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	ord := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID)
	}
	if list, err := ord().OrderBy("total", "ASC").Limit(2).List(); err != nil {
		r.fail("OrderBy/Limit", reporter.SeverityP1, "List: %v", err)
	} else if len(list) != 2 || !list[0].Total.Equal(decimal.RequireFromString("100.00")) {
		r.fail("OrderBy/Limit", reporter.SeverityP1, "want first total 100, got %d rows first=%v", len(list), firstTotal(list))
	}
	if list, err := ord().OrderBy("total", "ASC").Limit(2).Offset(2).List(); err != nil {
		r.fail("Offset", reporter.SeverityP1, "List: %v", err)
	} else if len(list) != 2 || !list[0].Total.Equal(decimal.RequireFromString("300.00")) {
		r.fail("Offset", reporter.SeverityP1, "want first total 300 after offset 2, got %d rows first=%v", len(list), firstTotal(list))
	}
	if list, err := quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).Distinct().Select("status").List(); err != nil {
		r.fail("Distinct", reporter.SeverityP1, "List: %v", err)
	} else if len(list) != 3 {
		r.fail("Distinct", reporter.SeverityP1, "Distinct status got %d rows, want 3 (paid/pending/cancelled)", len(list))
	}
	if page, err := ord().OrderBy("total", "ASC").Paginate(2, 0); err != nil {
		r.fail("Paginate", reporter.SeverityP1, "Paginate: %v", err)
	} else if len(page.Items) != 2 || page.Total != 4 || page.TotalPages != 2 {
		r.fail("Paginate", reporter.SeverityP1, "got items=%d total=%d pages=%d, want 2/4/2", len(page.Items), page.Total, page.TotalPages)
	}
}

func firstTotal(list []domain.Order) string {
	if len(list) == 0 {
		return "<none>"
	}
	return list[0].Total.String()
}

func streaming(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	ord := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID)
	}
	n := 0
	if err := ord().Iter(func(domain.Order) error { n++; return nil }); err != nil {
		r.fail("Iter", reporter.SeverityP1, "Iter: %v", err)
	} else if n != 4 {
		r.fail("Iter", reporter.SeverityP1, "Iter saw %d rows, want 4", n)
	}

	cur, err := ord().Cursor()
	if err != nil {
		r.fail("Cursor", reporter.SeverityP1, "Cursor: %v", err)
		return
	}
	defer cur.Close()
	m := 0
	for cur.Next() {
		var o domain.Order
		if err := cur.Scan(&o); err != nil {
			r.fail("Cursor/Scan", reporter.SeverityP1, "Scan: %v", err)
			return // intentional: skip the m-count and Err() asserts, the cursor is unusable
		}
		m++
	}
	if err := cur.Err(); err != nil {
		r.fail("Cursor/Err", reporter.SeverityP1, "cursor err: %v", err)
	}
	if m != 4 {
		r.fail("Cursor", reporter.SeverityP1, "Cursor saw %d rows, want 4", m)
	}
}

// joins verifies JOIN SQL *generation* per dialect via AsSubquery. It does
// NOT execute the join into the typed struct: a typed For[T] query defaults
// to SELECT * across all joined tables, so executing collides on duplicate
// columns (NULL-scan on outer joins, silently-wrong values on inner joins)
// — that execution bug is BB-2 in TASKS.md § "Bug-bash hallazgos".
func joins(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryGap)
	if _, err := quark.For[domain.Order](ctx, c).
		Join("order_lines").On("orders.id", "=", "order_lines.order_id").AsSubquery(); err != nil {
		r.fail("Join/SQLgen", reporter.SeverityP1, "AsSubquery: %v", err)
	}
	if _, err := quark.For[domain.Order](ctx, c).
		LeftJoin("order_lines").On("orders.id", "=", "order_lines.order_id").AsSubquery(); err != nil {
		r.fail("LeftJoin/SQLgen", reporter.SeverityP1, "AsSubquery: %v", err)
	}
}

func setOps(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryDialectSpecific)
	paid := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).Where("status", "=", "paid")
	}
	pending := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).Where("status", "=", "pending")
	}
	// Union (DISTINCT) and UnionAll: 2 paid + 1 pending, no overlap → 3 each.
	if list, err := paid().Union(pending()).List(); err != nil {
		r.fail("Union", reporter.SeverityP1, "List: %v", err)
	} else if len(list) != 3 {
		r.fail("Union", reporter.SeverityP1, "union returned %d, want 3", len(list))
	}
	if list, err := paid().UnionAll(pending()).List(); err != nil {
		r.fail("UnionAll", reporter.SeverityP1, "List: %v", err)
	} else if len(list) != 3 {
		r.fail("UnionAll", reporter.SeverityP1, "union all returned %d, want 3", len(list))
	}

	// INTERSECT/EXCEPT are unsupported on MySQL/MariaDB — expect the sentinel
	// there rather than reporting a failure.
	mysqlFamily := eng == tools.MySQL || eng == tools.MariaDB
	inA := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).WhereIn("status", []any{"paid", "pending"})
	}
	inB := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).WhereIn("status", []any{"pending", "cancelled"})
	}
	dialectSetOp := func(name string, err error) {
		switch {
		case mysqlFamily && !errors.Is(err, quark.ErrUnsupportedFeature):
			r.fail(name, reporter.SeverityP1, "want ErrUnsupportedFeature on %s, got %v", eng, err)
		case !mysqlFamily && err != nil:
			r.fail(name, reporter.SeverityP1, "List: %v", err)
		}
	}
	_, err := inA().Intersect(inB()).List()
	dialectSetOp("Intersect", err)
	_, err = inA().Except(inB()).List()
	dialectSetOp("Except", err)
}

// locking is execute-only and dialect-aware. A clean ErrUnsupportedFeature
// is acceptable (SQLite has no row locking; some lock modes are
// engine-specific). Two known Quark findings are tolerated (logged, not
// failed — filed in TASKS.md, and regressions to OTHER errors are still
// caught):
//   - BB-3: MariaDB rejects FOR SHARE (Quark emits MySQL-8 syntax for the
//     shared "mysql" dialect; MariaDB needs LOCK IN SHARE MODE).
//   - BB-4: Oracle FOR UPDATE + the implicit List() Limit wrap → ORA-02014.
func locking(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryDialectSpecific)
	knownFinding := func(err error) bool {
		s := err.Error()
		// Anchor to the literal tokens the engines emit, to avoid masking an
		// unrelated error that merely mentions "SHARE".
		mariadbForShare := strings.Contains(s, "'SHARE'") && strings.Contains(s, "syntax") // BB-3
		oracleWrapView := strings.Contains(s, "ORA-02014")                                 // BB-4
		return mariadbForShare || oracleWrapView
	}
	ok := func(name string, err error) {
		switch {
		case err == nil, errors.Is(err, quark.ErrUnsupportedFeature):
			return
		case knownFinding(err):
			t.Logf("known finding (TASKS.md BB-3/BB-4): %s on %s: %v", name, eng, err)
		default:
			r.fail(name, reporter.SeverityP1, "unexpected lock error: %v", err)
		}
	}
	base := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).Where("status", "=", "pending")
	}
	_, err := base().ForUpdate().List()
	ok("ForUpdate", err)
	_, err = base().ForUpdate().SkipLocked().List()
	ok("ForUpdate/SkipLocked", err)
	_, err = base().ForUpdate().NoWait().List()
	ok("ForUpdate/NoWait", err)
	_, err = base().ForShare().List()
	ok("ForShare", err)
}

func preload(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	cust, err := quark.For[domain.Customer](ctx, c).Preload("Orders").Find(fx.customerID)
	if err != nil {
		r.fail("Preload/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if len(cust.Orders) != 4 {
		r.fail("Preload(Orders)", reporter.SeverityP1, "loaded %d orders, want 4", len(cust.Orders))
	}
}

// windowCTE validates dialect SQL generation. Window-function columns and
// CTE joins do not map onto a typed struct (the latter re-hits BB-2's
// SELECT * collision), so this exercises SQL generation via AsSubquery.
func windowCTE(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryGap)
	win := quark.Over(quark.RowNumber(), quark.NewWindow().PartitionBy(quark.Col("status")).OrderBy(quark.Col("total"), true))
	if _, err := quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).SelectExpr("rn", win).AsSubquery(); err != nil {
		r.fail("Window/SelectExpr", reporter.SeverityP1, "AsSubquery (window SQL gen): %v", err)
	}

	paidIDs, err := quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID).Where("status", "=", "paid").Select("id").AsSubquery()
	if err != nil {
		r.fail("CTE/AsSubquery", reporter.SeverityP1, "AsSubquery: %v", err)
		return
	}
	if _, err := quark.For[domain.Order](ctx, c).
		With("paid_ids", paidIDs).
		Join("paid_ids").On("orders.id", "=", "paid_ids.id").AsSubquery(); err != nil {
		r.fail("CTE/With", reporter.SeverityP1, "AsSubquery: %v", err)
	}
}

func softDelete(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	target, err := quark.For[domain.Order](ctx, c).Find(fx.orderIDs[0])
	if err != nil {
		r.fail("SoftDelete/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	if _, err := quark.For[domain.Order](ctx, c).Delete(&target); err != nil {
		r.fail("SoftDelete/Delete", reporter.SeverityP1, "delete: %v", err)
		return
	}
	cust := func() *quark.Query[domain.Order] {
		return quark.For[domain.Order](ctx, c).Where("customer_id", "=", fx.customerID)
	}
	r.countEq("SoftDelete/default-excludes", cust(), 3)
	r.countEq("SoftDelete/WithTrashed", cust().WithTrashed(), 4)
	r.countEq("SoftDelete/OnlyTrashed", cust().OnlyTrashed(), 1)
	if _, err := quark.For[domain.Order](ctx, c).Restore(&target); err != nil {
		r.fail("SoftDelete/Restore", reporter.SeverityP1, "restore: %v", err)
	}
	r.countEq("SoftDelete/after-restore", cust(), 4)
}

func batches(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	now := time.Now().UTC().Truncate(time.Second)
	dec := decimal.RequireFromString
	mk := func(tag string, price string) *domain.Product {
		return &domain.Product{
			OrganizationID: fx.orgID, CategoryID: fx.childAID,
			SKU: "BATCH-" + eng + "-" + tag, Name: "B" + tag,
			Price: dec(price), Currency: "EUR", Weight: dec("1.0"), Active: true, CreatedAt: now,
		}
	}
	before, _ := quark.For[domain.Product](ctx, c).Count()

	batch := []*domain.Product{mk("a", "1.00"), mk("b", "2.00")}
	if err := quark.For[domain.Product](ctx, c).CreateBatch(batch); err != nil {
		r.fail("CreateBatch", reporter.SeverityP1, "create batch: %v", err)
	}
	if after, _ := quark.For[domain.Product](ctx, c).Count(); after != before+2 {
		r.fail("CreateBatch", reporter.SeverityP1, "count %d -> %d, want +2", before, after)
	}

	// UpsertBatch on the SKU conflict updates name.
	batch[0].Name = "B-updated"
	if err := quark.For[domain.Product](ctx, c).UpsertBatch(batch, []string{"sku"}, []string{"name"}); err != nil {
		r.fail("UpsertBatch", reporter.SeverityP1, "upsert batch: %v", err)
	}

	// UpdateBatch by PK.
	for _, p := range batch {
		p.Currency = "USD"
	}
	if err := quark.For[domain.Product](ctx, c).UpdateBatch(batch); err != nil {
		r.fail("UpdateBatch", reporter.SeverityP1, "update batch: %v", err)
	}

	// UpdateMap with WHERE.
	if _, err := quark.For[domain.Product](ctx, c).Where("sku", "=", "BATCH-"+eng+"-a").UpdateMap(map[string]any{"active": false}); err != nil {
		r.fail("UpdateMap", reporter.SeverityP1, "update map: %v", err)
	}

	// DeleteBatch by ids, then DeleteBy with WHERE.
	ids := []any{batch[0].ID, batch[1].ID}
	if _, err := quark.For[domain.Product](ctx, c).DeleteBatch(ids); err != nil {
		r.fail("DeleteBatch", reporter.SeverityP1, "delete batch: %v", err)
	}
	if _, err := quark.For[domain.Product](ctx, c).Where("currency", "=", "no-such").DeleteBy(); err != nil {
		r.fail("DeleteBy", reporter.SeverityP2, "delete by: %v", err)
	}
}

func optimisticLock(t *testing.T, ctx context.Context, c *quark.Client, eng string, fx fixture) {
	r := newRec(t, eng, reporter.CategoryRegression)
	loaded, err := quark.For[domain.User](ctx, c).Find(fx.userID)
	if err != nil {
		r.fail("OptimisticLock/Find", reporter.SeverityP1, "find: %v", err)
		return
	}
	winner := loaded // value copy at the same version
	loser := loaded

	winner.Locale = "fr"
	if _, err := quark.For[domain.User](ctx, c).Update(&winner); err != nil {
		r.fail("OptimisticLock/winner", reporter.SeverityP1, "first update: %v", err)
		return
	}
	loser.Locale = "de"
	_, err = quark.For[domain.User](ctx, c).Update(&loser)
	if !errors.Is(err, quark.ErrStaleEntity) {
		r.fail("OptimisticLock/collision", reporter.SeverityP1,
			"stale update: got err=%v, want ErrStaleEntity", err)
	}
}
