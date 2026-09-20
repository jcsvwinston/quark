// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// Probes for the "operacion" family: what an application that already runs on
// Quark in production needs from it — replicas, locking, savepoints, audit,
// soft delete, cache, observability, events, pagination, schema sync, seeding
// and the test kit.
//
// Every probe here drives the PUBLIC surface and then reads one of three
// things: the rows that came back, the statement the builder emitted (through
// e.rec), or the error the API returned. None of them looks for a symbol: the
// gaps this family records — a DELETE that forgets the version column, a batch
// path that writes no audit row, a pagination API that only knows OFFSET — are
// all observable from outside, and a gap that cannot be observed from outside
// is not a gap an application can hit.
//
// Most probes open their own database through e.fresh: nearly all of them need
// a client built with an option (a replica pool, a cache store, a middleware,
// a logger they can read back), and an option is fixed at New. The shared
// client cannot carry them.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"
	"github.com/jcsvwinston/quark/quarktest"
	"github.com/jcsvwinston/quark/seed"

	otelapi "go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// --- models -----------------------------------------------------------------
//
// Table names are prefixed ops_ so this family never shares a table with
// another one on the bench's shared in-memory database.

// opsMarker carries a name that says WHICH database answered a read. Routing
// is invisible in a row both pools hold, so the replica gets a different value
// for the same primary key and the row itself becomes the verdict.
type opsMarker struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (opsMarker) TableName() string { return "ops_marker" }

// opsAccount carries the optimistic-locking column, so the same model serves
// the version probes and the CRUD-shaped ones.
type opsAccount struct {
	ID      int64  `db:"id" pk:"true"`
	Owner   string `db:"owner"`
	Balance int64  `db:"balance"`
	Version int64  `db:"version" quark:"version"`
}

func (opsAccount) TableName() string { return "ops_account" }

// opsDoc has the soft-delete column Quark looks for.
type opsDoc struct {
	ID        int64   `db:"id" pk:"true"`
	Title     string  `db:"title"`
	DeletedAt *string `db:"deleted_at"`
}

func (opsDoc) TableName() string { return "ops_doc" }

// opsDocRenamed is the same model with the deletion timestamp under another
// name — the shape an application with its own column convention has.
type opsDocRenamed struct {
	ID        int64   `db:"id" pk:"true"`
	Title     string  `db:"title"`
	RemovedAt *string `db:"removed_at"`
}

func (opsDocRenamed) TableName() string { return "ops_doc_renamed" }

// opsDocTagged is the same shape again, with the column DECLARED as the
// deletion marker through a struct tag. It answers the question the note used
// to assert without measuring: whether an application can name its own soft
// delete column instead of spelling it deleted_at.
type opsDocTagged struct {
	ID        int64   `db:"id" pk:"true"`
	Title     string  `db:"title"`
	RemovedAt *string `db:"removed_at" quark:"softdelete"`
}

func (opsDocTagged) TableName() string { return "ops_doc_tagged" }

// opsComposite has a composite primary key, which is what the row-level cache
// tag cannot render.
type opsComposite struct {
	TenantID int64  `db:"tenant_id" pk:"true"`
	ItemID   int64  `db:"item_id" pk:"true"`
	Name     string `db:"name"`
}

func (opsComposite) TableName() string { return "ops_composite" }

// --- helpers ----------------------------------------------------------------

// opsDSN builds the DSN of a bench database by name, so a probe can hand a
// replica pool the same database another client is holding open.
func opsDSN(name string) string { return "file:" + name + "?mode=memory&cache=shared" }

// opsSeed migrates a model on a client and inserts one row, the setup nearly
// every probe starts from.
func opsSeed[T any](t *testing.T, c *quark.Client, model any, row *T) {
	t.Helper()
	if err := c.Migrate(context.Background(), model); err != nil {
		t.Fatalf("migrate %T: %v", model, err)
	}
	if err := quark.For[T](context.Background(), c).Create(row); err != nil {
		t.Fatalf("seed %T: %v", row, err)
	}
}

// opsCountSelects counts the SELECT statements recorded since the last reset.
// Several probes are about how many times the database was actually asked —
// a cache hit, a singleflight collapse, a page that costs two round trips.
func opsCountSelects(rec *recorder) int {
	n := 0
	for _, s := range rec.sql() {
		if strings.HasPrefix(strings.TrimSpace(s), "SELECT") {
			n++
		}
	}
	return n
}

// opsLogger returns a logger writing into a buffer the probe can read, for the
// controls whose whole observable output is a log line.
func opsLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// opsBus records the CRUD events an application would receive.
type opsBus struct {
	mu   sync.Mutex
	seen []string
}

func (b *opsBus) Publish(_ context.Context, ev quark.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, ev.Kind()+":"+ev.Table())
	return nil
}

func (b *opsBus) events() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

// opsFailingBus is the broker that is down — the case delivery guarantees are
// for. Whether the row survives a Publish that fails is the difference between
// an outbox and a best-effort emit, and it is only visible from a bus that
// refuses.
type opsFailingBus struct {
	mu    sync.Mutex
	calls int
}

func (b *opsFailingBus) Publish(context.Context, quark.Event) error {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	return errors.New("broker unreachable")
}

func (b *opsFailingBus) attempts() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// opsStore is a CacheStore an application could write: it keeps the bytes and
// records the tags each invalidation carried, which is the only way to see
// what Quark asked the cache to drop.
//
// It stores the tags an entry was written with and drops ONLY the entries
// carrying one of the tags an invalidation names. A store that emptied itself
// on any InvalidateTags would make "the write dropped the cached entry" true
// whatever tags Quark sent — the probe would be measuring the double, not the
// product. A real tagged store behaves like this one, so this is also the
// behaviour Quark's tags have to earn.
type opsStore struct {
	mu     sync.Mutex
	data   map[string][]byte
	tags   map[string][]string // cache key → the tags it was written with
	invals [][]string
}

func newOpsStore() *opsStore {
	return &opsStore{data: map[string][]byte{}, tags: map[string][]string{}}
}

func (s *opsStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *opsStore) Set(_ context.Context, key string, val []byte, _ time.Duration, tags ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = val
	s.tags[key] = append([]string(nil), tags...)
	return nil
}

func (s *opsStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	delete(s.tags, key)
	return nil
}

func (s *opsStore) InvalidateTags(_ context.Context, tags ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invals = append(s.invals, append([]string(nil), tags...))
	drop := make(map[string]bool, len(tags))
	for _, tag := range tags {
		drop[tag] = true
	}
	for key, entryTags := range s.tags {
		for _, tag := range entryTags {
			if drop[tag] {
				delete(s.data, key)
				delete(s.tags, key)
				break
			}
		}
	}
	return nil
}

// clearTags forgets the invalidations recorded so far, so a probe can attribute
// the next ones to one specific write.
func (s *opsStore) clearTags() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invals = nil
}

func (s *opsStore) tagsSeen() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]string(nil), s.invals...)
}

// snapshot / restore let a probe stage the cross-instance case: the peer's
// value disappears from the store and comes back while the loser is waiting,
// which is exactly what a loser sees when another process publishes.
func (s *opsStore) snapshot() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	s.data = map[string][]byte{}
	return out
}

func (s *opsStore) restore(vals map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range vals {
		s.data[k] = v
	}
}

// opsSQLTap records the statement the builder handed to the driver, BEFORE it
// runs. The query observer only fires on a statement that succeeded, so a
// clause aimed at another engine — a FOR UPDATE that SQLite refuses to parse —
// is invisible there. A middleware is a public extension point that sees the
// string on the way down, which is the only place a per-dialect clause can be
// read without that dialect's server.
type opsSQLTap struct {
	quark.BaseMiddleware
	mu   sync.Mutex
	seen []string
}

func (m *opsSQLTap) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		m.mu.Lock()
		m.seen = append(m.seen, sqlStr)
		m.mu.Unlock()
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *opsSQLTap) statements() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.seen...)
}

// opsLockingStore is an opsStore that also implements CacheLocker — the
// optional capability the stampede wrapper looks for. winner decides whether
// this "process" gets recompute rights, which makes both sides of the
// hand-off reproducible without a second process.
type opsLockingStore struct {
	*opsStore
	lmu      sync.Mutex
	keys     []string
	releases int
	winner   bool
}

func (l *opsLockingStore) AcquireLock(_ context.Context, key string, _ time.Duration) (bool, func() error, error) {
	l.lmu.Lock()
	l.keys = append(l.keys, key)
	win := l.winner
	l.lmu.Unlock()
	if !win {
		return false, nil, nil
	}
	return true, func() error {
		l.lmu.Lock()
		l.releases++
		l.lmu.Unlock()
		return nil
	}, nil
}

func (l *opsLockingStore) lockKeys() []string {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	return append([]string(nil), l.keys...)
}

// opsSlowQuery delays every SELECT so concurrent callers really are concurrent
// when they reach the cache wrapper. Without it, SQLite answers before the
// second goroutine arrives and a singleflight measurement would only be
// measuring how fast the machine is.
type opsSlowQuery struct {
	quark.BaseMiddleware
	d time.Duration
}

func (m opsSlowQuery) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		time.Sleep(m.d)
		return next(ctx, exec, sqlStr, args)
	}
}

// opsTableExists / opsColumns read the schema through the public *sql.DB the
// client hands out. The schema probes are about what DDL actually landed, and
// the catalogue is the only witness to that.
func opsTableExists(t *testing.T, c *quark.Client, table string) bool {
	t.Helper()
	var n int
	if err := c.Raw().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
		t.Fatalf("read sqlite catalogue: %v", err)
	}
	return n > 0
}

func opsColumns(t *testing.T, c *quark.Client, table string) []string {
	t.Helper()
	rows, err := c.Raw().QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("read columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, name)
	}
	return out
}

// --- probes -----------------------------------------------------------------

// probeReplicaRouting reads the ROW, not the configuration: two replica
// databases hold a different name for the same primary key, so the value that
// comes back names the pool that served it. Round-robin is measured the same
// way — six reads must show both replicas, because a router that always picks
// slot 0 would pass any check that only asks "did it leave the primary?".
func probeReplicaRouting(t *testing.T, e *env) verdict {
	ctx := e.ctx
	repA, _ := e.fresh(t, "ops_rep_a")
	opsSeed(t, repA, &opsMarker{}, &opsMarker{ID: 1, Name: "replica-a"})
	repB, _ := e.fresh(t, "ops_rep_b")
	opsSeed(t, repB, &opsMarker{}, &opsMarker{ID: 1, Name: "replica-b"})

	primary, _ := e.fresh(t, "ops_rep_primary",
		quark.WithReplicas(opsDSN("ops_rep_a"), opsDSN("ops_rep_b")),
		quark.WithReplicaStrategy(quark.ReplicaRoundRobin),
	)
	opsSeed(t, primary, &opsMarker{}, &opsMarker{ID: 1, Name: "primary"})

	served := map[string]int{}
	for i := 0; i < 6; i++ {
		rows, err := quark.For[opsMarker](ctx, primary).Where("id", "=", 1).List()
		if err != nil {
			t.Fatalf("routed read: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("routed read returned %d rows, want 1", len(rows))
		}
		served[rows[0].Name]++
	}
	if served["primary"] > 0 || served["replica-a"]+served["replica-b"] != 6 {
		// Reads never left the primary: the option is accepted and does
		// nothing an application can observe.
		return absent
	}
	if served["replica-a"] == 0 || served["replica-b"] == 0 {
		return partial // routed, but one pool takes every read
	}

	// Read-your-writes: Sticky must pin the read to the primary, which the
	// marker makes visible without any timing assumption.
	sticky, err := quark.For[opsMarker](quark.Sticky(ctx), primary).Where("id", "=", 1).List()
	if err != nil {
		t.Fatalf("sticky read: %v", err)
	}
	if len(sticky) != 1 || sticky[0].Name != "primary" {
		return partial
	}
	return present
}

// probeReplicaFailureHandling measures the EDGE of the failover, which is
// where the control actually lives. A replica that answers with an error that
// is not a transient connection failure is not retried on the primary, and it
// is not taken out of rotation either — the second read fails identically. It
// also measures the operational blind spot: Raw(), the only pool an
// application can reach, is the primary, so nothing exposes replica health.
//
// What it does NOT measure is the other side of the rule — that a TRANSIENT
// connection error DOES fall back to the primary. That needs a replica which
// can be dropped mid-flight, which is a live engine's job. So the title claims
// only the edge this probe walked, and the note says where the rest is proved.
func probeReplicaFailureHandling(t *testing.T, e *env) verdict {
	ctx := e.ctx

	// A replica database that exists (New pings it, so it must open) but does
	// not have the table. Reads routed there fail with a logic error.
	_, _ = e.fresh(t, "ops_rep_broken")
	primary, _ := e.fresh(t, "ops_rep_broken_primary", quark.WithReplicas(opsDSN("ops_rep_broken")))
	opsSeed(t, primary, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Version: 1})

	first, errFirst := quark.For[opsAccount](ctx, primary).List()
	second, errSecond := quark.For[opsAccount](ctx, primary).List()
	if errFirst == nil && errSecond == nil && len(first) == 1 && len(second) == 1 {
		// Every replica failure falls back to the primary: nothing missing
		// that this probe can see.
		return present
	}
	if errFirst == nil || errSecond == nil {
		// One of the two recovered: something took the replica out of
		// rotation after the first failure. The title records that NOTHING
		// does, so this is a gain and has to read as a different verdict —
		// repeating the recorded one would hide a cooldown appearing.
		return present
	}

	// The escape hatch still works, which is what makes the gap survivable.
	sticky, err := quark.For[opsAccount](quark.Sticky(ctx), primary).List()
	if err != nil || len(sticky) != 1 {
		t.Fatalf("sticky read through a broken replica: rows=%d err=%v", len(sticky), err)
	}

	// The only pool handle the API hands out is the primary's: ask it for the
	// marker row and it answers with the primary's copy, never a replica's.
	repHolder, _ := e.fresh(t, "ops_rep_health")
	opsSeed(t, repHolder, &opsMarker{}, &opsMarker{ID: 1, Name: "replica-a"})
	withReplica, _ := e.fresh(t, "ops_rep_health_primary", quark.WithReplicas(opsDSN("ops_rep_health")))
	opsSeed(t, withReplica, &opsMarker{}, &opsMarker{ID: 1, Name: "primary"})
	var raw string
	if err := withReplica.Raw().QueryRowContext(ctx, `SELECT name FROM ops_marker WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("query through Raw(): %v", err)
	}
	if raw != "primary" {
		t.Fatalf("Raw() served %q; the probe assumed it is the primary pool", raw)
	}
	return partial
}

// probePessimisticLocking reads the CLAUSE, because that is what the control
// is: every engine returns the same rows, and only one of them returns them
// locked. The bench runs on SQLite, so the per-dialect fragments come from the
// dialects themselves — public constructors an application uses to build a
// client — and the end-to-end path is measured on the engine that is here.
func probePessimisticLocking(t *testing.T, e *env) verdict {
	ctx := e.ctx

	type want struct {
		dialect quark.Dialect
		opts    quark.LockOptions
		hint    string
		suffix  string
		err     bool
	}
	cases := []want{
		{quark.PostgreSQL(), quark.LockOptions{Mode: quark.LockForUpdate}, "", " FOR UPDATE", false},
		{quark.PostgreSQL(), quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, "", " FOR UPDATE SKIP LOCKED", false},
		{quark.PostgreSQL(), quark.LockOptions{Mode: quark.LockForUpdate, NoWait: true}, "", " FOR UPDATE NOWAIT", false},
		{quark.PostgreSQL(), quark.LockOptions{Mode: quark.LockForShare}, "", " FOR SHARE", false},
		{quark.MySQL(), quark.LockOptions{Mode: quark.LockForShare}, "", " FOR SHARE", false},
		// MariaDB has no FOR SHARE; emitting MySQL-8 syntax there is a parse
		// error at the server, so the dialect writes the older clause and
		// refuses the modifiers it cannot carry.
		{quark.MariaDB(), quark.LockOptions{Mode: quark.LockForShare}, "", " LOCK IN SHARE MODE", false},
		{quark.MariaDB(), quark.LockOptions{Mode: quark.LockForShare, SkipLocked: true}, "", "", true},
		// MSSQL locks through a table hint instead of a trailing clause.
		{quark.MSSQL(), quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, " WITH (UPDLOCK, ROWLOCK, READPAST)", "", false},
		{quark.MSSQL(), quark.LockOptions{Mode: quark.LockForUpdate, NoWait: true}, "", "", true},
		{quark.Oracle(), quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, "", " FOR UPDATE SKIP LOCKED", false},
		{quark.Oracle(), quark.LockOptions{Mode: quark.LockForShare}, "", "", true},
		{quark.SQLite(), quark.LockOptions{Mode: quark.LockForUpdate}, "", "", true},
		// The zero value must stay silent: a lock nobody asked for is a
		// deadlock nobody can explain.
		{quark.PostgreSQL(), quark.LockOptions{}, "", "", false},
	}
	for _, c := range cases {
		hint, suffix, err := c.dialect.LockSuffix(c.opts)
		if c.err {
			if err == nil {
				return partial // the dialect emits something for a lock it cannot take
			}
			if !errors.Is(err, quark.ErrUnsupportedFeature) {
				return partial
			}
			continue
		}
		if err != nil || hint != c.hint || suffix != c.suffix {
			return partial
		}
	}

	// A fragment a dialect RETURNS is not a fragment the builder WRITES. The
	// two are separate pieces of code, and only the second one locks a row —
	// a SELECT that reaches the server without its suffix returns exactly the
	// same rows, unlocked, with nothing to see.
	//
	// So the builder is driven once per dialect with WithDialect over the
	// engine the bench has, and the statement is read from a middleware. The
	// engine then refuses to parse it, which is fine: the clause has already
	// been written by the time the driver sees it, and the middleware is the
	// only place that string is readable before it fails.
	emitted := []struct {
		dialect quark.Dialect
		build   func(q *quark.Query[opsAccount]) *quark.Query[opsAccount]
		want    string
	}{
		{quark.PostgreSQL(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForUpdate() }, " FOR UPDATE"},
		{quark.PostgreSQL(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForUpdate().SkipLocked() }, " FOR UPDATE SKIP LOCKED"},
		{quark.PostgreSQL(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForUpdate().NoWait() }, " FOR UPDATE NOWAIT"},
		{quark.PostgreSQL(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForShare() }, " FOR SHARE"},
		{quark.MySQL(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForUpdate().SkipLocked() }, " FOR UPDATE SKIP LOCKED"},
		{quark.MariaDB(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForShare() }, " LOCK IN SHARE MODE"},
		// MSSQL writes a table hint instead of a trailing clause, so the
		// string lands in the middle of the statement, not at its end.
		{quark.MSSQL(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForUpdate().SkipLocked() }, "WITH (UPDLOCK, ROWLOCK, READPAST)"},
		{quark.Oracle(), func(q *quark.Query[opsAccount]) *quark.Query[opsAccount] { return q.ForUpdate().SkipLocked() }, " FOR UPDATE SKIP LOCKED"},
	}
	for i, em := range emitted {
		tap := &opsSQLTap{}
		lockClient, _ := e.fresh(t, fmt.Sprintf("ops_lock_%s_%d", em.dialect.Name(), i),
			quark.WithDialect(em.dialect), quark.WithMiddleware(tap))
		// The error is the engine rejecting another dialect's grammar; the
		// statement the tap already holds is what the probe came for.
		_, _ = em.build(quark.For[opsAccount](ctx, lockClient).Where("id", "=", 900)).List()
		stmts := tap.statements()
		if len(stmts) == 0 {
			return partial // the builder refused a lock the dialect supports
		}
		if !strings.Contains(stmts[len(stmts)-1], em.want) {
			// The dialect knows the fragment and the builder drops it: the
			// application gets unlocked rows and no error.
			return partial
		}
	}

	// End to end on the engine the bench has: SQLite has no row-level lock,
	// and the query must say so rather than run unlocked. The shared client is
	// enough here — the refusal happens while the statement is being built, so
	// the probe needs the table to exist and no row in it.
	if err := e.c.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate on the shared client: %v", err)
	}
	_, err := quark.For[opsAccount](ctx, e.c).Where("id", "=", 900).ForUpdate().Limit(1).List()
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		// Silently returning unlocked rows would be worse than refusing.
		return partial
	}
	return present
}

// probeOptimisticLocking walks every write path a stale entity can take, and
// asks each one the same question: does the version this caller loaded decide
// whether the write lands?
//
// Both halves of the title are exercised. Update is refused with
// ErrStaleEntity; Tracked.Save — the other guarded path, and the one an
// application reaches for when it wants a partial UPDATE — is driven with a
// handle whose row moved underneath it and must be refused the same way. A
// probe that only drove Update would pass unchanged if that second guard were
// deleted tomorrow.
//
// The remaining paths are read as STATEMENTS through the recorder, because a
// row count cannot tell "matched by pk" from "matched by pk and version": the
// two return the same 1 when nobody raced, and differ only in the case the
// control exists for.
func probeOptimisticLocking(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, rec := e.fresh(t, "ops_optimistic")
	opsSeed(t, c, &opsAccount{}, &opsAccount{ID: 1, Owner: "start", Balance: 100, Version: 1})

	stale, err := quark.For[opsAccount](ctx, c).Find(1)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	winner := stale
	winner.Balance = 200
	if _, err := quark.For[opsAccount](ctx, c).Update(&winner); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if winner.Version != stale.Version+1 {
		// Not a verdict: a version that does not move makes every leg below
		// meaningless, so the probe cannot report on the control at all.
		t.Fatalf("the version did not move after a successful Update (%d → %d); "+
			"nothing downstream can be measured", stale.Version, winner.Version)
	}
	loser := stale
	loser.Balance = 999
	if _, err := quark.For[opsAccount](ctx, c).Update(&loser); !errors.Is(err, quark.ErrStaleEntity) {
		return absent // the guarded path the title names first is not guarded
	}

	// Tracked.Save, the second path the title claims is guarded: load a
	// handle, let another writer move the row, and save.
	handle, err := quark.For[opsAccount](ctx, c).Track().Find(1)
	if err != nil {
		t.Fatalf("tracked load: %v", err)
	}
	concurrent, err := quark.For[opsAccount](ctx, c).Find(1)
	if err != nil {
		t.Fatalf("concurrent load: %v", err)
	}
	concurrent.Balance = 300
	if _, err := quark.For[opsAccount](ctx, c).Update(&concurrent); err != nil {
		t.Fatalf("concurrent writer: %v", err)
	}
	handle.Entity.Balance = 400
	if _, err := handle.Save(ctx); !errors.Is(err, quark.ErrStaleEntity) {
		return absent // the tracking path writes over a concurrent change in silence
	}

	// UpdateMap: a WHERE the caller wrote, and no version in it.
	rec.reset()
	if _, err := quark.For[opsAccount](ctx, c).Where("id", "=", 1).
		UpdateMap(map[string]any{"balance": 888}); err != nil {
		t.Fatalf("UpdateMap: %v", err)
	}
	updateMapGuarded := strings.Contains(strings.ToLower(rec.last()), "version")

	// UpdateBatch: the predicate IS there, and nothing reports the miss. A
	// stale entity in a batch is dropped on the floor and the call returns
	// nil, which is the shape of the gap on this path — not the absence of
	// the predicate.
	fresh, err := quark.For[opsAccount](ctx, c).Find(1)
	if err != nil {
		t.Fatalf("reload before the batch: %v", err)
	}
	staleForBatch := stale
	staleForBatch.Balance = 777
	rec.reset()
	batchErr := quark.For[opsAccount](ctx, c).UpdateBatch([]*opsAccount{&staleForBatch})
	updateBatchGuarded := strings.Contains(strings.ToLower(rec.last()), "version")
	afterBatch, err := quark.For[opsAccount](ctx, c).Find(1)
	if err != nil {
		t.Fatalf("reload after the batch: %v", err)
	}
	batchWroteNothing := afterBatch.Balance == fresh.Balance
	// Unconditional, because the write itself is the failure whether or not a
	// predicate was emitted: the winner already moved the row, so a batch that
	// lands the stale copy is a lost update, not a grade of this verdict. The
	// old form only fired when the predicate was there, which let the worse
	// world — no predicate AND the stale row written — fall through to the
	// recorded `partial` below.
	if !batchWroteNothing {
		t.Fatalf("UpdateBatch wrote the stale entity (balance %d): that is a lost update, "+
			"not a grade of the verdict", afterBatch.Balance)
	}
	updateBatchReports := batchErr != nil

	// DeleteBatch, on a row of its own so the stale-delete leg below still
	// has one to lose.
	if err := quark.For[opsAccount](ctx, c).Create(&opsAccount{ID: 2, Owner: "batch", Version: 1}); err != nil {
		t.Fatalf("seed the batch-delete row: %v", err)
	}
	rec.reset()
	if _, err := quark.For[opsAccount](ctx, c).DeleteBatch([]any{int64(2)}); err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	deleteBatchGuarded := strings.Contains(strings.ToLower(rec.last()), "version")

	// The same stale entity, deleted instead of updated.
	rec.reset()
	rows, err := quark.For[opsAccount](ctx, c).Delete(&stale)
	deleteSQL := rec.last()
	deleteGuarded := errors.Is(err, quark.ErrStaleEntity) || rows == 0
	if !deleteGuarded {
		if strings.Contains(strings.ToLower(deleteSQL), "version") {
			t.Fatalf("the DELETE carried a version predicate yet removed the row: %s", deleteSQL)
		}
		// Measured: the stale delete removed the winner's row, and the
		// statement never mentioned the version column.
		var left int64
		if err := c.Raw().QueryRowContext(ctx, `SELECT COUNT(*) FROM ops_account WHERE id = 1`).Scan(&left); err != nil {
			t.Fatalf("count after stale delete: %v", err)
		}
		if left != 0 {
			t.Fatalf("stale delete reported %d rows but left %d behind", rows, left)
		}
	}

	// The verdict is the conjunction of the facts the title and the note
	// assert. Every other split has to read differently, or a path that gains
	// the predicate — or loses it — leaves the bench green.
	// updateBatchGuarded and updateBatchReports are two different facts about
	// UpdateBatch — the predicate went out, and the miss was never reported —
	// and only their conjunction is the gap the note publishes. Left out of
	// the switch, a UpdateBatch that lost its predicate entirely would read as
	// the same `partial` as the one that carries it and stays quiet.
	switch {
	case deleteGuarded && updateMapGuarded && deleteBatchGuarded && updateBatchGuarded && updateBatchReports:
		return present
	case !deleteGuarded && !updateMapGuarded && !deleteBatchGuarded && updateBatchGuarded && !updateBatchReports:
		return partial
	default:
		t.Fatalf("the version predicate moved on some paths and not others: "+
			"delete=%v updateMap=%v deleteBatch=%v updateBatchGuarded=%v updateBatchReports=%v — "+
			"update the recorded verdict and the note in the same change",
			deleteGuarded, updateMapGuarded, deleteBatchGuarded, updateBatchGuarded, updateBatchReports)
		return absent // unreachable; t.Fatalf stops the probe
	}
}

// probeSavepoints measures the unwind, not the keyword. Work done after a
// savepoint disappears on RollbackTo while the work before it survives the
// commit, and the post-commit callbacks registered after the savepoint go with
// it — a callback that fires for work that was rolled back is the failure this
// control exists to prevent.
func probeSavepoints(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, _ := e.fresh(t, "ops_savepoint")
	if err := c.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var fired []string
	err := c.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[opsAccount](ctx, tx).Create(&opsAccount{ID: 1, Owner: "kept", Version: 1}); err != nil {
			return err
		}
		tx.OnCommit(func(context.Context) error { fired = append(fired, "before"); return nil })
		if err := tx.Savepoint("sp1"); err != nil {
			return err
		}
		if err := quark.ForTx[opsAccount](ctx, tx).Create(&opsAccount{ID: 2, Owner: "undone", Version: 1}); err != nil {
			return err
		}
		tx.OnCommit(func(context.Context) error { fired = append(fired, "after"); return nil })
		return tx.RollbackTo("sp1")
	})
	if err != nil {
		t.Fatalf("transaction with savepoint: %v", err)
	}
	rows, err := quark.For[opsAccount](ctx, c).List()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(rows) != 1 || rows[0].Owner != "kept" {
		return absent // the savepoint did not bound the rollback
	}
	if len(fired) != 1 || fired[0] != "before" {
		return partial // the rows unwound but the callbacks did not
	}

	// A nested transaction is the same mechanism with a name the caller does
	// not have to invent: its failure must undo only its own work.
	err = c.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[opsAccount](ctx, tx).Create(&opsAccount{ID: 3, Owner: "outer", Version: 1}); err != nil {
			return err
		}
		inner := tx.Tx(ctx, func(inner *quark.Tx) error {
			if err := quark.ForTx[opsAccount](ctx, inner).Create(&opsAccount{ID: 4, Owner: "inner", Version: 1}); err != nil {
				return err
			}
			return errors.New("inner work failed")
		})
		if inner == nil {
			t.Fatalf("nested transaction swallowed its error")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer transaction: %v", err)
	}
	after, err := quark.For[opsAccount](ctx, c).List()
	if err != nil {
		t.Fatalf("read back after nesting: %v", err)
	}
	owners := map[string]bool{}
	for _, r := range after {
		owners[r.Owner] = true
	}
	if !owners["outer"] || owners["inner"] {
		return partial
	}
	return present
}

// probeAuditLog measures the audit trail the way an auditor would: by reading
// quark_audit. The scope gap is measured the same way — a batch insert writes
// the rows and no audit line — and so is the missing index, by asking the
// catalogue what indexes the table it just created actually has.
func probeAuditLog(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, _ := e.fresh(t, "ops_audit")
	if err := c.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := c.EnableAuditLog(ctx, quark.AuditConfig{}); err != nil {
		t.Fatalf("enable audit log: %v", err)
	}
	auditCount := func(where string, args ...any) int64 {
		var n int64
		q := `SELECT COUNT(*) FROM quark_audit`
		if where != "" {
			q += " WHERE " + where
		}
		if err := c.Raw().QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("read quark_audit: %v", err)
		}
		return n
	}

	a := &opsAccount{ID: 1, Owner: "a", Version: 1}
	if err := quark.For[opsAccount](ctx, c).Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	a.Balance = 10
	if _, err := quark.For[opsAccount](ctx, c).Update(a); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := quark.For[opsAccount](ctx, c).Delete(a); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := auditCount(`pk = '1'`); got != 3 {
		return absent // single-row CRUD is not audited at all
	}

	// Atomicity: a rolled-back write must leave no trail, or the log claims a
	// change that never happened.
	rollbackErr := errors.New("rolled back on purpose")
	if err := c.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[opsAccount](ctx, tx).Create(&opsAccount{ID: 77, Owner: "ghost", Version: 1}); err != nil {
			return err
		}
		return rollbackErr
	}); !errors.Is(err, rollbackErr) {
		t.Fatalf("expected the rollback error back, got %v", err)
	}
	if got := auditCount(`pk = '77'`); got != 0 {
		t.Fatalf("a rolled-back write left %d audit rows", got)
	}

	// Scope: the batch paths write rows and no audit line.
	before := auditCount("")
	if err := quark.For[opsAccount](ctx, c).CreateBatch([]*opsAccount{
		{ID: 10, Owner: "b", Version: 1},
		{ID: 11, Owner: "c", Version: 1},
	}); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	batchAudited := auditCount("") - before

	// The table the log grows into has no index to read it by.
	idx, err := c.Raw().QueryContext(ctx, `PRAGMA index_list("quark_audit")`)
	if err != nil {
		t.Fatalf("read indexes of quark_audit: %v", err)
	}
	indexes := 0
	for idx.Next() {
		indexes++
	}
	_ = idx.Close()

	// Retention: the audit surface is read back from the API itself. The note
	// claims there is no call that shrinks the table, and "I did not find
	// one" is not a measurement — the method set of the type an application
	// holds is. Anything that trims the log would be a method on *Client or a
	// knob on AuditConfig, and both are readable from outside.
	auditMethods := map[string]bool{}
	clientType := reflect.TypeOf(&quark.Client{})
	for i := 0; i < clientType.NumMethod(); i++ {
		name := clientType.Method(i).Name
		if strings.Contains(strings.ToLower(name), "audit") {
			auditMethods[name] = true
		}
	}
	retention := false
	for name := range auditMethods {
		if name != "EnableAuditLog" && name != "DisableAuditLog" {
			retention = true
		}
	}
	cfgType := reflect.TypeOf(quark.AuditConfig{})
	for i := 0; i < cfgType.NumField(); i++ {
		lower := strings.ToLower(cfgType.Field(i).Name)
		if strings.Contains(lower, "retain") || strings.Contains(lower, "retention") ||
			strings.Contains(lower, "purge") || strings.Contains(lower, "prune") ||
			strings.Contains(lower, "ttl") || strings.Contains(lower, "maxage") {
			retention = true
		}
	}
	if !auditMethods["EnableAuditLog"] || !auditMethods["DisableAuditLog"] {
		t.Fatalf("the audit surface no longer has Enable/DisableAuditLog: %v", auditMethods)
	}

	// Three independent facts, three independent ways this control can gain
	// ground. Folding them into one `partial` would mean a batch path that
	// starts auditing, or an index that appears, changes nothing the bench
	// reports — which is the failure this family exists to avoid.
	switch {
	case batchAudited == 2 && indexes > 0 && retention:
		return present
	case batchAudited == 0 && indexes == 0 && !retention:
		return partial
	default:
		t.Fatalf("the audit gaps moved apart: batchAudited=%d indexes=%d retention=%v — "+
			"update the recorded verdict and the note in the same change",
			batchAudited, indexes, retention)
		return absent // unreachable; t.Fatalf stops the probe
	}
}

// probeSoftDelete measures the three scopes against the rows, and the column
// name against the STATEMENT: a model whose deletion timestamp is not called
// deleted_at gets a physical DELETE, which returns the same "1 row affected"
// as a soft delete and destroys the row. It also measures the batch path,
// where the same query object deletes for real.
func probeSoftDelete(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, rec := e.fresh(t, "ops_soft")
	if err := c.Migrate(ctx, &opsDoc{}, &opsDocRenamed{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	doc := &opsDoc{ID: 1, Title: "kept"}
	if err := quark.For[opsDoc](ctx, c).Create(doc); err != nil {
		t.Fatalf("create: %v", err)
	}
	rec.reset()
	if _, err := quark.For[opsDoc](ctx, c).Delete(doc); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.HasPrefix(rec.last(), "UPDATE") {
		return absent // the delete was physical; there is no soft delete
	}
	live, _ := quark.For[opsDoc](ctx, c).List()
	trashed, _ := quark.For[opsDoc](ctx, c).OnlyTrashed().List()
	all, _ := quark.For[opsDoc](ctx, c).Unscoped().List()
	// The scopes and Restore are the capability the title names, not a grade
	// of it: a soft delete whose scopes disagree hides rows from the default
	// read and hands them back through another, which is worse than no soft
	// delete at all. Reading that as `partial` would be the recorded verdict.
	if len(live) != 0 || len(trashed) != 1 || len(all) != 1 {
		t.Fatalf("the scopes disagree on one soft-deleted row: default=%d OnlyTrashed=%d Unscoped=%d",
			len(live), len(trashed), len(all))
	}
	if n, err := quark.For[opsDoc](ctx, c).Restore(doc); err != nil || n != 1 {
		t.Fatalf("Restore returned rows=%d err=%v for one trashed row", n, err)
	}
	if back, _ := quark.For[opsDoc](ctx, c).List(); len(back) != 1 {
		t.Fatalf("the restored row is not back in the default scope")
	}

	// Another column name: the same call on a model that spells the column
	// removed_at emits a DELETE and the row is gone for good.
	other := &opsDocRenamed{ID: 1, Title: "gone"}
	if err := quark.For[opsDocRenamed](ctx, c).Create(other); err != nil {
		t.Fatalf("create renamed: %v", err)
	}
	rec.reset()
	if _, err := quark.For[opsDocRenamed](ctx, c).Delete(other); err != nil {
		t.Fatalf("delete renamed: %v", err)
	}
	renamedIsSoft := strings.HasPrefix(rec.last(), "UPDATE")
	remaining, _ := quark.For[opsDocRenamed](ctx, c).Unscoped().List()

	// And the same column DECLARED as the deletion marker through a tag. The
	// note used to assert that no tag names another column; this is that
	// assertion turned into a measurement, on the spelling an application
	// would reach for first. The tag vocabulary is closed, so the model is
	// refused before it ever reaches a statement — which is a firmer answer
	// than a DELETE would have been.
	// Two flags, not one: refusing the tag at migration time and accepting it
	// and then ignoring it are different answers to the same question, and
	// only the refusal is the one the note publishes. With `taggedIsSoft`
	// alone, a tag that migrated and then deleted physically would read as the
	// recorded `partial` while an application that believed it had declared
	// its deletion column silently lost rows.
	taggedIsSoft := false
	taggedRefused := false
	taggedErr := c.Migrate(ctx, &opsDocTagged{})
	switch {
	case taggedErr == nil:
		tagged := &opsDocTagged{ID: 1, Title: "tagged"}
		if err := quark.For[opsDocTagged](ctx, c).Create(tagged); err != nil {
			t.Fatalf("create tagged: %v", err)
		}
		rec.reset()
		if _, err := quark.For[opsDocTagged](ctx, c).Delete(tagged); err != nil {
			t.Fatalf("delete tagged: %v", err)
		}
		taggedIsSoft = strings.HasPrefix(rec.last(), "UPDATE")
	case errors.Is(taggedErr, quark.ErrInvalidTag):
		// The token is not in the vocabulary: there is no tag to name another
		// deletion column, and the model does not even migrate.
		taggedRefused = true
	default:
		t.Fatalf("the tagged model failed for a reason the probe cannot read as an answer: %v", taggedErr)
	}

	// Who deleted: the soft delete writes a timestamp into the model's own
	// table, so the only place an author could land is a column Quark adds.
	// The catalogue says which columns the table ended up with.
	authorColumn := false
	for _, col := range opsColumns(t, c, "ops_doc") {
		lower := strings.ToLower(col)
		if strings.Contains(lower, "deleted_by") || strings.Contains(lower, "deleted_user") {
			authorColumn = true
		}
	}

	// The batch path on a model that DOES have deleted_at.
	bulk := &opsDoc{ID: 3, Title: "bulk"}
	if err := quark.For[opsDoc](ctx, c).Create(bulk); err != nil {
		t.Fatalf("create bulk: %v", err)
	}
	if _, err := quark.For[opsDoc](ctx, c).Where("id", "=", 3).DeleteBy(); err != nil {
		t.Fatalf("DeleteBy: %v", err)
	}
	survived, _ := quark.For[opsDoc](ctx, c).Unscoped().Where("id", "=", 3).List()
	deleteByIsSoft := len(survived) == 1

	// Four facts, four ways this control can gain ground: the renamed column,
	// the tagged column, the author, and DeleteBy. One shared `partial` would
	// swallow any of them closing.
	switch {
	case renamedIsSoft && len(remaining) == 1 && taggedIsSoft && authorColumn && deleteByIsSoft:
		return present
	case !renamedIsSoft && len(remaining) == 0 && !taggedIsSoft && taggedRefused && !authorColumn && !deleteByIsSoft:
		return partial
	default:
		t.Fatalf("the soft-delete gaps moved apart: renamedIsSoft=%v rowsLeft=%d taggedIsSoft=%v "+
			"taggedRefused=%v authorColumn=%v deleteByIsSoft=%v — update the recorded verdict "+
			"and the note in the same change",
			renamedIsSoft, len(remaining), taggedIsSoft, taggedRefused, authorColumn, deleteByIsSoft)
		return absent // unreachable; t.Fatalf stops the probe
	}
}

// probeQueryCache measures the cache through the two things an application can
// see: the statements that were NOT sent, and the tags Quark asked the store
// to drop. The composite-PK gap shows up in those tags — the same write that
// carries a row tag for a single-key model carries only the table tag when the
// key has two columns, so one row's change drops every cached query on the
// table. The hit itself is invisible to the query observer, which is why a
// deployment cannot count hits and misses.
func probeQueryCache(t *testing.T, e *env) verdict {
	ctx := e.ctx
	store := newOpsStore()
	c, rec := e.fresh(t, "ops_cache", quark.WithCacheStore(store))
	if err := c.Migrate(ctx, &opsAccount{}, &opsComposite{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := quark.For[opsAccount](ctx, c).Create(&opsAccount{ID: 1, Owner: "a", Version: 1}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Cache() with no tag of its own, which is the shape that carries the
	// table tag: a caller-supplied tag REPLACES it, and an entry tagged only
	// by the caller is one the table-level invalidation can no longer reach.
	read := func() int {
		rec.reset()
		if _, err := quark.For[opsAccount](ctx, c).Cache(time.Minute).Where("id", "=", 1).List(); err != nil {
			t.Fatalf("cached read: %v", err)
		}
		return opsCountSelects(rec)
	}
	if read() != 1 {
		t.Fatalf("the first read of a cold cache did not reach the database")
	}
	if hitSQL := read(); hitSQL != 0 {
		return absent // nothing is served from the cache
	}

	// A write must invalidate, or the cache serves the old row forever. The
	// store drops only the entries carrying the tags this write names, so the
	// re-read reaching the database is a statement about QUARK's tags and not
	// about a store that empties itself whatever it is told.
	store.clearTags()
	if err := quark.For[opsAccount](ctx, c).Create(&opsAccount{ID: 2, Owner: "b", Version: 1}); err != nil {
		t.Fatalf("second create: %v", err)
	}
	singleKeyTags := store.tagsSeen()
	if len(singleKeyTags) == 0 {
		// Cached and never invalidated: there is no tag invalidation, which
		// is the whole title, so this cannot read as the recorded partial.
		return absent
	}
	if len(singleKeyTags) != 1 {
		t.Fatalf("one write invalidated %d times; the probe needs exactly one to read its tags", len(singleKeyTags))
	}
	if read() != 1 {
		t.Fatalf("the tags this write carried (%v) do not reach the entry it has to invalidate", singleKeyTags[0])
	}

	// The exact tag set, not "one of them starts with the right prefix": the
	// table tag is what drops every cached query on the table, the row tag is
	// what makes the drop precise, and the title claims both.
	singleKeyExact := opsSameTags(singleKeyTags[0], []string{"ops_account", "ops_account:2"})

	// The same write on a composite-key model, where the row tag cannot be
	// rendered. The composite case is recorded as "table tag ONLY", so the
	// absence of the row tag is asserted alongside the presence of the other.
	store.clearTags()
	if err := quark.For[opsComposite](ctx, c).Create(&opsComposite{TenantID: 1, ItemID: 2, Name: "x"}); err != nil {
		t.Fatalf("composite create: %v", err)
	}
	compositeTags := store.tagsSeen()
	if len(compositeTags) != 1 {
		t.Fatalf("the composite write invalidated %d times; the probe needs exactly one to read its tags", len(compositeTags))
	}
	compositeExact := opsSameTags(compositeTags[0], []string{"ops_composite"})
	compositeRowTagged := false
	for _, tag := range compositeTags[0] {
		if strings.HasPrefix(tag, "ops_composite:") {
			compositeRowTagged = true
		}
	}

	switch {
	case singleKeyExact && compositeRowTagged:
		return present // the composite key gained its row tag
	case singleKeyExact && compositeExact:
		return partial // exactly the recorded shape: row tag on one, table tag only on the other
	default:
		t.Fatalf("the invalidation tags are neither shape the bench records: "+
			"single-key=%v composite=%v — update the recorded verdict and the note "+
			"in the same change", singleKeyTags[0], compositeTags[0])
		return absent // unreachable; t.Fatalf stops the probe
	}
}

// opsSameTags compares two tag sets as SETS: order is the cache's business,
// membership is the contract.
func opsSameTags(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]int, len(got))
	for _, tag := range got {
		seen[tag]++
	}
	for _, tag := range want {
		seen[tag]--
		if seen[tag] < 0 {
			return false
		}
	}
	return true
}

// probeCacheStampede measures both halves of the stampede defence from
// outside. Singleflight: eight concurrent readers of the same cold key must
// reach the database once, which is only a real measurement while the compute
// is slow enough for them to overlap — hence the delaying middleware. Cross
// instance: the winner takes the per-key lock and releases it, and a second
// client that is DENIED the lock serves the value the peer published without
// querying at all.
func probeCacheStampede(t *testing.T, e *env) verdict {
	ctx := e.ctx
	store := newOpsStore()
	c, rec := e.fresh(t, "ops_stampede",
		quark.WithCacheStore(store),
		quark.WithMiddleware(opsSlowQuery{d: 60 * time.Millisecond}),
	)
	opsSeed(t, c, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Version: 1})

	rec.reset()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = quark.For[opsAccount](ctx, c).Cache(time.Minute, "ops").Where("id", "=", 1).List()
		}()
	}
	wg.Wait()
	if computes := opsCountSelects(rec); computes != 1 {
		if computes == 0 {
			t.Fatalf("eight cold readers issued no query at all")
		}
		// The in-process half is the whole of what this bench can reach. If
		// the herd is not collapsed there is no stampede control here at all,
		// and reading it as `partial` would be the recorded verdict again.
		return absent
	}

	// Cross-instance: one store standing in for the shared cache two
	// processes share, and a lock decision the probe controls.
	locking := &opsLockingStore{opsStore: newOpsStore(), winner: true}
	holder, holderRec := e.fresh(t, "ops_stampede_holder",
		quark.WithCacheStore(locking), quark.WithCacheCrossInstance())
	opsSeed(t, holder, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Version: 1})
	holderRec.reset()
	if _, err := quark.For[opsAccount](ctx, holder).Cache(time.Minute).Where("id", "=", 1).List(); err != nil {
		t.Fatalf("holder read: %v", err)
	}
	if len(locking.lockKeys()) == 0 {
		// The hand-off is the second half of the title. A wrapper that stops
		// asking the store for the lock has lost it, and `partial` here would
		// be indistinguishable from the recorded verdict.
		t.Fatalf("the store implements CacheLocker and the wrapper never asked it for a lock")
	}
	locking.lmu.Lock()
	released := locking.releases
	locking.lmu.Unlock()
	if released == 0 {
		t.Fatalf("the recompute lock was taken and never released")
	}

	// The loser: the value is not in the cache yet, the lock is refused, and
	// the peer publishes while it waits.
	published := locking.snapshot()
	locking.lmu.Lock()
	locking.winner = false
	locking.keys = nil
	locking.lmu.Unlock()
	go func() {
		time.Sleep(60 * time.Millisecond)
		locking.restore(published)
	}()
	loser, loserRec := e.fresh(t, "ops_stampede_loser",
		quark.WithCacheStore(locking), quark.WithCacheCrossInstance())
	if err := loser.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate loser: %v", err)
	}
	loserRec.reset()
	rows, err := quark.For[opsAccount](ctx, loser).Cache(time.Minute).Where("id", "=", 1).List()
	if err != nil {
		t.Fatalf("loser read: %v", err)
	}
	if len(rows) != 1 || opsCountSelects(loserRec) != 0 {
		// Not a grade of the verdict: the hand-off is the second half of the
		// title, and a loser that queries anyway is that half failing. If it
		// read as `partial` it would read exactly like the recorded verdict,
		// and the regression would ship green.
		t.Fatalf("the denied client served %d rows after %d queries; "+
			"with the peer's value published it must serve 1 row and query 0 times",
			len(rows), opsCountSelects(loserRec))
	}
	// Everything the wrapper promises in-process is reachable and works; what
	// this bench cannot reach is a second process and a real lock backend —
	// which is the ONLY thing the recorded `partial` stands for.
	return partial
}

// probeOpenTelemetry drives a client through the OTel middleware with the SDK
// recording into memory, then reads the spans and the collected instruments.
// The two gaps are read from the same place: no span carries the table it
// operated on, and the instrument set has nothing about connection pools,
// cache or replica routing — the three things an operator watches first.
func probeOpenTelemetry(t *testing.T, e *env) verdict {
	ctx := e.ctx
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	// The middleware resolves tracer and meter from the global providers, so
	// the probe installs its own and puts the previous ones back: a bench that
	// leaves global state behind measures the next probe too.
	prevTracer, prevMeter := otelapi.GetTracerProvider(), otelapi.GetMeterProvider()
	otelapi.SetTracerProvider(tp)
	otelapi.SetMeterProvider(mp)
	defer func() {
		otelapi.SetTracerProvider(prevTracer)
		otelapi.SetMeterProvider(prevMeter)
	}()

	c, _ := e.fresh(t, "ops_otel", quark.WithMiddleware(quarkotel.New()))
	opsSeed(t, c, &opsAccount{}, &opsAccount{ID: 1, Owner: "topsecret", Version: 1})
	if _, err := quark.For[opsAccount](ctx, c).Where("owner", "=", "topsecret").List(); err != nil {
		t.Fatalf("instrumented read: %v", err)
	}

	ended := spans.Ended()
	if len(ended) == 0 {
		return absent // the middleware is installed and emits nothing
	}
	var sawStatement, sawTable, sawArgs bool
	for _, sp := range ended {
		for _, attr := range sp.Attributes() {
			switch string(attr.Key) {
			case "db.statement":
				sawStatement = true
				if strings.Contains(attr.Value.Emit(), "topsecret") {
					t.Fatalf("a bind value reached db.statement: %s", attr.Value.Emit())
				}
			case "db.statement.args":
				sawArgs = true
			// db.collection.name is what current OTel semconv calls the
			// table; the two older spellings are kept so a span that starts
			// carrying either of them is still seen.
			case "db.table", "db.sql.table", "db.collection.name":
				sawTable = true
			}
		}
	}
	if !sawStatement {
		// Without the statement there is nothing on the span to redact, so
		// neither half of the title can be read off it.
		t.Fatalf("no span carries db.statement; the redaction this control is about has nothing to act on")
	}
	if sawArgs {
		// Redaction by DEFAULT is a contract, not a grade: the recorded
		// verdict is already partial, so returning partial here would leave
		// every bound value on every span invisible to this bench forever.
		t.Fatalf("db.statement.args appeared on a span with no opt-in; " +
			"redaction by default is what keeps the tracing backend from becoming a copy of the data")
	}

	// The other half of redaction: opting in must actually expose the args,
	// or the option is decoration.
	optedIn, _ := e.fresh(t, "ops_otel_args",
		quark.WithMiddleware(quarkotel.New(quarkotel.WithSpanRedaction(quarkotel.IncludeArgs))))
	opsSeed(t, optedIn, &opsAccount{}, &opsAccount{ID: 1, Owner: "topsecret", Version: 1})
	before := len(spans.Ended())
	if _, err := quark.For[opsAccount](ctx, optedIn).Where("owner", "=", "topsecret").List(); err != nil {
		t.Fatalf("opted-in read: %v", err)
	}
	optedInArgs := false
	for _, sp := range spans.Ended()[before:] {
		for _, attr := range sp.Attributes() {
			if string(attr.Key) == "db.statement.args" {
				optedInArgs = true
			}
		}
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	instruments := map[string]bool{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			instruments[m.Name] = true
		}
	}
	// "The instruments cover queries only" has two halves, and a len() > 0
	// measures neither. The positive half is the exact set an operator gets;
	// the negative half is that nothing in it is about a pool, the cache or a
	// replica. An instrument that appeared or vanished changes the answer.
	queryOnly := len(instruments) > 0
	for name := range instruments {
		if !strings.HasPrefix(name, "quark.queries.") {
			queryOnly = false
		}
	}
	for _, name := range []string{"quark.queries.total", "quark.queries.duration"} {
		if !instruments[name] {
			queryOnly = false // the counter and the histogram an operator starts from
		}
	}
	operational := false
	for name := range instruments {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "pool") || strings.Contains(lower, "conn") ||
			strings.Contains(lower, "cache") || strings.Contains(lower, "replica") {
			operational = true
		}
	}
	// One switch over every fact the title and the note assert, so that a
	// half that closes — or one that quietly opens — cannot land on the
	// verdict already recorded.
	switch {
	case optedInArgs && sawTable && operational:
		return present
	case optedInArgs && !sawTable && !operational && queryOnly:
		return partial
	default:
		t.Fatalf("the observability facts moved apart: opt-in args=%v table attribute=%v "+
			"operational instruments=%v instrument set=%v — update the recorded verdict and "+
			"the note in the same change",
			optedInArgs, sawTable, operational, instruments)
		return absent // unreachable; t.Fatalf stops the probe
	}
}

// probeSlowQueryLog measures the line itself, because the control is a log
// line: ONE of them has to appear per statement over the threshold, at WARN,
// carrying the parameterised SQL and NOT the value that was bound into it.
//
// The buffer is emptied after the seed. With a one-nanosecond threshold every
// statement is slow, the seed's own writes included, so a probe that read the
// whole buffer would be counting its own setup and could never tell one line
// per statement from two.
func probeSlowQueryLog(t *testing.T, e *env) verdict {
	ctx := e.ctx
	logger, buf := opsLogger()
	c, _ := e.fresh(t, "ops_slow", quark.WithLogger(logger), quark.WithSlowQueryThreshold(time.Nanosecond))
	opsSeed(t, c, &opsAccount{}, &opsAccount{ID: 1, Owner: "topsecret", Version: 1})
	buf.Reset()
	if _, err := quark.For[opsAccount](ctx, c).Where("owner", "=", "topsecret").Limit(10).List(); err != nil {
		t.Fatalf("read: %v", err)
	}
	logged := buf.String()
	// The buffer also carries lines that are not this control's (the
	// unbounded-read warning, for one), so the count is over the slow-query
	// lines themselves, and the level is read off the line that carries them.
	var slowLines []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "slow query") {
			slowLines = append(slowLines, line)
		}
	}
	if len(slowLines) == 0 {
		return absent
	}
	if len(slowLines) != 1 {
		return partial // one statement, more than one warning: the log is noise
	}
	if !strings.Contains(slowLines[0], "level=WARN") {
		return partial // logged below WARN, where an operator's filter will not see it
	}
	if !strings.Contains(slowLines[0], "SELECT") {
		return partial // a warning that does not say which query is not actionable
	}
	if strings.Contains(logged, "topsecret") {
		return partial // the bind value leaked into the log
	}

	// Below the threshold there must be nothing: a slow-query log that fires
	// for every query is a log nobody reads.
	quietLogger, quietBuf := opsLogger()
	quiet, _ := e.fresh(t, "ops_slow_off", quark.WithLogger(quietLogger), quark.WithSlowQueryThreshold(time.Hour))
	opsSeed(t, quiet, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Version: 1})
	if _, err := quark.For[opsAccount](ctx, quiet).List(); err != nil {
		t.Fatalf("read on the quiet client: %v", err)
	}
	if strings.Contains(quietBuf.String(), "slow query") {
		return partial
	}
	return present
}

// probeStrictReads measures the three modes on the entrypoints that have no
// implicit cap, the per-query escape, and the N+1 detector — including its
// declared limit, that it warns ONCE per context and table and never rejects.
func probeStrictReads(t *testing.T, e *env) verdict {
	ctx := e.ctx
	rejectLogger, _ := opsLogger()
	strict, _ := e.fresh(t, "ops_strict_reject",
		quark.WithLogger(rejectLogger), quark.WithStrictReads(quark.StrictReadsReject))
	opsSeed(t, strict, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Version: 1})

	noop := func(opsAccount) error { return nil }
	if err := quark.For[opsAccount](ctx, strict).Iter(noop); !errors.Is(err, quark.ErrInvalidQuery) {
		return absent // an unbounded stream is accepted under the strictest mode
	}
	if err := quark.For[opsAccount](ctx, strict).Limit(10).Iter(noop); err != nil {
		return partial // a bounded read must not be rejected
	}
	if err := quark.For[opsAccount](ctx, strict).AllowUnbounded().Iter(noop); err != nil {
		return partial // the declared export has no escape
	}
	if _, err := quark.For[opsAccount](ctx, strict).Cursor(); !errors.Is(err, quark.ErrInvalidQuery) {
		return partial // Cursor is the other uncapped entrypoint
	}

	warnLogger, warnBuf := opsLogger()
	warn, _ := e.fresh(t, "ops_strict_warn",
		quark.WithLogger(warnLogger), quark.WithStrictReads(quark.StrictReadsWarn))
	opsSeed(t, warn, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Version: 1})
	opsSeed(t, warn, &opsMarker{}, &opsMarker{ID: 1, Name: "m"})
	if err := quark.For[opsAccount](ctx, warn).Iter(noop); err != nil {
		return partial // warn mode must warn, not fail
	}
	if !strings.Contains(warnBuf.String(), "unbounded read") {
		return partial
	}
	// The title names both uncapped entrypoints, so both are driven in Warn
	// mode too: Cursor has to warn and still hand back the stream.
	warnBefore := strings.Count(warnBuf.String(), "unbounded read")
	warnCursor, err := quark.For[opsAccount](ctx, warn).Cursor()
	if err != nil {
		return partial // warn mode must warn on Cursor, not refuse it
	}
	_ = warnCursor.Close()
	if strings.Count(warnBuf.String(), "unbounded read") == warnBefore {
		return partial // Cursor slipped past the mode that Iter honours
	}

	// N+1: the point reads only count inside a tracked context, and the
	// warning fires once PER TABLE. One table alone cannot tell "once per
	// context and table" from "once per context": a detector that warned and
	// then went quiet for the whole request would pass it. So the same
	// tracked context runs the loop again over a second table and the count
	// has to move to two.
	tracked := quark.TrackReads(ctx)
	for i := 0; i < 25; i++ {
		if _, err := quark.For[opsAccount](tracked, warn).Find(1); err != nil {
			t.Fatalf("point read %d: %v", i, err)
		}
	}
	if got := strings.Count(warnBuf.String(), "N+1"); got != 1 {
		if got == 0 {
			return partial // nothing detects the loop
		}
		t.Fatalf("the N+1 detector warned %d times for one context and table", got)
	}
	for i := 0; i < 25; i++ {
		if _, err := quark.For[opsMarker](tracked, warn).Find(1); err != nil {
			t.Fatalf("second-table point read %d: %v", i, err)
		}
	}
	if got := strings.Count(warnBuf.String(), "N+1"); got != 2 {
		return partial // the detector is keyed by context alone, so the second loop is silent
	}
	untrackedBefore := strings.Count(warnBuf.String(), "N+1")
	for i := 0; i < 25; i++ {
		if _, err := quark.For[opsAccount](ctx, warn).Find(1); err != nil {
			t.Fatalf("untracked point read %d: %v", i, err)
		}
	}
	if strings.Count(warnBuf.String(), "N+1") != untrackedBefore {
		t.Fatalf("reads outside a TrackReads context were counted")
	}
	return present
}

// probeEventBus measures which writes an application actually hears about.
// The batch gap is documented; the Tracked.Save one is not, and it is the one
// that hurts: dirty tracking is the recommended update path, it writes the row
// and the audit line, and the subscriber never learns the row changed.
func probeEventBus(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, _ := e.fresh(t, "ops_events")
	if err := c.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	bus := &opsBus{}
	c.UseEventBus(bus)

	a := &opsAccount{ID: 1, Owner: "a", Version: 1}
	if err := quark.For[opsAccount](ctx, c).Create(a); err != nil {
		t.Fatalf("create: %v", err)
	}
	a.Owner = "b"
	if _, err := quark.For[opsAccount](ctx, c).Update(a); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := quark.For[opsAccount](ctx, c).Delete(a); err != nil {
		t.Fatalf("delete: %v", err)
	}
	crud := bus.events()
	if len(crud) != 3 {
		return absent // the bus is wired and hears nothing
	}
	// Three events is not the claim; three events that say WHAT happened and
	// to WHICH table is. A subscriber routes on the kind, so a bus that
	// published three identical events would be useless and still count 3.
	wantCRUD := []string{"created:ops_account", "updated:ops_account", "deleted:ops_account"}
	for i, want := range wantCRUD {
		if crud[i] != want {
			t.Fatalf("the CRUD events are %v, not %v: a subscriber routes on the kind and the table", crud, wantCRUD)
		}
	}

	// Dirty tracking: a write that happens, and an event that does not.
	tracked := &opsAccount{ID: 20, Owner: "tracked", Version: 1}
	if err := quark.For[opsAccount](ctx, c).Create(tracked); err != nil {
		t.Fatalf("create tracked row: %v", err)
	}
	before := len(bus.events())
	handle, err := quark.For[opsAccount](ctx, c).Track().Find(20)
	if err != nil {
		t.Fatalf("tracked load: %v", err)
	}
	handle.Entity.Owner = "renamed"
	rows, err := handle.Save(ctx)
	if err != nil {
		t.Fatalf("tracked save: %v", err)
	}
	if rows != 1 {
		t.Fatalf("tracked save wrote %d rows; the probe needs a real write to ask about its event", rows)
	}
	trackedEmitted := len(bus.events()) - before

	// Batch writes, the documented scope boundary.
	before = len(bus.events())
	if err := quark.For[opsAccount](ctx, c).CreateBatch([]*opsAccount{{ID: 30, Owner: "x", Version: 1}}); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	batchEmitted := len(bus.events()) - before

	// Delivery: a bus whose Publish fails. The note says the write stands and
	// the event is lost, which is what "no outbox" means from outside — and
	// it is measured by breaking the bus, not by reading the dispatcher.
	failing := &opsFailingBus{}
	c.UseEventBus(failing)
	publishErr := quark.For[opsAccount](ctx, c).Create(&opsAccount{ID: 40, Owner: "orphan", Version: 1})
	orphan, err := quark.For[opsAccount](ctx, c).Where("id", "=", 40).Count()
	if err != nil {
		t.Fatalf("count after a failing publish: %v", err)
	}
	if failing.attempts() == 0 {
		t.Fatalf("the failing bus was never asked to publish; the probe cannot measure delivery")
	}
	writeSurvivedFailedPublish := orphan == 1
	// The note says the error reaches the caller after the fact, so that is a
	// fact of its own and not a by-product of the row surviving. "Best-effort
	// emit" and "silent emit" are two different contracts, and only the first
	// is the one the note publishes: a Create that swallowed the bus failure
	// and returned nil would lose the event with no signal at all, which is
	// strictly worse than the recorded gap.
	publishErrReported := publishErr != nil
	if !writeSurvivedFailedPublish && !publishErrReported {
		t.Fatalf("the row is gone and Create returned nil: the write was undone in silence")
	}

	// The title says both of these emit NOTHING. Asserting that explicitly is
	// the whole point: "tracked emits, batch does not" is a different world
	// and has to read as a different verdict.
	switch {
	case trackedEmitted == 1 && batchEmitted == 1 && !writeSurvivedFailedPublish:
		return present
	case trackedEmitted == 0 && batchEmitted == 0 && writeSurvivedFailedPublish && publishErrReported:
		return partial
	default:
		t.Fatalf("the event scope moved: trackedEmitted=%d batchEmitted=%d "+
			"writeSurvivedFailedPublish=%v publishErrReported=%v — update the recorded "+
			"verdict and the note in the same change",
			trackedEmitted, batchEmitted, writeSurvivedFailedPublish, publishErrReported)
		return absent // unreachable; t.Fatalf stops the probe
	}
}

// probeDirtyTracking reads the UPDATE. "Only what changed" is a claim about a
// statement, not about a row count: an UPDATE that rewrites every column
// returns the same 1 row and silently overwrites a concurrent writer's work.
func probeDirtyTracking(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, rec := e.fresh(t, "ops_dirty")
	opsSeed(t, c, &opsAccount{}, &opsAccount{ID: 1, Owner: "a", Balance: 100, Version: 1})

	handle, err := quark.For[opsAccount](ctx, c).Track().Find(1)
	if err != nil {
		t.Fatalf("tracked load: %v", err)
	}
	rec.reset()
	if rows, err := handle.Save(ctx); err != nil || rows != 0 {
		return partial // an untouched entity still writes
	}
	if len(rec.sql()) != 0 {
		return partial // no change, and a statement was still sent
	}

	handle.Entity.Balance = 250
	if changed := handle.Changed(); len(changed) != 1 || changed[0] != "balance" {
		return partial // the dirty set does not name the changed column
	}
	rec.reset()
	if _, err := handle.Save(ctx); err != nil {
		t.Fatalf("save: %v", err)
	}
	stmt := rec.last()
	if !strings.HasPrefix(stmt, "UPDATE") {
		t.Fatalf("a changed entity emitted %q", stmt)
	}
	setClause := stmt
	if idx := strings.Index(stmt, " WHERE "); idx > 0 {
		setClause = stmt[:idx]
	}
	if !strings.Contains(setClause, "balance") {
		t.Fatalf("the UPDATE does not write the changed column: %s", stmt)
	}
	if strings.Contains(setClause, "owner") {
		return partial // untouched columns are rewritten
	}
	return present
}

// probePagination measures what every pagination entrypoint EMITS. Both of
// them are read here because the absence is only credible if the probe looked
// everywhere the API offers: Paginate spends two statements per page and moves
// with OFFSET, so page N costs the server the N-1 pages before it, and Cursor
// streams one plain SELECT with no continuation predicate and nothing to
// resume from. No entrypoint emits a comparison against the last row read,
// which is what keyset pagination is.
func probePagination(t *testing.T, e *env) verdict {
	ctx := e.ctx
	c, rec := e.fresh(t, "ops_page")
	if err := c.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := 1; i <= 6; i++ {
		if err := quark.For[opsAccount](ctx, c).Create(&opsAccount{ID: int64(i), Owner: "o", Version: 1}); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}

	rec.reset()
	page, err := quark.For[opsAccount](ctx, c).OrderBy("id", "ASC").Paginate(2, 2)
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != 5 {
		t.Fatalf("page 2 of size 2 returned %+v", page.Items)
	}
	stmts := rec.sql()
	keyset := false
	offset := false
	for _, s := range stmts {
		upper := strings.ToUpper(s)
		if strings.Contains(upper, "OFFSET") {
			offset = true
		}
		// A keyset page asks for the rows after the last one it saw: the
		// ordering column appears in the WHERE clause, not only in ORDER BY.
		// The quoting is SQLite's, which is the only dialect this bench
		// emits; a predicate written with another engine's quotes would not
		// be seen here, and would still be caught by the OFFSET check below —
		// a keyset page has no OFFSET to fall back on.
		if where := strings.Index(upper, " WHERE "); where >= 0 {
			tail := upper[where:]
			if end := strings.Index(tail, " ORDER BY "); end > 0 {
				tail = tail[:end]
			}
			if strings.Contains(tail, `"ID" >`) || strings.Contains(tail, `"ID" <`) ||
				strings.Contains(tail, "(\"ID\",") {
				keyset = true
			}
		}
	}
	if keyset {
		return present
	}
	if !offset {
		t.Fatalf("Paginate emitted neither OFFSET nor a keyset predicate: %v", stmts)
	}

	// The second half of the title is about the VALUE, not the statement: a
	// page that carried a resumable token would be keyset pagination even
	// over an OFFSET query. So the returned type is read, not grepped — a
	// token would have to be a field on the page an application holds.
	pageType := reflect.TypeOf(*page)
	for i := 0; i < pageType.NumField(); i++ {
		lower := strings.ToLower(pageType.Field(i).Name)
		if strings.Contains(lower, "cursor") || strings.Contains(lower, "token") ||
			strings.Contains(lower, "after") || strings.Contains(lower, "seek") ||
			strings.Contains(lower, "next") {
			return partial // the page hands back a position to resume from
		}
	}

	// The other entrypoint: streaming, and equally without a continuation.
	rec.reset()
	cursor, err := quark.For[opsAccount](ctx, c).OrderBy("id", "ASC").Limit(2).Cursor()
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	var last opsAccount
	for cursor.Next() {
		if err := cursor.Scan(&last); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	if err := cursor.Close(); err != nil {
		t.Fatalf("close cursor: %v", err)
	}
	for _, s := range rec.sql() {
		if strings.Contains(strings.ToUpper(s), " WHERE ") {
			return partial // the stream carries some continuation after all
		}
	}

	// And the cursor value itself, the same way: a stream an application can
	// resume would have to say where it stopped, through a field or a method.
	// Next/Scan/Err/Close advance and drain it; none of them hands back a
	// position the caller can store and come back with.
	cursorType := reflect.TypeOf(cursor)
	for i := 0; i < cursorType.NumMethod(); i++ {
		lower := strings.ToLower(cursorType.Method(i).Name)
		if strings.Contains(lower, "token") || strings.Contains(lower, "position") ||
			strings.Contains(lower, "resume") || strings.Contains(lower, "after") ||
			strings.Contains(lower, "seek") {
			return partial // the stream exposes somewhere to resume from
		}
	}
	elem := cursorType.Elem()
	for i := 0; i < elem.NumField(); i++ {
		if elem.Field(i).IsExported() {
			return partial // a field an application could read a position out of
		}
	}
	return absent
}

// probeSchemaSync drives the two changes an application makes to a live
// schema — a new column and a renamed one — and then measures the transaction
// the API advertises. The failure is staged with an indexed column the model
// no longer declares: SQLite refuses to drop it, the sync fails, and the table
// it had created moments earlier is still there afterwards, which is what
// "the transaction does not cover the CREATE" looks like from outside.
func probeSchemaSync(t *testing.T, e *env) verdict {
	ctx := e.ctx
	limits := quark.DefaultLimits()
	limits.SafeMigrations = false // the drop path is part of the control
	c, _ := e.fresh(t, "ops_sync", quark.WithLimits(limits))

	if err := c.Migrate(ctx, &opsSyncV1{}); err != nil {
		t.Fatalf("migrate v1: %v", err)
	}
	// A row with a value in the column about to be renamed. A rename is a
	// promise about the DATA, and drop-then-add produces exactly the same
	// column list while losing every row's value — so the column list alone
	// cannot tell a rename from a silent data loss.
	if _, err := c.Raw().ExecContext(ctx,
		`INSERT INTO ops_sync (id, keep, legacy) VALUES (1, 'carried', 'l')`); err != nil {
		t.Fatalf("seed the row the rename has to carry: %v", err)
	}
	if err := c.Sync(ctx, quark.SyncOptions{}, &opsSyncV2{}); err != nil {
		t.Fatalf("sync v2: %v", err)
	}
	// Exact set, not substrings: "keep" is a prefix of "kept", so a check for
	// the old name by substring depends on where in the list it lands.
	after := opsColumns(t, c, "ops_sync")
	if !opsSameTags(after, []string{"id", "kept", "legacy", "added"}) {
		if !opsHasColumn(after, "added") {
			return absent // a new column does not reach the table
		}
		// Not a grade of the verdict, for the same reason as the emptied
		// column three lines below: the rename is one of the two capabilities
		// the title asserts, while the recorded `partial` speaks only about
		// the transaction that does not cover the CREATE. Reading a lost
		// rename as that same `partial` would keep the bench green through
		// the regression.
		t.Fatalf("Sync left the columns %v: the rename the title asserts was not applied — "+
			"update the recorded verdict and the note in the same change", after)
	}
	var carried string
	if err := c.Raw().QueryRowContext(ctx, `SELECT kept FROM ops_sync WHERE id = 1`).Scan(&carried); err != nil {
		t.Fatalf("read the renamed column: %v", err)
	}
	if carried != "carried" {
		// Not a grade of the verdict: a "rename" that empties the column is
		// data loss, and the recorded partial would hide it.
		t.Fatalf("the renamed column holds %q, not the value the old one carried: "+
			"the rename dropped and re-added instead of renaming", carried)
	}

	// Stage a drop the engine will refuse, so the sync fails after it has
	// already created a table.
	if err := c.CreateIndex(ctx, "ops_sync", "ops_sync_legacy_idx", []string{"legacy"}, false); err != nil {
		t.Fatalf("create index: %v", err)
	}
	err := c.Sync(ctx, quark.SyncOptions{}, &opsSyncNew{}, &opsSyncV3{})
	if err == nil {
		t.Fatalf("the sync that had to fail succeeded; the probe cannot measure the rollback")
	}
	// The migration lock the note used to assert about: on this engine there
	// is none to take at all. AcquireMigrationLock is the only way an
	// application asks for one, and SQLite's dialect refuses it — so whether
	// Sync would take a lock is a question only a live engine answers, and
	// the note says so instead of claiming an answer.
	if _, lockErr := c.AcquireMigrationLock(ctx, "ops_sync_lock", time.Second); !errors.Is(lockErr, quark.ErrUnsupportedFeature) {
		t.Fatalf("SQLite answered AcquireMigrationLock with %v: the note says no lock can be "+
			"measured on this engine, and that is no longer true", lockErr)
	}

	if opsTableExists(t, c, "ops_sync_new") {
		// The ALTERs rolled back, the CREATE did not.
		return partial
	}
	return present
}

// opsHasColumn reports whether a column list contains an exact name.
func opsHasColumn(cols []string, name string) bool {
	for _, col := range cols {
		if col == name {
			return true
		}
	}
	return false
}

// probeSeeding measures what the seeding surface actually is. Registration
// order is a promise an application depends on, so it is checked; and then the
// same seeder runs twice against the same database, which is the question a
// seeding ENGINE answers (it would refuse, or record that it already ran) and
// a registry cannot: both runs land, and the database holds no ledger of
// either.
func probeSeeding(t *testing.T, e *env) verdict {
	ctx := e.ctx
	// The registry is process-global; leave it as it was found.
	seed.Reset()
	t.Cleanup(seed.Reset)

	c, _ := e.fresh(t, "ops_seed")
	if err := c.Migrate(ctx, &opsAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	runs := 0
	seed.Register("ops_beta", func(ctx context.Context, cl *quark.Client) error {
		runs++
		return quark.For[opsAccount](ctx, cl).Create(&opsAccount{ID: int64(100 + runs), Owner: "seeded", Version: 1})
	})
	seed.Register("ops_alpha", func(context.Context, *quark.Client) error { return nil })
	names := seed.Names()
	if len(names) != 2 || names[0] != "ops_beta" || names[1] != "ops_alpha" {
		// "Ordered registry" is the only capability the title names. If the
		// order is not registration order there is no capability left, and
		// reporting the recorded partial would keep the bench green over it.
		return absent
	}
	if seed.Count() != 2 {
		return absent
	}
	fn, ok := seed.Get("ops_beta")
	if !ok {
		return absent
	}
	if err := fn(ctx, c); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := fn(ctx, c); err != nil {
		t.Fatalf("second run: %v", err)
	}
	seeded, err := quark.For[opsAccount](ctx, c).Where("owner", "=", "seeded").Count()
	if err != nil {
		t.Fatalf("count seeded rows: %v", err)
	}
	if seeded != 2 {
		return present // something refused or deduplicated the second run
	}

	// "No transaction" is the third claim in the title and the easiest of the
	// three to measure: a seeder that writes a row and THEN fails. If anything
	// wrapped the seeder, the row would be gone with the error.
	seed.Register("ops_halfway", func(ctx context.Context, cl *quark.Client) error {
		if err := quark.For[opsAccount](ctx, cl).Create(&opsAccount{ID: 200, Owner: "halfway", Version: 1}); err != nil {
			return err
		}
		return errors.New("seeder failed after writing")
	})
	halfway, ok := seed.Get("ops_halfway")
	if !ok {
		return absent
	}
	if err := halfway(ctx, c); err == nil {
		t.Fatalf("the seeder that has to fail succeeded; the probe cannot measure the rollback")
	}
	halfwayRows, err := quark.For[opsAccount](ctx, c).Where("owner", "=", "halfway").Count()
	if err != nil {
		t.Fatalf("count after the failing seeder: %v", err)
	}
	if halfwayRows == 0 {
		return present // the write was rolled back: a seeder runs in a transaction after all
	}

	// And nothing recorded that it ran: unlike migrations, there is no state
	// table to ask.
	rows, err := c.Raw().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND lower(name) LIKE '%seed%'`)
	if err != nil {
		t.Fatalf("read catalogue: %v", err)
	}
	ledger := false
	for rows.Next() {
		ledger = true
	}
	_ = rows.Close()
	if ledger {
		return present
	}
	return partial
}

// probeTestKit uses the kit the way an application's test would, and then asks
// it for the one thing its own documentation says it cannot do: observe what
// happens at commit. The rollback isolation is real; a post-commit callback
// registered inside the kit's transaction never runs, so behaviour that only
// exists after a commit is out of reach of the kit.
func probeTestKit(t *testing.T, e *env) verdict {
	ctx := e.ctx
	_ = e
	// The engine the kit speaks is measured, not assumed: the environment
	// names a PostgreSQL DSN the way the engine suites do, and the kit is
	// asked for a client anyway. A kit that could reach another engine would
	// either hand back that engine's client or skip; this one does neither.
	t.Setenv("QUARK_TEST_POSTGRES_DSN", "postgres://bench:bench@127.0.0.1:1/bench?sslmode=disable")
	c := quarktest.SQLite(t, quark.WithLogger(quiet))
	if c.Dialect().Name() != "sqlite" {
		// The kit honoured the DSN: it reaches an engine beyond SQLite, which
		// is more than the note records.
		return present
	}
	quarktest.Migrate(t, c, &opsAccount{})

	var committed int
	var insideTx int64
	quarktest.Tx(t, c, func(tx *quark.Tx) {
		if err := quark.ForTx[opsAccount](ctx, tx).Create(&opsAccount{ID: 1, Owner: "a", Version: 1}); err != nil {
			t.Fatalf("write inside the kit transaction: %v", err)
		}
		n, err := quark.ForTx[opsAccount](ctx, tx).Count()
		if err != nil {
			t.Fatalf("count inside the kit transaction: %v", err)
		}
		insideTx = n
		tx.OnCommit(func(context.Context) error { committed++; return nil })
	})
	if insideTx != 1 {
		return absent // the write is not even visible to the test that made it
	}
	left, err := quark.For[opsAccount](ctx, c).Count()
	if err != nil {
		t.Fatalf("count after the kit transaction: %v", err)
	}
	if left != 0 {
		// Rollback-per-test is the FIRST clause of the title and the reason
		// the kit exists. A kit that leaks a write into the next test is
		// broken, not partial — and the recorded verdict is partial, so
		// returning it here would keep the bench green over the leak.
		t.Fatalf("%d row(s) survived the kit transaction: the rollback-per-test isolation does not hold", left)
	}
	if committed > 0 {
		// A post-commit callback that fires under an enforced rollback would
		// be worse than not firing: the test would see effects of a commit
		// that never happened.
		t.Fatalf("a post-commit callback fired inside a transaction that always rolls back")
	}
	return partial
}

// --- schema-sync models -----------------------------------------------------
//
// Three versions of one table plus a table that does not exist yet: the shape
// of an application deploying a schema change while a previous column is still
// in the database.

type opsSyncV1 struct {
	ID     int64  `db:"id" pk:"true"`
	Keep   string `db:"keep"`
	Legacy string `db:"legacy"`
}

func (opsSyncV1) TableName() string { return "ops_sync" }

// opsSyncV2 renames keep → kept and adds a column.
type opsSyncV2 struct {
	ID     int64  `db:"id" pk:"true"`
	Kept   string `db:"kept" quark:"rename:keep"`
	Added  string `db:"added"`
	Legacy string `db:"legacy"`
}

func (opsSyncV2) TableName() string { return "ops_sync" }

// opsSyncV3 drops legacy, which is the statement the staged index makes fail.
type opsSyncV3 struct {
	ID    int64  `db:"id" pk:"true"`
	Kept  string `db:"kept"`
	Added string `db:"added"`
}

func (opsSyncV3) TableName() string { return "ops_sync" }

type opsSyncNew struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (opsSyncNew) TableName() string { return "ops_sync_new" }
