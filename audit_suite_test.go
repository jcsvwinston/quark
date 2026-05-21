// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jcsvwinston/quark"
)

// auditWidget is the model exercised by the audit-log suite test.
type auditWidget struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	Qty  int    `db:"qty"`
}

// auditReadRow maps the quark_audit table so the test can read audit
// rows back through the normal builder (reads are not audited, so
// this does not recurse). The Go field for the table_name column is
// TgtTable to avoid colliding with the TableName() method.
type auditReadRow struct {
	ID        int64                      `db:"id" pk:"true"`
	TenantID  string                     `db:"tenant_id"`
	UserID    string                     `db:"user_id"`
	TgtTable  string                     `db:"table_name"`
	Operation string                     `db:"operation"`
	PK        string                     `db:"pk"`
	Diff      quark.JSON[map[string]any] `db:"diff"`
}

func (auditReadRow) TableName() string { return "quark_audit" }

type auditCtxKey string

const (
	auditUserKey   auditCtxKey = "audit_user"
	auditTenantKey auditCtxKey = "audit_tenant"
)

// testAuditLog exercises the F5-7 audit log end-to-end on whatever
// engine the SharedSuite is running against. It enables auditing on
// the shared client (this is the last suite entry, so no later test
// is affected), then verifies created/updated/deleted rows land in
// quark_audit with the right operation, attribution, and diff shape.
func testAuditLog(ctx context.Context, t *testing.T, client *quark.Client) {
	cfg := quark.AuditConfig{
		UserFromContext: func(c context.Context) string {
			if v, ok := c.Value(auditUserKey).(string); ok {
				return v
			}
			return ""
		},
		TenantFromContext: func(c context.Context) string {
			if v, ok := c.Value(auditTenantKey).(string); ok {
				return v
			}
			return ""
		},
	}
	if err := client.EnableAuditLog(ctx, cfg); err != nil {
		t.Fatalf("EnableAuditLog: %v", err)
	}
	// Audit logging mutates the shared client. Turn it back off when
	// this subtest finishes so the assumption "AuditLog is the last
	// SharedSuite entry" stops being load-bearing.
	t.Cleanup(client.DisableAuditLog)

	if err := client.Migrate(ctx, &auditWidget{}); err != nil {
		t.Fatalf("migrate auditWidget: %v", err)
	}
	// Clean prior rows for this table so re-runs are deterministic.
	dropTable(client, "audit_widgets")
	if err := client.Migrate(ctx, &auditWidget{}); err != nil {
		t.Fatalf("re-migrate auditWidget: %v", err)
	}

	actx := context.WithValue(ctx, auditUserKey, "u1")
	actx = context.WithValue(actx, auditTenantKey, "t1")

	readAudit := func(t *testing.T, op string, pk string) auditReadRow {
		t.Helper()
		rows, err := quark.For[auditReadRow](ctx, client).
			Where("table_name", "=", "audit_widgets").
			Where("operation", "=", op).
			Where("pk", "=", pk).
			OrderBy("id", "DESC").
			Limit(1).
			List()
		if err != nil {
			t.Fatalf("read audit (%s/%s): %v", op, pk, err)
		}
		if len(rows) == 0 {
			t.Fatalf("no audit row for %s/%s", op, pk)
		}
		return rows[0]
	}

	// --- Create → "created" with full-row diff + attribution ---
	w := &auditWidget{Name: "foo", Qty: 1}
	if err := quark.For[auditWidget](actx, client).Create(w); err != nil {
		t.Fatalf("create: %v", err)
	}
	pk := fmt.Sprint(w.ID)

	created := readAudit(t, "created", pk)
	if created.UserID != "u1" || created.TenantID != "t1" {
		t.Errorf("created attribution = user %q tenant %q, want u1/t1", created.UserID, created.TenantID)
	}
	if got := fmt.Sprint(created.Diff.V["name"]); got != "foo" {
		t.Errorf("created diff name = %q, want foo", got)
	}
	// JSON numbers round-trip as float64.
	if got := fmt.Sprint(created.Diff.V["qty"]); got != "1" {
		t.Errorf("created diff qty = %q, want 1", got)
	}

	// --- Tracked.Save update → "updated" with {old,new} delta ---
	tracked, err := quark.For[auditWidget](actx, client).Track().Find(w.ID)
	if err != nil {
		t.Fatalf("track find: %v", err)
	}
	tracked.Entity.Name = "bar"
	if _, err := tracked.Save(actx); err != nil {
		t.Fatalf("tracked save: %v", err)
	}
	updated := readAudit(t, "updated", pk)
	delta, ok := updated.Diff.V["name"].(map[string]any)
	if !ok {
		t.Fatalf("updated diff name is not an {old,new} object: %T %v", updated.Diff.V["name"], updated.Diff.V["name"])
	}
	if fmt.Sprint(delta["old"]) != "foo" || fmt.Sprint(delta["new"]) != "bar" {
		t.Errorf("updated diff name = {old:%v,new:%v}, want {old:foo,new:bar}", delta["old"], delta["new"])
	}

	// --- Delete → "deleted" with full-row diff ---
	if _, err := quark.For[auditWidget](actx, client).Delete(&auditWidget{ID: w.ID}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	deleted := readAudit(t, "deleted", pk)
	if deleted.Operation != "deleted" {
		t.Errorf("deleted operation = %q", deleted.Operation)
	}

	// --- ExcludeTables filter: a write to an excluded table is NOT audited ---
	if err := client.EnableAuditLog(ctx, quark.AuditConfig{
		ExcludeTables: []string{"audit_widgets"},
	}); err != nil {
		t.Fatalf("re-enable audit with exclude: %v", err)
	}
	w2 := &auditWidget{Name: "skip", Qty: 9}
	if err := quark.For[auditWidget](actx, client).Create(w2); err != nil {
		t.Fatalf("create excluded: %v", err)
	}
	rows, err := quark.For[auditReadRow](ctx, client).
		Where("table_name", "=", "audit_widgets").
		Where("pk", "=", fmt.Sprint(w2.ID)).
		List()
	if err != nil {
		t.Fatalf("read audit after exclude: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("excluded table produced %d audit rows, want 0", len(rows))
	}

	// Restore default config (no filters); t.Cleanup disables audit
	// entirely when the subtest returns.
	_ = client.EnableAuditLog(ctx, cfg)
}

// TestF5_7_AuditAtomicWithTxRollback proves the "junto al commit, no
// después" guarantee (ADR-0013): the audit row is written on the same
// transaction as the CRUD, so a rollback discards BOTH the data and
// its audit trail. This is the key difference from the EventBus
// (F5-6), whose post-commit emission can be lost on a crash. Runs on
// SQLite directly — the atomicity is a Go/transaction property, not
// dialect-specific.
func TestF5_7_AuditAtomicWithTxRollback(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.EnableAuditLog(ctx, quark.AuditConfig{}); err != nil {
		t.Fatalf("EnableAuditLog: %v", err)
	}
	if err := client.Migrate(ctx, &auditWidget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	sentinel := errors.New("force-rollback")
	var createdID int64
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		w := &auditWidget{Name: "rollback-me", Qty: 1}
		if err := quark.ForTx[auditWidget](ctx, tx).Create(w); err != nil {
			return err
		}
		createdID = w.ID
		return sentinel // triggers rollback
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx returned %v, want sentinel", err)
	}

	// The widget must NOT exist (rolled back).
	if _, err := quark.For[auditWidget](ctx, client).Find(createdID); !errors.Is(err, quark.ErrNotFound) {
		t.Errorf("widget should have rolled back, Find err = %v (want ErrNotFound)", err)
	}
	// And critically: NO audit row either — it joined the same tx.
	rows, err := quark.For[auditReadRow](ctx, client).
		Where("table_name", "=", "audit_widgets").
		Limit(10).
		List()
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("audit row survived a rolled-back tx (%d rows) — not atomic", len(rows))
	}
}

// TestF5_7_TrackedSaveAuditAtomicWithTxRollback covers the same
// atomicity guarantee for the dirty-tracking path, which has its OWN
// audit write (in dirty_track.go, separate from query_crud.go). A
// Tracked.Save inside a rolled-back ForTx must discard both the
// UPDATE and its {old,new} audit row.
func TestF5_7_TrackedSaveAuditAtomicWithTxRollback(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.EnableAuditLog(ctx, quark.AuditConfig{}); err != nil {
		t.Fatalf("EnableAuditLog: %v", err)
	}
	if err := client.Migrate(ctx, &auditWidget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed a committed row (this audits a "created" entry).
	w := &auditWidget{Name: "orig", Qty: 1}
	if err := quark.For[auditWidget](ctx, client).Create(w); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	sentinel := errors.New("force-rollback")
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		tracked, terr := quark.ForTx[auditWidget](ctx, tx).Track().Find(w.ID)
		if terr != nil {
			return terr
		}
		tracked.Entity.Name = "changed"
		if _, terr := tracked.Save(ctx); terr != nil {
			return terr
		}
		return sentinel // roll back the UPDATE + its audit row
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx returned %v, want sentinel", err)
	}

	// The row's name must still be "orig" (update rolled back).
	got, ferr := quark.For[auditWidget](ctx, client).Find(w.ID)
	if ferr != nil {
		t.Fatalf("find: %v", ferr)
	}
	if got.Name != "orig" {
		t.Errorf("name = %q, want orig (update should have rolled back)", got.Name)
	}
	// No "updated" audit row survived.
	updatedRows, err := quark.For[auditReadRow](ctx, client).
		Where("table_name", "=", "audit_widgets").
		Where("operation", "=", "updated").
		Limit(10).
		List()
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(updatedRows) != 0 {
		t.Errorf("Tracked.Save audit row survived rollback (%d rows) — not atomic", len(updatedRows))
	}
}
