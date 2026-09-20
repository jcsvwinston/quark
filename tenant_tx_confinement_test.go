// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-26. Tenant confinement used to stop at the transaction boundary.
//
// For[T] applies it when the provider is a *TenantRouter — the schema prefix
// under SchemaPerTenant, the injected predicate under RowLevelSecurityClient.
// ForTx[T] built its query from the *Tx alone, which knew nothing about a
// router, and TenantRouter.Tx handed back a bare *Tx for every strategy that
// is not Native. So inside router.Tx a query read every tenant's rows and
// wrote to the default schema, with no error anywhere.
//
// These tests read the STATEMENT rather than the rows. Two different queries
// can return the same rows on a fixture this small, and only one of them is
// the one being measured; the clause is the fact.

type txConfinementRow struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Label    string `db:"label"`
}

func (txConfinementRow) TableName() string { return "tx_confinement_rows" }

type txStatementRecorder struct {
	mu   sync.Mutex
	all  []string
	last string
	args []any // the binds of the last statement
}

func (r *txStatementRecorder) ObserveQuery(ev QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all = append(r.all, ev.SQL)
	r.last = ev.SQL
	r.args = ev.Args
}

func (r *txStatementRecorder) read() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// reset forgets what was recorded so far, so a test can ask "what ran since".
func (r *txStatementRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.all, r.last, r.args = nil, "", nil
}

// any reports whether some statement recorded since the last reset contains
// needle.
func (r *txStatementRecorder) any(needle string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.all {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func (r *txStatementRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.all)
}

func txConfinementClient(t *testing.T, name string, opts ...any) (*Client, *txStatementRecorder) {
	t.Helper()
	rec := &txStatementRecorder{}
	opts = append([]any{
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithQueryObserver(rec),
	}, opts...)
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared", opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Migrate(context.Background(), &txConfinementRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return c, rec
}

type txConfinementTenantKey struct{}

func txConfinementCtx(tenant string) context.Context {
	return context.WithValue(context.Background(), txConfinementTenantKey{}, tenant)
}

func txConfinementResolver(ctx context.Context) string {
	tenant, _ := ctx.Value(txConfinementTenantKey{}).(string)
	return tenant
}

func txConfinementRouter(base *Client, strategy TenantStrategy) *TenantRouter {
	cfg := DefaultTenantConfig()
	cfg.Strategy = strategy
	cfg.BaseClient = base
	cfg.TenantColumn = "tenant_id"
	return NewTenantRouter(cfg, txConfinementResolver, nil)
}

// A SchemaPerTenant query inside router.Tx has to carry the same schema
// prefix it carries outside. SQLite has no such schema, so the prefix shows up
// in the statement or in the error naming the table it could not find — either
// way it is the qualified name that proves the confinement reached the query.
func TestSchemaPerTenantSurvivesTransaction(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_schema")
	router := txConfinementRouter(c, SchemaPerTenant)
	ctx := txConfinementCtx("acme")

	var inside string
	if err := router.Tx(ctx, func(tx *Tx) error {
		_, err := ForTx[txConfinementRow](ctx, tx).List()
		inside = rec.read()
		if err != nil {
			inside += " " + err.Error()
		}
		return nil
	}); err != nil {
		t.Fatalf("router.Tx: %v", err)
	}

	if !strings.Contains(inside, "acme.tx_confinement_rows") {
		t.Fatalf("inside router.Tx the query is not confined to the tenant's schema: %s\n\n"+
			"This is QK-26: ForTx built its query with no schema, so the read hit the "+
			"shared default-schema table and a write would have landed there too.", inside)
	}
}

// And a RowLevelSecurityClient query inside router.Tx has to carry the same
// predicate. Here the table name does not change, so the predicate itself is
// the only evidence.
func TestRowLevelSecurityClientSurvivesTransaction(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_clientside")
	router := txConfinementRouter(c, RowLevelSecurityClient)
	ctx := txConfinementCtx("acme")

	var inside string
	if err := router.Tx(ctx, func(tx *Tx) error {
		if _, err := ForTx[txConfinementRow](ctx, tx).List(); err != nil {
			return err
		}
		inside = rec.read()
		return nil
	}); err != nil {
		t.Fatalf("router.Tx: %v", err)
	}

	if !strings.Contains(strings.ToLower(inside), "tenant_id") {
		t.Fatalf("inside router.Tx the query carries no tenant predicate: %s\n\n"+
			"This is QK-26: the read returned every tenant's rows.", inside)
	}
}

// A write is the half that matters most, because a read that leaks is visible
// and a write that lands in the wrong place is not.
func TestSchemaPerTenantWriteSurvivesTransaction(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_write")
	router := txConfinementRouter(c, SchemaPerTenant)
	ctx := txConfinementCtx("acme")

	var stmt string
	_ = router.Tx(ctx, func(tx *Tx) error {
		row := txConfinementRow{TenantID: "acme", Label: "written inside"}
		err := ForTx[txConfinementRow](ctx, tx).Create(&row)
		stmt = rec.read()
		if err != nil {
			stmt += " " + err.Error()
		}
		return nil
	})

	if !strings.Contains(stmt, "acme.tx_confinement_rows") {
		t.Fatalf("a write inside router.Tx is not confined to the tenant's schema: %s\n\n"+
			"This is the half of QK-26 that leaves no trace: the row lands in the "+
			"shared table and nothing reports it.", stmt)
	}
}

// QK-26, second half: under RowLevelSecurityNative the statements of a query
// built inside router.Tx have to travel on the transaction's own connection.
// Giving it the native executor sends them down a second connection, which
// commits writes the outer rollback cannot undo.
func TestNativeRLSInsideTxKeepsTheTransactionExecutor(t *testing.T) {
	db, err := sql.Open("sqlite", "file:qk26_native_exec?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, err := NewWithDB("postgres", db, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	router := txConfinementRouter(c, RowLevelSecurityNative)
	router.config.SkipPolicyVerification = true // this test measures the executor, not the policies
	ctx := txConfinementCtx("acme")

	sqlTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = sqlTx.Rollback() }()
	tx := &Tx{tx: sqlTx, client: c, ctx: ctx, router: router}

	q := ForTx[txConfinementRow](ctx, tx)
	if _, ok := q.exec.(*sql.Tx); !ok {
		t.Fatalf("a query built inside the router's transaction runs on %T instead of the transaction: "+
			"its statements leave the transaction, so a write survives the rollback and a read "+
			"misses the snapshot", q.exec)
	}
	if q.tenantID != "acme" {
		t.Fatalf("the tenant is not stamped (%q): the cache key stops being tenant-scoped", q.tenantID)
	}
}

// ---------------------------------------------------------------------------
// What the adversarial review of the first cut found, one test per finding.
// Each was checked by reverting the corresponding fix and watching it fail.
// ---------------------------------------------------------------------------

// Finding 1. Create did not consult q.err. A query can fail to build with its
// client already set — a RowLevelSecurityNative router over an engine that is
// not PostgreSQL, or, inside a transaction, a tenant the transaction refuses —
// and every read path returned that error, but the write path minted its own
// BaseQuery copies without it and executeQueryRow never looked. The INSERT
// went to the engine unfiltered, with no complaint. A query that failed to
// build runs nothing.
//
// (With NO tenant in the context For[T] never gets a client, so Create was
// already refused there — by "client not initialized", which is the wrong
// reason. The two strategies below pin that door too, for the message.)
func TestCreateOnAQueryThatFailedToBuildWritesNothing(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_create_native_sqlite")
	router := txConfinementRouter(c, RowLevelSecurityNative)
	rec.reset()
	row := txConfinementRow{TenantID: "acme", Label: "must not land"}
	err := For[txConfinementRow](txConfinementCtx("acme"), router).Create(&row)
	if !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("Create under RowLevelSecurityNative on sqlite returned %v, want ErrUnsupportedFeature", err)
	}
	if rec.any("INSERT") {
		t.Fatalf("Create under a strategy the dialect refuses still ran its INSERT: %v\n\n"+
			"The query carried the refusal and the write ignored it — the row landed with "+
			"no isolation at all, on an engine that has no policy to catch it.", rec.all)
	}
	if n, _ := For[txConfinementRow](context.Background(), c).Count(); n != 0 {
		t.Fatalf("a row was written under a refused strategy (count=%d)", n)
	}

	for _, strategy := range []TenantStrategy{SchemaPerTenant, RowLevelSecurityClient} {
		c, rec := txConfinementClient(t, "qk26_create_notenant_"+strategyName(strategy))
		router := txConfinementRouter(c, strategy)
		rec.reset()
		row := txConfinementRow{TenantID: "acme", Label: "must not land"}
		if err := For[txConfinementRow](context.Background(), router).Create(&row); err == nil {
			t.Fatalf("%s: Create with no tenant in the context succeeded", strategyName(strategy))
		}
		if rec.any("INSERT") {
			t.Fatalf("%s: Create with no tenant in the context still ran its INSERT: %v", strategyName(strategy), rec.all)
		}
	}
}

// Finding 2. The first cut confined only router.Tx. GetClient(ctx) + client.Tx
// handed back a bare *Tx: the same leak through the other door. The BaseClient
// now knows its router, and a transaction opened on it with a tenant in the
// context is confined in BeginTx.
func TestClientTxOnTheBaseClientIsConfined(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_clienttx_schema")
	router := txConfinementRouter(c, SchemaPerTenant)
	ctx := txConfinementCtx("acme")

	client, err := router.GetClient(ctx)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	var inside string
	_ = client.Tx(ctx, func(tx *Tx) error {
		_, err := ForTx[txConfinementRow](ctx, tx).List()
		inside = rec.read()
		if err != nil {
			inside += " " + err.Error()
		}
		return nil
	})
	if !strings.Contains(inside, "acme.tx_confinement_rows") {
		t.Fatalf("a transaction opened with router.GetClient(ctx) + client.Tx is not confined: %s\n\n"+
			"router.Tx was confined and this door was not — the same leak, one call away.", inside)
	}

	// And the client-side predicate through the same door.
	c2, rec2 := txConfinementClient(t, "qk26_clienttx_clientside")
	router2 := txConfinementRouter(c2, RowLevelSecurityClient)
	client2, _ := router2.GetClient(ctx)
	if err := client2.Tx(ctx, func(tx *Tx) error {
		_, err := ForTx[txConfinementRow](ctx, tx).List()
		return err
	}); err != nil {
		t.Fatalf("client.Tx under RowLevelSecurityClient: %v", err)
	}
	if !strings.Contains(strings.ToLower(rec2.read()), "tenant_id") {
		t.Fatalf("inside client.Tx the query carries no tenant predicate: %s", rec2.read())
	}
}

// The same door with NO tenant in the context stays what it always was: a
// transaction on the shared pool, unconfined, like For[T] on the base client.
// This is documented, not accidental — there is nobody to confine it to, and
// migrations and provisioning run exactly this way.
func TestClientTxWithoutTenantStaysOnTheSharedPool(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_clienttx_notenant")
	_ = txConfinementRouter(c, SchemaPerTenant)

	if err := c.Tx(context.Background(), func(tx *Tx) error {
		_, err := ForTx[txConfinementRow](context.Background(), tx).List()
		return err
	}); err != nil {
		t.Fatalf("client.Tx with no tenant in the context: %v", err)
	}
	if strings.Contains(rec.read(), ".tx_confinement_rows") {
		t.Fatalf("a transaction with no tenant got a schema from somewhere: %s", rec.read())
	}
}

// Under RowLevelSecurityNative the confinement of a transaction is the
// set_config on its connection. Through client.Tx on the stamped BaseClient it
// has to run too — otherwise the engine's policy filters by nothing and every
// statement inside sees zero rows, or with a permissive policy every row.
// SQLite has no set_config, so the attempt is what we can observe: the
// transaction fails naming it, instead of opening unarmed.
func TestNativeClientTxOnTheBaseClientArmsTheSessionVariable(t *testing.T) {
	db, err := sql.Open("sqlite", "file:qk26_native_clienttx?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c, err := NewWithDB("postgres", db, WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_ = txConfinementRouter(c, RowLevelSecurityNative)

	called := false
	err = c.Tx(txConfinementCtx("acme"), func(tx *Tx) error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "set_config") {
		t.Fatalf("client.Tx on a Native router's BaseClient did not arm the session variable (err=%v, fn called=%v): "+
			"the transaction opened with the policy filtering by nothing", err, called)
	}
	if called {
		t.Fatal("fn ran inside a transaction whose tenant variable failed to set")
	}
}

// Finding 4. Under DatabasePerTenant the pool IS the confinement, and before
// the first cut a ForTx with a plain context inside router.Tx simply worked.
// The first cut re-resolved the tenant from the query's context and failed it.
// The transaction's tenant rules (ADR-0025): a context with no tenant inherits
// it.
func TestDatabasePerTenantForTxWithPlainContextInheritsTheTenant(t *testing.T) {
	cfg := DefaultTenantConfig()
	cfg.Strategy = DatabasePerTenant
	router := NewTenantRouter(cfg, txConfinementResolver, func(tenant string) (*Client, error) {
		c, _ := txConfinementClient(t, "qk26_dbper_"+tenant)
		row := txConfinementRow{TenantID: tenant, Label: "own"}
		if err := For[txConfinementRow](context.Background(), c).Create(&row); err != nil {
			return nil, err
		}
		return c, nil
	})

	var got []txConfinementRow
	err := router.Tx(txConfinementCtx("acme"), func(tx *Tx) error {
		var err error
		got, err = ForTx[txConfinementRow](context.Background(), tx).List()
		return err
	})
	if err != nil {
		t.Fatalf("ForTx with a plain context inside router.Tx under DatabasePerTenant: %v\n\n"+
			"This is the regression the review found: the pool already confines the "+
			"transaction, and re-asking the query's context for a tenant fails what used to work.", err)
	}
	if len(got) != 1 || got[0].TenantID != "acme" {
		t.Fatalf("the transaction did not read its own tenant's pool: %+v", got)
	}
}

// Finding 6, decided and written (ADR-0025): the transaction fixes the tenant.
// A query context inside it that resolves ANOTHER tenant is refused with
// ErrTenantMismatch — under Native the engine is already filtering by the
// transaction's tenant, so honouring the query's would silently cross tenants
// — and the refusal covers writes too (finding 1 through the transactional
// path).
func TestForTxRefusesAnotherTenantInsideTheTransaction(t *testing.T) {
	c, rec := txConfinementClient(t, "qk26_mismatch")
	router := txConfinementRouter(c, SchemaPerTenant)

	err := router.Tx(txConfinementCtx("acme"), func(tx *Tx) error {
		rec.reset()
		if _, err := ForTx[txConfinementRow](txConfinementCtx("globex"), tx).List(); !errors.Is(err, ErrTenantMismatch) {
			t.Fatalf("a query for tenant globex inside acme's transaction returned %v, want ErrTenantMismatch", err)
		}
		row := txConfinementRow{TenantID: "globex", Label: "must not land"}
		if err := ForTx[txConfinementRow](txConfinementCtx("globex"), tx).Create(&row); !errors.Is(err, ErrTenantMismatch) {
			t.Fatalf("a write for tenant globex inside acme's transaction returned %v, want ErrTenantMismatch", err)
		}
		if rec.count() != 0 {
			t.Fatalf("statements ran for the refused tenant: %v", rec.all)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("router.Tx: %v", err)
	}
}

// Finding 3. Preload built the relation's SELECT with the bare table name, so
// inside a SchemaPerTenant query the children came from the default schema
// while the parents came from the tenant's. SQLite lets the test attach a
// database under the tenant's name, which is what makes the evidence positive:
// the children read back are the ones that live in acme's schema.
type txConfinementParent struct {
	ID       int64                `db:"id" pk:"true"`
	TenantID string               `db:"tenant_id"`
	Children []txConfinementChild `rel:"has_many" join:"parent_id"`
}

func (txConfinementParent) TableName() string { return "tx_conf_parents" }

type txConfinementChild struct {
	ID       int64  `db:"id" pk:"true"`
	ParentID int64  `db:"parent_id"`
	TenantID string `db:"tenant_id"`
	Label    string `db:"label"`
}

func (txConfinementChild) TableName() string { return "tx_conf_children" }

func TestPreloadInsideSchemaPerTenantQualifiesTheRelationTable(t *testing.T) {
	// One connection, so the ATTACH below is visible to every statement.
	c, rec := txConfinementClient(t, "qk26_preload", WithMaxOpenConns(1))
	ctx := context.Background()
	for _, stmt := range []string{
		"ATTACH DATABASE ':memory:' AS acme",
		"CREATE TABLE acme.tx_conf_parents (id INTEGER PRIMARY KEY, tenant_id TEXT)",
		"CREATE TABLE acme.tx_conf_children (id INTEGER PRIMARY KEY, parent_id INTEGER, tenant_id TEXT, label TEXT)",
		"CREATE TABLE tx_conf_parents (id INTEGER PRIMARY KEY, tenant_id TEXT)",
		"CREATE TABLE tx_conf_children (id INTEGER PRIMARY KEY, parent_id INTEGER, tenant_id TEXT, label TEXT)",
		"INSERT INTO acme.tx_conf_parents VALUES (1, 'acme')",
		"INSERT INTO acme.tx_conf_children VALUES (10, 1, 'acme', 'in acme schema')",
		"INSERT INTO tx_conf_parents VALUES (1, 'shared')",
		"INSERT INTO tx_conf_children VALUES (99, 1, 'shared', 'in the shared table')",
	} {
		// Straight on the pool: raw SQL through the client is off by default,
		// and this is fixture, not the thing being measured.
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	router := txConfinementRouter(c, SchemaPerTenant)
	rec.reset()

	parents, err := For[txConfinementParent](txConfinementCtx("acme"), router).Preload("Children").List()
	if err != nil {
		t.Fatalf("preload under SchemaPerTenant: %v", err)
	}
	if !rec.any(`"acme"."tx_conf_children"`) {
		t.Fatalf("the relation's SELECT is not qualified with the tenant's schema: %v\n\n"+
			"The parents came from acme's schema and the children from the shared table.", rec.all)
	}
	if len(parents) != 1 || len(parents[0].Children) != 1 || parents[0].Children[0].Label != "in acme schema" {
		t.Fatalf("the children read back are not acme's: %+v", parents)
	}
}

func strategyName(s TenantStrategy) string {
	switch s {
	case DatabasePerTenant:
		return "DatabasePerTenant"
	case SchemaPerTenant:
		return "SchemaPerTenant"
	case RowLevelSecurityClient:
		return "RowLevelSecurityClient"
	case RowLevelSecurityNative:
		return "RowLevelSecurityNative"
	}
	return "unknown"
}
