// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
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
	last string
}

func (r *txStatementRecorder) ObserveQuery(ev QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = ev.SQL
}

func (r *txStatementRecorder) read() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func txConfinementClient(t *testing.T, name string) (*Client, *txStatementRecorder) {
	t.Helper()
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithQueryObserver(rec),
	)
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
