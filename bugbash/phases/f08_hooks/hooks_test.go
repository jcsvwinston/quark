// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f08_hooks is bug-bash phase F8: transactional semantics of hooks,
// events and the audit log, per engine.
//
//   - Savepoints: 5-level nested tx.Tx; rolling back the inner levels drops
//     both their data rows AND their queued OnCommit hooks, while the outer
//     levels commit (savepoint hook-queue truncation, ADR-0013).
//   - OnCommit / OnRollback fire-or-discard by outcome; an erroring callback
//     is logged, not fatal (the data stays committed).
//   - EventBus: post-commit publish; a bus error does not roll back the write.
//   - Audit log: N single writes → N quark_audit rows with a valid diff;
//     audit is inline-in-tx, so a rolled-back tx leaves neither data nor audit
//     (the atomicity guarantee — the spec's kill-9 is simulated by a rollback).
//   - Hooks: BeforeFind aborts a read; AfterFind error propagates;
//     TxFromContext is reachable from a lifecycle hook dispatched via ForTx.
//
// Sub-tests share the engine's tables (server engines), so each asserts on its
// own uniquely-named rows or on deltas, never on absolute table counts.
//
// Documented gaps (cited, not tested as working):
//   - BeforeFind cannot mutate the query — the hook only receives ctx and can
//     only abort by returning an error (hooks.go). The spec's "BeforeFind that
//     adds a Where" is not in the API.
//   - 100k audit rows / real kill-9 are F14 soak-tier; F8 scales down (logged)
//     and simulates the crash with a rollback.
package f08_hooks

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f08_hooks"

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

// ev is the generic write subject (savepoints, OnCommit, events, audit).
type ev struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
	N    int    `db:"n"`
}

// findGuard.BeforeFind aborts a read when blockFind is set. Toggled per
// sub-test (engines run sequentially, so a package toggle is safe).
type findGuard struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

var (
	blockFind      atomic.Bool
	errFindBlocked = errors.New("f08: BeforeFind blocked the read")
)

func (findGuard) BeforeFind(ctx context.Context) error {
	if blockFind.Load() {
		return errFindBlocked
	}
	return nil
}

// afterGuard.AfterFind errors when failAfterFind is set.
type afterGuard struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

var (
	failAfterFind    atomic.Bool
	errAfterFindHook = errors.New("f08: AfterFind failed")
)

func (afterGuard) AfterFind(ctx context.Context) error {
	if failAfterFind.Load() {
		return errAfterFindHook
	}
	return nil
}

// txProbe.BeforeCreate records whether TxFromContext sees the active tx.
type txProbe struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

var txProbeSawTx atomic.Bool

func (txProbe) BeforeCreate(ctx context.Context) error {
	if quark.TxFromContext(ctx) != nil {
		txProbeSawTx.Store(true)
	}
	return nil
}

func allModels() []any { return []any{&ev{}, &findGuard{}, &afterGuard{}, &txProbe{}} }

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
		Phase: phase, Test: name, Engine: r.eng, Category: r.cat, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestHooks ./phases/f08_hooks/... -engines=" + r.eng,
		},
	})
}

// marker is a goroutine-safe FIFO recorder for hook firings.
type marker struct {
	mu   sync.Mutex
	seen []string
}

func (m *marker) hook(name string) func(context.Context) error {
	return func(context.Context) error {
		m.mu.Lock()
		m.seen = append(m.seen, name)
		m.mu.Unlock()
		return nil
	}
}
func (m *marker) list() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}
func (m *marker) has(name string) bool {
	for _, s := range m.list() {
		if s == name {
			return true
		}
	}
	return false
}

func TestHooks(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var ce []string
		for _, e := range engines {
			if e != tools.SQLite {
				ce = append(ce, e)
			}
		}
		tools.Down(ce...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			t.Run("SavepointTruncation", func(t *testing.T) { savepointTruncation(t, ctx, conn, eng) })
			t.Run("OnCommitOnRollback", func(t *testing.T) { onCommitOnRollback(t, ctx, conn, eng) })
			t.Run("EventBus", func(t *testing.T) { eventBus(t, ctx, conn, eng) })
			t.Run("AuditLog", func(t *testing.T) { auditLog(t, ctx, conn, eng) })
			t.Run("AuditAtomicity", func(t *testing.T) { auditAtomicity(t, ctx, conn, eng) })
			t.Run("FindHooks", func(t *testing.T) { findHooks(t, ctx, conn, eng) })
			t.Run("TxFromContext", func(t *testing.T) { txFromContext(t, ctx, conn, eng) })
		})
	}
}

func newClient(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) *quark.Client {
	t.Helper()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(limits))
	if err != nil {
		t.Fatalf("quark.New(%q): %v", conn.Driver, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		if eng == tools.SQLite {
			_ = os.Remove(conn.DSN)
		}
	})
	if err := client.Migrate(ctx, allModels()...); err != nil {
		t.Fatalf("migrate on %s: %v", eng, err)
	}
	return client
}

func countEvByName(ctx context.Context, c *quark.Client, name string) (int64, error) {
	return quark.For[ev](ctx, c).Where("name", "=", name).Count()
}

// savepointTruncation: 5-level nested tx.Tx. Level 4 returns an error (rolled
// back by its savepoint, carrying level 5 with it); level 3 swallows that
// error and commits. Both the data rows and the OnCommit hooks of levels 4-5
// must be dropped; levels 1-3 must survive, OnCommit firing in FIFO order.
func savepointTruncation(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)
	m := &marker{}
	name := func(l int) string { return fmt.Sprintf("spL%d-%s", l, eng) }

	mk := func(tx *quark.Tx, l int) error {
		if err := quark.ForTx[ev](ctx, tx).Create(&ev{Name: name(l), N: l}); err != nil {
			return err
		}
		tx.OnCommit(m.hook(name(l)))
		return nil
	}

	err := client.Tx(ctx, func(tx *quark.Tx) error {
		if err := mk(tx, 1); err != nil {
			return err
		}
		return tx.Tx(ctx, func(tx *quark.Tx) error {
			if err := mk(tx, 2); err != nil {
				return err
			}
			return tx.Tx(ctx, func(tx *quark.Tx) error {
				if err := mk(tx, 3); err != nil {
					return err
				}
				// Level 4 rolls back (taking level 5 with it). Swallow its
				// error so level 3 commits.
				_ = tx.Tx(ctx, func(tx *quark.Tx) error {
					if err := mk(tx, 4); err != nil {
						return err
					}
					_ = tx.Tx(ctx, func(tx *quark.Tx) error {
						return mk(tx, 5)
					})
					return errors.New("induced rollback at level 4")
				})
				return nil
			})
		})
	})
	if err != nil {
		r.fail("Savepoint/Tx", reporter.SeverityP1, "outer tx: %v", err)
		return
	}

	// Data: levels 1-3 persisted, 4-5 rolled back.
	for l := 1; l <= 3; l++ {
		if n, err := countEvByName(ctx, client, name(l)); err != nil {
			r.fail("Savepoint/count", reporter.SeverityP1, "count L%d: %v", l, err)
		} else if n != 1 {
			r.fail("Savepoint/data", reporter.SeverityP0, "level %d row count=%d, want 1 (committed level lost)", l, n)
		}
	}
	for l := 4; l <= 5; l++ {
		if n, err := countEvByName(ctx, client, name(l)); err != nil {
			r.fail("Savepoint/count", reporter.SeverityP1, "count L%d: %v", l, err)
		} else if n != 0 {
			r.fail("Savepoint/data", reporter.SeverityP0, "level %d row count=%d, want 0 (rolled-back savepoint persisted)", l, n)
		}
	}

	// Hooks: OnCommit fired for 1-3 in FIFO order, not for 4-5.
	got := m.list()
	want := []string{name(1), name(2), name(3)}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		r.fail("Savepoint/hooks", reporter.SeverityP1,
			"OnCommit fired %v, want %v (savepoint hook truncation / FIFO order)", got, want)
	}
}

// onCommitOnRollback: the right queue fires by outcome, and an erroring
// OnCommit callback is logged but does not fail the commit.
func onCommitOnRollback(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)
	m := &marker{}

	// Commit path.
	if err := client.Tx(ctx, func(tx *quark.Tx) error {
		tx.OnCommit(m.hook("commit/onCommit"))
		tx.OnRollback(m.hook("commit/onRollback"))
		return nil
	}); err != nil {
		r.fail("OnCommit/commitTx", reporter.SeverityP1, "commit tx: %v", err)
	}
	if !m.has("commit/onCommit") || m.has("commit/onRollback") {
		r.fail("OnCommit/commitFires", reporter.SeverityP1,
			"on commit: OnCommit must fire and OnRollback must not (seen=%v)", m.list())
	}

	// Rollback path.
	_ = client.Tx(ctx, func(tx *quark.Tx) error {
		tx.OnCommit(m.hook("rb/onCommit"))
		tx.OnRollback(m.hook("rb/onRollback"))
		return errors.New("force rollback")
	})
	if !m.has("rb/onRollback") || m.has("rb/onCommit") {
		r.fail("OnRollback/rbFires", reporter.SeverityP1,
			"on rollback: OnRollback must fire and OnCommit must not (seen=%v)", m.list())
	}

	// An erroring OnCommit callback is logged, not fatal — the data persists.
	err := client.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[ev](ctx, tx).Create(&ev{Name: "oc-err-" + eng}); err != nil {
			return err
		}
		tx.OnCommit(func(context.Context) error { return errors.New("boom in OnCommit") })
		return nil
	})
	if err != nil {
		r.fail("OnCommit/errNotFatal", reporter.SeverityP1, "an OnCommit error must not fail the commit, got: %v", err)
	}
	if n, _ := countEvByName(ctx, client, "oc-err-"+eng); n != 1 {
		r.fail("OnCommit/errPersists", reporter.SeverityP1, "data not persisted after OnCommit error: count=%d, want 1", n)
	}
}

// captureBus records events and can be told to fail Publish.
type captureBus struct {
	mu     sync.Mutex
	events []quark.Event
	fail   atomic.Bool
}

func (b *captureBus) Publish(ctx context.Context, e quark.Event) error {
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
	if b.fail.Load() {
		return errors.New("bus down")
	}
	return nil
}
func (b *captureBus) kinds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.events))
	for i, e := range b.events {
		out[i] = e.Kind()
	}
	return out
}

// eventBus: a Create publishes a "created" event; a bus error does not roll
// back the write (at-least-once, no outbox — ADR-0013).
func eventBus(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)
	bus := &captureBus{}
	client.UseEventBus(bus)

	if err := quark.For[ev](ctx, client).Create(&ev{Name: "evt-" + eng}); err != nil {
		r.fail("EventBus/Create", reporter.SeverityP1, "create: %v", err)
		return
	}
	found := false
	for _, k := range bus.kinds() {
		if k == "created" {
			found = true
		}
	}
	if !found {
		r.fail("EventBus/publish", reporter.SeverityP1, "no \"created\" event published (kinds=%v)", bus.kinds())
	}

	// A bus error must NOT roll back the write (at-least-once, no outbox —
	// ADR-0013). The contract surfaces the failure as ErrEventEmitFailed so the
	// caller knows the event was lost, but the data stays committed — it is not
	// silently swallowed.
	bus.fail.Store(true)
	err := quark.For[ev](ctx, client).Create(&ev{Name: "evt2-" + eng})
	if !errors.Is(err, quark.ErrEventEmitFailed) {
		r.fail("EventBus/errSurfaced", reporter.SeverityP1,
			"a bus error should surface as ErrEventEmitFailed, got: %v", err)
	}
	if n, _ := countEvByName(ctx, client, "evt2-"+eng); n != 1 {
		r.fail("EventBus/errPersists", reporter.SeverityP0,
			"data not persisted despite bus error (count=%d, want 1) — a bus error must not roll back the write", n)
	}
}

// auditLog: N single Creates produce N audit rows (delta) each carrying a
// non-empty diff. Scaled down from the spec's 100k (logged).
func auditLog(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)
	if err := client.EnableAuditLog(ctx, quark.AuditConfig{}); err != nil {
		r.fail("Audit/enable", reporter.SeverityP1, "enable audit: %v", err)
		return
	}

	const n = 200
	t.Logf("AuditLog: %d single writes (spec target 100k is F14 soak-tier)", n)
	before := auditCount(t, ctx, client)
	for i := 0; i < n; i++ {
		if err := quark.For[ev](ctx, client).Create(&ev{Name: fmt.Sprintf("aud-%s-%d", eng, i), N: i}); err != nil {
			r.fail("Audit/Create", reporter.SeverityP1, "create %d: %v", i, err)
			return
		}
	}
	after := auditCount(t, ctx, client)
	if after-before != n {
		r.fail("Audit/count", reporter.SeverityP1, "audit rows delta=%d, want %d (one row per write)", after-before, n)
	}

	// At least one diff must be a non-empty JSON object. Unquoted identifiers
	// resolve on every engine (Oracle folds them to upper-case, matching the
	// migrator's quoted-upper-case DDL).
	var diff string
	if err := client.Raw().QueryRowContext(ctx,
		`SELECT diff FROM quark_audit WHERE operation = 'created' ORDER BY id DESC`).Scan(&diff); err != nil {
		r.fail("Audit/diffRead", reporter.SeverityP1, "read diff: %v", err)
		return
	}
	if strings.TrimSpace(diff) == "" || diff == "{}" || diff == "null" {
		r.fail("Audit/diffValid", reporter.SeverityP1, "audit diff is empty/null (%q), want the inserted row", diff)
	}
}

// auditAtomicity: audit is inline-in-tx — a rolled-back tx leaves neither the
// data row nor its audit row. (Stands in for the spec's kill-9.)
func auditAtomicity(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)
	if err := client.EnableAuditLog(ctx, quark.AuditConfig{}); err != nil {
		r.fail("Atomicity/enable", reporter.SeverityP1, "enable audit: %v", err)
		return
	}
	before := auditCount(t, ctx, client)

	_ = client.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[ev](ctx, tx).Create(&ev{Name: "atomic-" + eng}); err != nil {
			return err
		}
		return errors.New("rollback after write+audit")
	})

	if n, _ := countEvByName(ctx, client, "atomic-"+eng); n != 0 {
		r.fail("Atomicity/data", reporter.SeverityP0, "rolled-back data row persisted (count=%d, want 0)", n)
	}
	if after := auditCount(t, ctx, client); after != before {
		r.fail("Atomicity/audit", reporter.SeverityP0,
			"rolled-back tx left an audit row (delta=%d, want 0) — audit not atomic with the write", after-before)
	}
}

// findHooks: BeforeFind can abort a read; AfterFind error propagates.
// BeforeFind cannot mutate the query (it only receives ctx) — documented gap.
func findHooks(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)

	if err := quark.For[findGuard](ctx, client).Create(&findGuard{Name: "fg-" + eng}); err != nil {
		r.fail("Find/seed", reporter.SeverityP1, "seed findGuard: %v", err)
		return
	}
	if err := quark.For[afterGuard](ctx, client).Create(&afterGuard{Name: "ag-" + eng}); err != nil {
		r.fail("Find/seed", reporter.SeverityP1, "seed afterGuard: %v", err)
		return
	}

	// BeforeFind aborts the read.
	blockFind.Store(true)
	_, err := quark.For[findGuard](ctx, client).Limit(10).List()
	blockFind.Store(false)
	if !errors.Is(err, errFindBlocked) {
		r.fail("Find/beforeAbort", reporter.SeverityP1, "BeforeFind error did not abort the read (got %v)", err)
	}
	// With the hook quiet, the read works.
	if _, err := quark.For[findGuard](ctx, client).Limit(10).List(); err != nil {
		r.fail("Find/beforeClear", reporter.SeverityP1, "read failed with BeforeFind quiet: %v", err)
	}

	// AfterFind error propagates to the caller.
	failAfterFind.Store(true)
	_, err = quark.For[afterGuard](ctx, client).Limit(10).List()
	failAfterFind.Store(false)
	if !errors.Is(err, errAfterFindHook) {
		r.fail("Find/afterError", reporter.SeverityP1, "AfterFind error did not propagate (got %v)", err)
	}
}

// txFromContext: a lifecycle hook dispatched via ForTx can reach the active
// transaction through TxFromContext.
func txFromContext(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng, reporter.CategoryRegression)
	client := newClient(t, ctx, conn, eng)

	txProbeSawTx.Store(false)
	if err := client.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[txProbe](ctx, tx).Create(&txProbe{Name: "tp-" + eng})
	}); err != nil {
		r.fail("TxFromContext/Create", reporter.SeverityP1, "create in tx: %v", err)
		return
	}
	if !txProbeSawTx.Load() {
		r.fail("TxFromContext/visible", reporter.SeverityP1,
			"a lifecycle hook dispatched via ForTx could not see the active tx via TxFromContext")
	}
}

func auditCount(t *testing.T, ctx context.Context, c *quark.Client) int64 {
	t.Helper()
	var n int64
	if err := c.Raw().QueryRowContext(ctx, "SELECT COUNT(*) FROM quark_audit").Scan(&n); err != nil {
		t.Fatalf("count quark_audit: %v", err)
	}
	return n
}
