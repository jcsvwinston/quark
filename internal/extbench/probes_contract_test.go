// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// Probes for the "contract" family: the extension points a third party
// programs against, whether each one works from outside package quark, and
// whether anything declares — and freezes — which of them are a contract.
//
// The extension points are implemented HERE, in package extbench, which can
// only see quark's exported API: if a hook, a store or a middleware works from
// this package, it works from an application's.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// --- the census -------------------------------------------------------------
//
// What a contract page has to cover is not a list this bench keeps by hand:
// it is every exported type of quark and quarkdriver that a third party CAN
// implement and hand back — an interface with no unexported method, a
// function type, or a record of functions (quarkdriver.Classifier). Computed
// from the type-checked API, so a type added tomorrow joins the census
// without anyone editing this file.

type censusEntry struct {
	pkg     string // import path
	qual    string // "quark" or "quarkdriver"
	name    string
	kind    string   // "interface" | "func" | "record"
	members []string // interface methods, or record fields
}

func (c censusEntry) qualified() string { return c.qual + "." + c.name }

func implementable(a *apiTypes) []censusEntry {
	var out []censusEntry
	for _, pkg := range []struct{ path, qual string }{{rootPkg, "quark"}, {driverPkg, "quarkdriver"}} {
		p := a.pkgs[pkg.path]
		for _, name := range p.Scope().Names() {
			tn, ok := p.Scope().Lookup(name).(*types.TypeName)
			if !ok || !tn.Exported() {
				continue
			}
			// An alias of a type the census already counts is the same
			// type twice (quark.EventListener = quarkdriver.Listener). An
			// alias of an INTERNAL type is the only name a third party can
			// write, so it stays (quark.TypeMapper).
			if tn.IsAlias() {
				if named, ok := types.Unalias(tn.Type()).(*types.Named); ok {
					if path := named.Obj().Pkg().Path(); path == rootPkg || path == driverPkg {
						continue
					}
				}
			}
			e := censusEntry{pkg: pkg.path, qual: pkg.qual, name: name}
			switch u := tn.Type().Underlying().(type) {
			case *types.Interface:
				if !u.IsMethodSet() || u.NumMethods() == 0 {
					continue // a constraint, or `any`
				}
				sealed := false
				for i := 0; i < u.NumMethods(); i++ {
					if !u.Method(i).Exported() {
						sealed = true
					}
					e.members = append(e.members, u.Method(i).Name())
				}
				if sealed {
					continue // implementable only inside the package
				}
				e.kind = "interface"
			case *types.Signature:
				e.kind = "func"
			case *types.Struct:
				if u.NumFields() == 0 {
					continue
				}
				allFuncs := true
				for i := 0; i < u.NumFields(); i++ {
					f := u.Field(i)
					if _, isFunc := f.Type().Underlying().(*types.Signature); !isFunc || !f.Exported() {
						allFuncs = false
					}
					e.members = append(e.members, f.Name())
				}
				if !allFuncs {
					continue
				}
				e.kind = "record"
			default:
				continue
			}
			sort.Strings(e.members)
			out = append(out, e)
		}
	}
	return out
}

// --- CON-01 -------------------------------------------------------------------

// docPages returns every published page of the site's current docs.
func docPages(t *testing.T, e *env) map[string]string {
	t.Helper()
	pages := map[string]string{}
	base := filepath.Join(e.root, "website", "docs")
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdx")) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(e.root, path)
		pages[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	if len(pages) == 0 {
		t.Fatalf("no pages under %s: the probe would measure nothing and call it absent", base)
	}
	return pages
}

var backticked = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_.]*)`")

// stabilityRows returns, for one page, the symbols that a markdown table with
// a "stability" column declares — one per row, the first backticked name of
// the row's first cell.
func stabilityRows(page string) []string {
	var rows []string
	inTable := false
	for _, line := range strings.Split(page, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			inTable = false
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if !inTable {
			for _, c := range cells {
				if strings.EqualFold(strings.TrimSpace(c), "stability") {
					inTable = true
				}
			}
			continue
		}
		if m := backticked.FindStringSubmatch(cells[0]); m != nil {
			rows = append(rows, m[1])
		}
	}
	return rows
}

func probeContractPage(t *testing.T, e *env) verdict {
	census := implementable(e.loadAPI(t))
	if len(census) == 0 {
		t.Fatal("the census of implementable types is empty: the probe is broken, not the contract")
	}
	known := map[string]censusEntry{}
	for _, c := range census {
		known[c.name] = c
		known[c.qualified()] = c
	}

	declared := map[string]bool{} // census entries a stability row declares
	dangling := 0                 // rows naming nothing in the census
	bestPage, bestMentions := "", 0
	for path, page := range docPages(t, e) {
		for _, sym := range stabilityRows(page) {
			if c, ok := known[sym]; ok {
				declared[c.qualified()] = true
			} else {
				dangling++
			}
		}
		mentioned := map[string]bool{}
		for _, m := range backticked.FindAllStringSubmatch(page, -1) {
			if c, ok := known[m[1]]; ok {
				mentioned[c.qualified()] = true
			}
		}
		if len(mentioned) > bestMentions {
			bestPage, bestMentions = path, len(mentioned)
		}
	}

	kinds := map[string]int{}
	for _, c := range census {
		kinds[c.kind]++
	}
	t.Logf("census: %d implementable types (%d interfaces, %d function types, %d records of functions)",
		len(census), kinds["interface"], kinds["func"], kinds["record"])
	t.Logf("declared with a stability on some page: %d of %d (%d rows name nothing in the census)", len(declared), len(census), dangling)
	t.Logf("the page that names the most of them: %s, with %d of %d", bestPage, bestMentions, len(census))

	switch {
	case len(declared) == len(census) && dangling == 0:
		return present
	case len(declared) > 0:
		return partial
	default:
		return absent
	}
}

// --- CON-02 -------------------------------------------------------------------

func probeSurfaceFreeze(t *testing.T, e *env) verdict {
	census := implementable(e.loadAPI(t))
	raw, err := os.ReadFile(filepath.Join(e.root, "acceptance", "apisurface.json"))
	if err != nil {
		t.Fatalf("read the frozen surface: %v", err)
	}
	var surface struct {
		Symbols []map[string]any `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &surface); err != nil {
		t.Fatalf("parse apisurface.json: %v", err)
	}
	recorded := map[string]bool{}
	fields := map[string]bool{}
	for _, s := range surface.Symbols {
		recorded[fmt.Sprint(s["pkg"])+" "+fmt.Sprint(s["name"])] = true
		for k := range s {
			fields[k] = true
		}
	}

	// What a third party's code depends on: an interface's methods, a
	// record's fields, a function type itself.
	var total, named int
	var missing []string
	for _, c := range census {
		var keys []string
		switch c.kind {
		case "interface":
			for _, m := range c.members {
				keys = append(keys, fmt.Sprintf("(%s).%s", c.name, m))
			}
		case "record":
			for _, f := range c.members {
				keys = append(keys, c.name+"."+f)
			}
		case "func":
			keys = append(keys, c.name)
		}
		for _, k := range keys {
			total++
			if recorded[c.pkg+" "+k] {
				named++
			} else {
				missing = append(missing, c.qual+"."+k)
			}
		}
	}
	var fieldList []string
	for f := range fields {
		fieldList = append(fieldList, f)
	}
	sort.Strings(fieldList)
	carriesSignature := false
	for _, f := range fieldList {
		if f != "pkg" && f != "name" && f != "kind" {
			carriesSignature = true
		}
	}
	t.Logf("members a third party depends on: %d; recorded by name in apisurface.json: %d; not recorded: %v", total, named, missing)
	t.Logf("an entry of apisurface.json carries %v — a signature change leaves it byte-identical: %v", fieldList, !carriesSignature)

	switch {
	case named == total && carriesSignature:
		return present
	case named > 0:
		return partial
	default:
		return absent
	}
}

// --- CON-03 -------------------------------------------------------------------

var errHookAbort = errors.New("extbench: the BeforeCreate hook refused")

var hookLog struct {
	sync.Mutex
	fired map[string]int
	abort bool
}

func hookFired(name string) {
	hookLog.Lock()
	defer hookLog.Unlock()
	hookLog.fired[name]++
}

type hookRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (hookRow) TableName() string { return "hook_rows" }

func (*hookRow) BeforeCreate(context.Context) error {
	hookFired("BeforeCreate")
	hookLog.Lock()
	defer hookLog.Unlock()
	if hookLog.abort {
		return errHookAbort
	}
	return nil
}
func (*hookRow) AfterCreate(context.Context) error  { hookFired("AfterCreate"); return nil }
func (*hookRow) BeforeUpdate(context.Context) error { hookFired("BeforeUpdate"); return nil }
func (*hookRow) AfterUpdate(context.Context) error  { hookFired("AfterUpdate"); return nil }
func (*hookRow) BeforeDelete(context.Context) error { hookFired("BeforeDelete"); return nil }
func (*hookRow) AfterDelete(context.Context) error  { hookFired("AfterDelete"); return nil }
func (*hookRow) BeforeFind(context.Context) error   { hookFired("BeforeFind"); return nil }
func (*hookRow) AfterFind(context.Context) error    { hookFired("AfterFind"); return nil }

// The compiler checks the hooks are the exported interfaces, not look-alikes.
var (
	_ quark.BeforeCreateHook = (*hookRow)(nil)
	_ quark.AfterCreateHook  = (*hookRow)(nil)
	_ quark.BeforeUpdateHook = (*hookRow)(nil)
	_ quark.AfterUpdateHook  = (*hookRow)(nil)
	_ quark.BeforeDeleteHook = (*hookRow)(nil)
	_ quark.AfterDeleteHook  = (*hookRow)(nil)
	_ quark.BeforeFindHook   = (*hookRow)(nil)
	_ quark.AfterFindHook    = (*hookRow)(nil)
)

func probeModelHooks(t *testing.T, e *env) verdict {
	hookLog.Lock()
	hookLog.fired, hookLog.abort = map[string]int{}, false
	hookLog.Unlock()

	c, _ := e.open(t, "sqlite", "con_hooks")
	if err := c.Migrate(e.ctx, &hookRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := hookRow{Name: "a"}
	if err := quark.For[hookRow](e.ctx, c).Create(&r); err != nil {
		t.Fatalf("create: %v", err)
	}
	r.Name = "b"
	if _, err := quark.For[hookRow](e.ctx, c).Update(&r); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := quark.For[hookRow](e.ctx, c).List(); err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := quark.For[hookRow](e.ctx, c).Delete(&r); err != nil {
		t.Fatalf("delete: %v", err)
	}

	hookLog.Lock()
	hookLog.abort = true
	hookLog.Unlock()
	abortErr := quark.For[hookRow](e.ctx, c).Create(&hookRow{Name: "refused"})
	hookLog.Lock()
	hookLog.abort = false
	fired := hookLog.fired
	hookLog.Unlock()
	left, err := quark.For[hookRow](e.ctx, c).Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	missing := 0
	for _, h := range []string{"BeforeCreate", "AfterCreate", "BeforeUpdate", "AfterUpdate",
		"BeforeDelete", "AfterDelete", "BeforeFind", "AfterFind"} {
		if fired[h] == 0 {
			t.Logf("%s never fired", h)
			missing++
		}
	}
	aborts := errors.Is(abortErr, errHookAbort) && left == 0
	t.Logf("hooks fired: %v; a BeforeCreate error aborts the insert: %v (error %v, rows left %d)", fired, aborts, abortErr, left)
	switch {
	case missing == 0 && aborts:
		return present
	case missing < 8:
		return partial
	default:
		return absent
	}
}

// --- CON-04 -------------------------------------------------------------------

// seeingMiddleware records the statements that pass through it, and the
// nesting order of the chain on the first statement.
type seeingMiddleware struct {
	quark.BaseMiddleware
	name  string
	mu    *sync.Mutex
	seen  *[]string
	order *[]string
}

func (m seeingMiddleware) note(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	*m.seen = append(*m.seen, s)
	*m.order = append(*m.order, m.name)
}

func (m seeingMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, ex quark.Executor, s string, args []any) (sql.Result, error) {
		m.note(s)
		return next(ctx, ex, s, args)
	}
}

func (m seeingMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, ex quark.Executor, s string, args []any) (*sql.Rows, error) {
		m.note(s)
		return next(ctx, ex, s, args)
	}
}

func (m seeingMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, ex quark.Executor, s string, args []any) *sql.Row {
		m.note(s)
		return next(ctx, ex, s, args)
	}
}

type obsRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name" quark:"unique"`
}

func (obsRow) TableName() string { return "obs_rows" }

type obsRowV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name" quark:"unique"`
	Extra string `db:"extra"`
}

func (obsRowV2) TableName() string { return "obs_rows" }

// observationBattery is what an application does in its first week: migrate,
// write, read, transact, plan a schema change, apply it.
func observationBattery(ctx context.Context, t *testing.T, c *quark.Client) {
	t.Helper()
	must := func(what string, err error) {
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	must("migrate", c.Migrate(ctx, &obsRow{}))
	r := obsRow{Name: "a"}
	must("create", quark.For[obsRow](ctx, c).Create(&r))
	_, err := quark.For[obsRow](ctx, c).Find(r.ID)
	must("find", err)
	_, err = quark.For[obsRow](ctx, c).Where("name", "=", "a").List()
	must("list", err)
	_, err = quark.For[obsRow](ctx, c).Count()
	must("count", err)
	r.Name = "b"
	_, err = quark.For[obsRow](ctx, c).Update(&r)
	must("update", err)
	must("upsert", quark.For[obsRow](ctx, c).Upsert(&obsRow{Name: "b"}, []string{"name"}, []string{"name"}))
	must("batch", quark.For[obsRow](ctx, c).CreateBatch([]*obsRow{{Name: "c"}, {Name: "d"}}))
	must("tx", c.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[obsRow](ctx, tx).Create(&obsRow{Name: "e"}); err != nil {
			return err
		}
		if err := tx.Savepoint("sp"); err != nil {
			return err
		}
		if err := quark.ForTx[obsRow](ctx, tx).Create(&obsRow{Name: "f"}); err != nil {
			return err
		}
		return tx.RollbackTo("sp")
	}))
	must("raw exec", c.Exec(ctx, `UPDATE "obs_rows" SET "name" = ? WHERE "name" = ?`, "g", "e"))
	rows, err := c.RawQuery(ctx, `SELECT "id" FROM "obs_rows" WHERE "id" > ?`, 0)
	must("raw query", err)
	_ = rows.Close()
	_, err = c.PlanMigration(ctx, &obsRowV2{})
	must("plan", err)
	must("sync", c.Sync(ctx, quark.SyncOptions{}, &obsRowV2{}))
	_, err = quark.For[obsRow](ctx, c).Delete(&r)
	must("delete", err)
}

// multisetMinus returns the elements of a not matched one-for-one in b.
func multisetMinus(a, b []string) []string {
	left := map[string]int{}
	for _, s := range b {
		left[s]++
	}
	var out []string
	for _, s := range a {
		if left[s] > 0 {
			left[s]--
			continue
		}
		out = append(out, s)
	}
	return out
}

// firstWords reduces statements to their first two words, so a note can say
// WHAT was missed without quoting SQL.
func firstWords(stmts []string) []string {
	set := map[string]bool{}
	for _, s := range stmts {
		f := strings.Fields(s)
		if len(f) > 2 {
			f = f[:2]
		}
		set[strings.Join(f, " ")] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func probeInterception(t *testing.T, e *env) verdict {
	registerWireDriver()
	var mu sync.Mutex
	var seenA, seenB, order []string
	a := seeingMiddleware{name: "first", mu: &mu, seen: &seenA, order: &order}
	b := seeingMiddleware{name: "second", mu: &mu, seen: &seenB, order: &order}
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true

	c, rec := e.open(t, wireDriverName, "con_interception",
		quark.WithDialect(quark.SQLite()), quark.WithLimits(limits),
		quark.WithMiddleware(a), quark.WithMiddleware(b))
	stop := wireCapture()
	rec.reset()
	observationBattery(e.ctx, t, c)
	wire := stop()

	mu.Lock()
	defer mu.Unlock()
	observed := rec.sql()
	missedByMiddleware := multisetMinus(wire, seenA)
	missedByObserver := multisetMinus(wire, observed)
	bothChains := len(multisetMinus(seenA, seenB)) == 0 && len(multisetMinus(seenB, seenA)) == 0
	firstOutermost := len(order) >= 2 && order[0] == "first" && order[1] == "second"

	t.Logf("statements that reached the engine: %d; through the middleware: %d; through the observer: %d",
		len(wire), len(seenA), len(observed))
	t.Logf("reached the engine without passing the middleware: %d %v", len(missedByMiddleware), firstWords(missedByMiddleware))
	t.Logf("reached the engine without reaching the observer: %d %v", len(missedByObserver), firstWords(missedByObserver))
	t.Logf("both middlewares saw the same statements: %v; the first registered is the outermost: %v", bothChains, firstOutermost)

	if len(wire) == 0 || len(seenA) == 0 {
		t.Fatal("nothing recorded: the recording driver or the middleware is not wired, and the probe would measure that")
	}
	if len(missedByMiddleware) == 0 && len(missedByObserver) == 0 && bothChains && firstOutermost {
		return present
	}
	// The partial is recorded down to what each extension point misses: a
	// path that starts or stops reaching one of them changes the note.
	want := interceptionRecorded
	if len(missedByMiddleware) != want.middlewareMisses || len(missedByObserver) != want.observerMisses ||
		!slicesEqual(firstWords(missedByMiddleware), want.middlewareClasses) ||
		!slicesEqual(firstWords(missedByObserver), want.observerClasses) {
		t.Errorf("what the middleware and the observer miss moved:\n  measured middleware %d %q\n           observer   %d %q\n  recorded middleware %d %q\n           observer   %d %q\nupdate interceptionRecorded and CON-04's note together",
			len(missedByMiddleware), firstWords(missedByMiddleware), len(missedByObserver), firstWords(missedByObserver),
			want.middlewareMisses, want.middlewareClasses, want.observerMisses, want.observerClasses)
	}
	if len(missedByMiddleware) == len(wire) {
		return absent
	}
	return partial
}

func slicesEqual(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// interceptionRecorded is what CON-04 records the middleware and the
// observer missing on the battery: how many statements, and of which kinds
// (the first two words of each).
var interceptionRecorded = struct {
	middlewareMisses, observerMisses   int
	middlewareClasses, observerClasses []string
}{
	middlewareMisses: 13,
	middlewareClasses: []string{
		"ALTER TABLE", "CREATE TABLE",
		`PRAGMA foreign_key_list("obs_rows")`, `PRAGMA index_info("sqlite_autoindex_obs_rows_1")`,
		`PRAGMA index_list("obs_rows")`, `PRAGMA table_info("obs_rows")`, `PRAGMA table_info(obs_rows)`,
		"ROLLBACK TO", `SAVEPOINT "sp"`,
		`SELECT "id"`,       // client.RawQuery
		"SELECT name",       // the sqlite_master listing of introspection
		`UPDATE "obs_rows"`, // client.Exec
	},
	observerMisses: 12,
	observerClasses: []string{
		"ALTER TABLE", "CREATE TABLE",
		"INSERT INTO", // CreateBatch
		`PRAGMA foreign_key_list("obs_rows")`, `PRAGMA index_info("sqlite_autoindex_obs_rows_1")`,
		`PRAGMA index_list("obs_rows")`, `PRAGMA table_info("obs_rows")`, `PRAGMA table_info(obs_rows)`,
		"ROLLBACK TO", `SAVEPOINT "sp"`,
		"SELECT name",
	},
}

// --- CON-05 -------------------------------------------------------------------

type mapStore struct {
	mu          sync.Mutex
	data        map[string][]byte
	gets, sets  int
	hits        int
	invalidated []string
}

func (s *mapStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	v, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	s.hits++
	return v, nil
}

func (s *mapStore) Set(_ context.Context, key string, val []byte, _ time.Duration, _ ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	s.data[key] = val
	return nil
}

func (s *mapStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *mapStore) InvalidateTags(_ context.Context, tags ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidated = append(s.invalidated, tags...)
	s.data = map[string][]byte{}
	return nil
}

type busRecorder struct {
	mu     sync.Mutex
	events []string
}

func (b *busRecorder) Publish(_ context.Context, ev quark.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, ev.Kind()+":"+ev.Table())
	return nil
}

func (b *busRecorder) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

type storeRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (storeRow) TableName() string { return "store_rows" }

func probeStoresAndBus(t *testing.T, e *env) verdict {
	store := &mapStore{data: map[string][]byte{}}
	c, rec := e.open(t, "sqlite", "con_stores", quark.WithCacheStore(store))
	if err := c.Migrate(e.ctx, &storeRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := quark.For[storeRow](e.ctx, c).Create(&storeRow{Name: "a"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The store serves the second read, and a write invalidates the table.
	rec.reset()
	first, err1 := quark.For[storeRow](e.ctx, c).Cache(time.Minute).List()
	afterFirst := len(rec.sql())
	second, err2 := quark.For[storeRow](e.ctx, c).Cache(time.Minute).List()
	afterSecond := len(rec.sql())
	if err1 != nil || err2 != nil {
		t.Fatalf("cached reads: %v / %v", err1, err2)
	}
	served := store.sets > 0 && store.hits > 0 && afterSecond == afterFirst && len(first) == len(second)
	if err := quark.For[storeRow](e.ctx, c).Create(&storeRow{Name: "b"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	invalidated := false
	for _, tag := range store.invalidated {
		if tag == "store_rows" {
			invalidated = true
		}
	}
	t.Logf("cache store: sets=%d gets=%d hits=%d, statements for the second read=%d, invalidated on write: %v",
		store.sets, store.gets, store.hits, afterSecond-afterFirst, invalidated)

	// The bus hears committed writes, after the commit, and not rolled-back ones.
	bus := &busRecorder{}
	c.UseEventBus(bus)
	if err := quark.For[storeRow](e.ctx, c).Create(&storeRow{Name: "c"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	direct := bus.count() == 1
	rolledBack := errors.New("roll back")
	_ = c.Tx(e.ctx, func(tx *quark.Tx) error {
		_ = quark.ForTx[storeRow](e.ctx, tx).Create(&storeRow{Name: "d"})
		return rolledBack
	})
	silentOnRollback := bus.count() == 1
	var duringTx int
	if err := c.Tx(e.ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[storeRow](e.ctx, tx).Create(&storeRow{Name: "e"}); err != nil {
			return err
		}
		duringTx = bus.count()
		return nil
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}
	afterCommit := duringTx == 1 && bus.count() == 2
	t.Logf("event bus: %v; published on a plain write: %v, silent on rollback: %v, held until commit: %v",
		bus.events, direct, silentOnRollback, afterCommit)

	if served && invalidated && direct && silentOnRollback && afterCommit {
		return present
	}
	if served || direct {
		return partial
	}
	return absent
}

// --- CON-06 -------------------------------------------------------------------

// extCents is a type quark has never heard of.
type extCents int64

type centsRow struct {
	ID    int64    `db:"id" pk:"true"`
	Price extCents `db:"price"`
}

func (centsRow) TableName() string { return "cents_rows" }

var centsMapperOnce sync.Once
var centsMapperSaw sync.Map // dialect name → true

func probeTypeMapper(t *testing.T, e *env) verdict {
	centsMapperOnce.Do(func() {
		quark.RegisterTypeMapper(reflect.TypeOf(extCents(0)), func(dialect string, _ quark.TypeOptions) string {
			centsMapperSaw.Store(dialect, true)
			return "CENTSINT"
		})
	})
	c, _ := e.open(t, "sqlite", "con_typemapper")
	if err := c.Migrate(e.ctx, &centsRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	schema, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	declared := ""
	for _, tbl := range schema.Tables {
		if tbl.Name == "cents_rows" {
			for _, col := range tbl.Columns {
				if col.Name == "price" {
					declared = col.Type
				}
			}
		}
	}
	if err := quark.For[centsRow](e.ctx, c).Create(&centsRow{Price: 1999}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := quark.For[centsRow](e.ctx, c).First()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	_, sawSQLite := centsMapperSaw.Load("sqlite")
	t.Logf("column type after Migrate: %q; the mapper was handed the dialect name: %v; value round-trips: %v",
		declared, sawSQLite, got.Price == 1999)
	switch {
	case strings.EqualFold(declared, "CENTSINT") && sawSQLite && got.Price == 1999:
		return present
	case strings.EqualFold(declared, "CENTSINT"):
		return partial
	default:
		return absent
	}
}

// --- CON-07 -------------------------------------------------------------------

// implicitConvention is a method Quark calls on a caller's type by asserting
// against an interface no third party can name.
type implicitConvention struct {
	method string // as a third party writes it
	sig    func(*types.Func) bool
	fires  func(t *testing.T, e *env) bool
}

type namedTableRow struct {
	ID int64 `db:"id" pk:"true"`
}

func (namedTableRow) TableName() string { return "ext_named_table" }

var errInvalidRow = errors.New("extbench: name is required")

type validatedRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (validatedRow) TableName() string { return "validated_rows" }

func (v *validatedRow) Validate(context.Context) error {
	if v.Name == "" {
		return errInvalidRow
	}
	return nil
}

type stateErr struct{ code string }

func (s stateErr) Error() string    { return "SQLSTATE " + s.code }
func (s stateErr) SQLState() string { return s.code }

func sigIs(params, results []string) func(*types.Func) bool {
	return func(f *types.Func) bool {
		sig := f.Type().(*types.Signature)
		if sig.Params().Len() != len(params) || sig.Results().Len() != len(results) {
			return false
		}
		for i, p := range params {
			if sig.Params().At(i).Type().String() != p {
				return false
			}
		}
		for i, r := range results {
			if sig.Results().At(i).Type().String() != r {
				return false
			}
		}
		return true
	}
}

func implicitConventions() []implicitConvention {
	return []implicitConvention{
		{
			method: "TableName() string",
			sig:    sigIs(nil, []string{"string"}),
			fires: func(t *testing.T, e *env) bool {
				c, _ := e.open(t, "sqlite", "con_tablename")
				if err := c.Migrate(e.ctx, &namedTableRow{}); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				var n int
				err := c.Raw().QueryRowContext(e.ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='ext_named_table'`).Scan(&n)
				return err == nil && n == 1
			},
		},
		{
			method: "Validate(context.Context) error",
			sig:    sigIs([]string{"context.Context"}, []string{"error"}),
			fires: func(t *testing.T, e *env) bool {
				c, _ := e.open(t, "sqlite", "con_validate")
				if err := c.Migrate(e.ctx, &validatedRow{}); err != nil {
					t.Fatalf("migrate: %v", err)
				}
				err := quark.For[validatedRow](e.ctx, c).Create(&validatedRow{})
				n, _ := quark.For[validatedRow](e.ctx, c).Count()
				return errors.Is(err, errInvalidRow) && n == 0
			},
		},
		{
			method: "SQLState() string",
			sig:    sigIs(nil, []string{"string"}),
			fires: func(t *testing.T, e *env) bool {
				return quark.IsUniqueViolation(fmt.Errorf("insert: %w", stateErr{"23505"})) &&
					quark.IsDeadlock(stateErr{"40P01"})
			},
		},
	}
}

// exportedInterfaceWith reports whether quark or quarkdriver exports an
// interface whose method set is exactly the one method named.
func exportedInterfaceWith(a *apiTypes, method string, sig func(*types.Func) bool) string {
	name := strings.SplitN(method, "(", 2)[0]
	for _, path := range []string{rootPkg, driverPkg} {
		p := a.pkgs[path]
		for _, n := range p.Scope().Names() {
			tn, ok := p.Scope().Lookup(n).(*types.TypeName)
			if !ok || !tn.Exported() {
				continue
			}
			iface, ok := tn.Type().Underlying().(*types.Interface)
			if !ok || iface.NumMethods() != 1 {
				continue
			}
			if m := iface.Method(0); m.Name() == name && sig(m) {
				return p.Name() + "." + n
			}
		}
	}
	return ""
}

func probeImplicitConventions(t *testing.T, e *env) verdict {
	api := e.loadAPI(t)
	var unnamed, named, silent []string
	for _, c := range implicitConventions() {
		if !c.fires(t, e) {
			silent = append(silent, c.method)
			continue
		}
		if iface := exportedInterfaceWith(api, c.method, c.sig); iface != "" {
			named = append(named, c.method+" → "+iface)
		} else {
			unnamed = append(unnamed, c.method)
		}
	}
	t.Logf("called by Quark with no exported interface: %v; with one: %v; not called: %v", unnamed, named, silent)
	switch {
	case len(silent) > 0:
		t.Errorf("a convention Quark used to honour no longer fires: %v — that is a regression, not a gap", silent)
		return absent
	case len(unnamed) == 0:
		return present
	default:
		return partial
	}
}
