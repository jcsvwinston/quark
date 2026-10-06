// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"container/list"
	"context"
	"database/sql"
	"sync"
)

// WithStatementCache keeps up to size prepared statements per connection pool
// and reuses them, instead of letting the driver prepare, execute and close a
// statement on every query (QK-37, ADR-0027). Off by default; size <= 0 keeps
// it off.
//
// What it changes is the protocol, not the results. On MySQL and MariaDB,
// go-sql-driver/mysql sends a query that has arguments as three commands —
// prepare, execute, close — and two round trips; with the cache the prepare
// and the close happen once per statement and connection, and every later
// call is one execute. The server prepares exactly the statements it already
// prepared without the cache, so the SQL it runs, the binary protocol it
// answers in and the values the caller reads back are the same. Measured on
// the engine bench (benchmarks/engines, control MY-02, mysql:8.4), FindByPK
// took 46–48 % less time with the cache than without it, in the same rounds,
// on the CI runner, and 41–42 % less on a laptop; the figures and the
// machines are on the benchmarks page.
//
// What is cached: the statements the query builder sends (StatementQuery and
// StatementExec) that carry at least one argument, no sql.NamedArg, and at
// most 4 KiB of SQL. A statement without arguments is left alone because the
// MySQL driver sends it as plain text today, and preparing it would change
// the protocol it travels in; a long one is almost always a multi-row batch
// whose text changes with the row count. Schema work, savepoints and raw SQL
// are never cached.
//
// Where it applies: the client's pool and each replica pool get a cache of
// their own, least recently used out first. Inside a transaction, a statement
// is prepared on the transaction's connection the first time the transaction
// sends it, and reused for the rest of it. Statements sent on a
// *sql.Conn — the migration lock, the per-statement transaction of
// RowLevelSecurityNative — and on any executor a middleware substitutes go to
// the driver as before. The middleware chain and the observers see the same
// statements and the same events either way.
//
// PostgreSQL and Oracle: the option is ignored, and New says so in its log.
// pgx already keeps a statement cache per connection, which also handles a
// plan that a schema change invalidated; a database/sql statement on top of
// it would prepare twice and handle neither. On Oracle, go-ora v2.9.0 — the
// driver drivers/oracle ships — answered a re-executed prepared SELECT
// COUNT(*) with 0 for a row that existed, once a DELETE and an INSERT had run
// between two executions, on one connection and without Quark in between; a
// stale count is how Quark tells a stale entity from an excluded one, so the
// cache stays off there until the driver is fixed (ADR-0027).
//
// Sizing: each connection that runs a cached statement holds it prepared on
// the server until the statement is evicted or the connection closes, so a
// pool can hold up to size × MaxOpenConns statements. MySQL caps prepared
// statements per server with max_prepared_stmt_count (16382 by default),
// across every client of the server; size it so every application instance
// fits under it.
//
// The MySQL alternative is interpolateParams=true in the DSN: the driver then
// writes the arguments into the SQL text and sends one query, with no
// server-side statement at all. The dialects reference page compares the two.
//
//	client, err := quark.New("mysql", dsn, quark.WithStatementCache(256))
func WithStatementCache(size int) Option {
	return func(c *Client) {
		c.stmtCacheSize = size
	}
}

// stmtCacheRefusal says why the statement cache does not run on a dialect,
// or "" when it does.
func stmtCacheRefusal(dialect string) string {
	switch dialect {
	case "postgres":
		return "the pgx driver already keeps a statement cache per connection"
	case "oracle":
		return "go-ora v2.9.0 answered a re-executed prepared statement from a stale result (ADR-0027)"
	}
	return ""
}

// stmtCacheMaxSQL is the longest statement the cache keeps. Single-row
// statements of a wide model and IN lists of a few hundred keys fit; a
// multi-row INSERT of a batch, whose text changes with every row count, does
// not, and would only push the statements that repeat out of the cache.
const stmtCacheMaxSQL = 4 << 10

// stmtCache is a client's statement cache: one LRU per pool the client
// sends statements to, and the statements bound to each open transaction.
type stmtCache struct {
	size  int
	pools map[*sql.DB]*stmtLRU // the primary and each replica; fixed after New
	// txs holds, for every transaction the client opened while the cache was
	// on, the statements bound to it. BeginTx adds the entry; Commit and
	// Rollback remove it.
	txs sync.Map // *sql.Tx → *txStmts
}

func newStmtCache(size int, primary *sql.DB, replicas []*sql.DB) *stmtCache {
	sc := &stmtCache{size: size, pools: make(map[*sql.DB]*stmtLRU, 1+len(replicas))}
	sc.pools[primary] = newStmtLRU(primary, size)
	for _, r := range replicas {
		sc.pools[r] = newStmtLRU(r, size)
	}
	return sc
}

// cacheable reports whether a statement goes through the cache (see
// WithStatementCache for why each rule is there).
func cacheable(st *stmt, sqlStr string, args []any) bool {
	if st.kind != StatementQuery && st.kind != StatementExec {
		return false
	}
	if len(args) == 0 || len(sqlStr) > stmtCacheMaxSQL {
		return false
	}
	for _, a := range args {
		if _, named := a.(sql.NamedArg); named {
			return false
		}
	}
	return true
}

// stmtLease is a cached statement lent to one call. Returned by value so a
// call allocates nothing for it.
type stmtLease struct {
	stmt  *sql.Stmt
	lru   *stmtLRU   // set when the statement came from a pool's LRU
	entry *stmtEntry // its entry there
}

// release hands the statement back. A pool statement is counted back, and the
// LRU closes it once it is evicted and no call holds it. A transaction's
// statement needs nothing: it stays bound until the transaction ends, an
// error of one call included — an execution error does not undo a
// preparation, and dropping the statement to prepare it again would pile
// preparations up on the connection of a transaction that keeps failing.
func (l stmtLease) release() {
	if l.lru != nil {
		l.lru.release(l.entry)
	}
}

// lease returns the cached statement to run sqlStr on exec, or a zero lease
// (stmt == nil) when the cache does not serve this statement or this
// executor, and the caller sends it the way it always did. A nil cache
// serves nothing, so the hot path costs one comparison when the option is
// off.
func (sc *stmtCache) lease(ctx context.Context, exec Executor, st *stmt, sqlStr string, args []any) stmtLease {
	if sc == nil || !cacheable(st, sqlStr, args) {
		return stmtLease{}
	}
	switch e := exec.(type) {
	case *sql.DB:
		lru := sc.pools[e]
		if lru == nil {
			return stmtLease{}
		}
		entry := lru.acquire(ctx, sqlStr)
		if entry == nil {
			return stmtLease{}
		}
		return stmtLease{stmt: entry.stmt, lru: lru, entry: entry}
	case *sql.Tx:
		v, ok := sc.txs.Load(e)
		if !ok {
			return stmtLease{}
		}
		return stmtLease{stmt: v.(*txStmts).get(ctx, e, sqlStr)}
	}
	return stmtLease{}
}

// beginTx starts tracking the statements of a transaction the client opened.
func (sc *stmtCache) beginTx(tx *sql.Tx) {
	if sc == nil {
		return
	}
	sc.txs.Store(tx, &txStmts{max: sc.size, m: map[string]*sql.Stmt{}})
}

// endTx stops tracking a transaction once it committed or rolled back.
// database/sql closes the statements bound to it on its own.
func (sc *stmtCache) endTx(tx *sql.Tx) {
	if sc == nil {
		return
	}
	sc.txs.Delete(tx)
}

// close closes every pool statement no call holds, and marks the rest to be
// closed when their call hands them back.
func (sc *stmtCache) close() {
	if sc == nil {
		return
	}
	for _, lru := range sc.pools {
		lru.closeAll()
	}
}

// --- one pool's LRU ---------------------------------------------------------

type stmtEntry struct {
	query   string
	stmt    *sql.Stmt
	refs    int  // calls holding it
	evicted bool // out of the LRU; closed when refs reaches zero
}

type stmtLRU struct {
	db  *sql.DB
	max int

	mu      sync.Mutex
	entries map[string]*list.Element // query → element holding *stmtEntry
	order   *list.List               // front: most recently used
	closed  bool
}

func newStmtLRU(db *sql.DB, max int) *stmtLRU {
	return &stmtLRU{db: db, max: max, entries: make(map[string]*list.Element, max), order: list.New()}
}

// acquire returns the entry for query, preparing the statement on a miss, and
// counts the caller in. It returns nil when the statement cannot be prepared:
// the caller then sends it the uncached way, and gets the driver's own error
// if there is one.
//
// The prepare runs outside the lock: on MySQL it is a round trip, and holding
// the lock through it would queue every other statement of the pool behind
// it. Two callers that miss on the same query at once both prepare; the
// second to come back uses the first one's statement and closes its own.
func (l *stmtLRU) acquire(ctx context.Context, query string) *stmtEntry {
	if e := l.lookup(query); e != nil {
		return e
	}
	prepared, err := l.db.PrepareContext(ctx, query)
	if err != nil {
		return nil
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = prepared.Close()
		return nil
	}
	if el, ok := l.entries[query]; ok {
		l.order.MoveToFront(el)
		e := el.Value.(*stmtEntry)
		e.refs++
		l.mu.Unlock()
		_ = prepared.Close()
		return e
	}
	e := &stmtEntry{query: query, stmt: prepared, refs: 1}
	l.entries[query] = l.order.PushFront(e)
	var toClose []*sql.Stmt
	for l.order.Len() > l.max {
		tail := l.order.Back()
		old := tail.Value.(*stmtEntry)
		l.order.Remove(tail)
		delete(l.entries, old.query)
		old.evicted = true
		if old.refs == 0 {
			toClose = append(toClose, old.stmt)
		}
	}
	l.mu.Unlock()
	for _, s := range toClose {
		_ = s.Close()
	}
	return e
}

// lookup is acquire without the prepare: the entry for query, counted in, or
// nil when the LRU does not hold it.
func (l *stmtLRU) lookup(query string) *stmtEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	el, ok := l.entries[query]
	if !ok {
		return nil
	}
	l.order.MoveToFront(el)
	e := el.Value.(*stmtEntry)
	e.refs++
	return e
}

// release counts a caller out, and closes the statement if it was evicted
// while the caller held it. Rows the caller is still reading keep the
// driver's statement alive past the Close: database/sql defers the final
// close until they are closed.
func (l *stmtLRU) release(e *stmtEntry) {
	l.mu.Lock()
	e.refs--
	closeIt := e.evicted && e.refs == 0
	l.mu.Unlock()
	if closeIt {
		_ = e.stmt.Close()
	}
}

func (l *stmtLRU) closeAll() {
	l.mu.Lock()
	l.closed = true
	var toClose []*sql.Stmt
	for el := l.order.Front(); el != nil; el = el.Next() {
		e := el.Value.(*stmtEntry)
		e.evicted = true
		if e.refs == 0 {
			toClose = append(toClose, e.stmt)
		}
	}
	l.order.Init()
	l.entries = map[string]*list.Element{}
	l.mu.Unlock()
	for _, s := range toClose {
		_ = s.Close()
	}
}

// len is the number of statements the LRU holds; tests read it.
func (l *stmtLRU) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}

// --- one transaction's statements ---------------------------------------------

// txStmts is the statements bound to one transaction: each prepared once on
// the transaction's connection with (*sql.Tx).PrepareContext, the first time
// the transaction sends it, and reused for the rest of the transaction.
// database/sql closes them when the transaction ends.
//
// Two shortcuts were left out on purpose. Binding the pool's statement with
// (*sql.Tx).StmtContext would skip that first prepare when the connection
// already has the statement, but it hides a failed preparation inside the
// returned statement until its first use — through QueryRow, after the lease
// is gone — so a prepare that timed out would fail every later call of that
// statement in the transaction. And preparing a missing statement on the pool
// needs a pool connection, which the transaction may be holding: with
// MaxOpenConns(1) that wait never ends.
type txStmts struct {
	max int

	mu sync.Mutex
	m  map[string]*sql.Stmt
}

// get returns the statement for query bound to tx, or nil to send it
// uncached: when the transaction already holds as many statements as the
// cache's size, or when it cannot be prepared. Without the bound, a
// transaction that sends many distinct statements would keep every one of
// them prepared on its connection until it ends.
func (t *txStmts) get(ctx context.Context, tx *sql.Tx, query string) *sql.Stmt {
	t.mu.Lock()
	if s, ok := t.m[query]; ok {
		t.mu.Unlock()
		return s
	}
	full := len(t.m) >= t.max
	t.mu.Unlock()
	if full {
		return nil
	}
	s, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.m[query]; ok {
		// Another call of the same transaction got there first; database/sql
		// closes the spare with the transaction.
		return prev
	}
	t.m[query] = s
	return s
}
