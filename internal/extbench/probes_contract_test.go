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

// censusAliases maps every exported alias in package quark of a type
// declared in quarkdriver ("quark.Dialect") to the qualified name the census
// counts it under ("quarkdriver.Dialect"). implementable counts the type
// once, where it is declared; the alias is the name an application writes.
func censusAliases(a *apiTypes) map[string]string {
	out := map[string]string{}
	p := a.pkgs[rootPkg]
	for _, name := range p.Scope().Names() {
		tn, ok := p.Scope().Lookup(name).(*types.TypeName)
		if !ok || !tn.Exported() || !tn.IsAlias() {
			continue
		}
		if named, ok := types.Unalias(tn.Type()).(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == driverPkg {
			out["quark."+name] = "quarkdriver." + named.Obj().Name()
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

// stabilityRow is one row of a markdown table with a "stability" column: the
// symbol its first cell names (the first backticked name in it), and the
// values of its stability and extension-point columns, lower-cased and
// stripped of markup.
type stabilityRow struct {
	symbol    string
	stability string
	extension string
}

// stabilityTables returns, for one page, every markdown table whose header
// has a "stability" column, as its rows. A table may also carry a column
// whose header starts with "extension"; a row's extension value is its first
// word.
func stabilityTables(page string) [][]stabilityRow {
	var tables [][]stabilityRow
	var current []stabilityRow
	inTable, stabCol, extCol := false, -1, -1
	flush := func() {
		if inTable {
			tables = append(tables, current)
		}
		current, inTable, stabCol, extCol = nil, false, -1, -1
	}
	clean := func(cell string) string {
		cell = strings.NewReplacer("*", "", "`", "", "_", " ").Replace(strings.TrimSpace(cell))
		return strings.ToLower(strings.TrimSpace(cell))
	}
	header := false
	for _, line := range strings.Split(page, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			flush()
			header = false
			continue
		}
		cells := strings.Split(strings.Trim(trimmed, "|"), "|")
		if !inTable && !header {
			header = true
			for i, c := range cells {
				switch h := clean(c); {
				case h == "stability":
					stabCol = i
				case strings.HasPrefix(h, "extension"):
					extCol = i
				}
			}
			if stabCol >= 0 {
				inTable = true
			}
			continue
		}
		if !inTable || strings.Trim(trimmed, "|-: ") == "" {
			continue // a table without a stability column, or the separator row
		}
		m := backticked.FindStringSubmatch(cells[0])
		if m == nil {
			continue
		}
		row := stabilityRow{symbol: m[1]}
		if stabCol < len(cells) {
			row.stability = clean(cells[stabCol])
		}
		if extCol >= 0 && extCol < len(cells) {
			if f := strings.Fields(clean(cells[extCol])); len(f) > 0 {
				row.extension = strings.Trim(f[0], ".,;:—-")
			}
		}
		current = append(current, row)
	}
	flush()
	return tables
}

// The values a contract row may carry. What each one promises is the page's
// to say (website/docs/reference/extension-contract.mdx); the probe holds the
// vocabulary fixed, so a row cannot invent a fourth stability nobody defined.
var (
	stabilityValues = map[string]bool{"stable": true, "experimental": true, "internal-use": true}
	extensionValues = map[string]bool{"yes": true, "no": true}
)

func probeContractPage(t *testing.T, e *env) verdict {
	api := e.loadAPI(t)
	census := implementable(api)
	if len(census) == 0 {
		t.Fatal("the census of implementable types is empty: the probe is broken, not the contract")
	}
	known := map[string]censusEntry{}
	for _, c := range census {
		known[c.name] = c
		known[c.qualified()] = c
	}
	// A page may name a type by its alias in package quark — quark.Dialect
	// for quarkdriver.Dialect since A11 Q3, the name acceptance/apisurface.json
	// records as alias_of — and that is the same type: a row naming it
	// declares the census entry, it does not dangle.
	for alias, target := range censusAliases(api) {
		if c, ok := known[target]; ok {
			known[alias] = c
		}
	}

	// The contract is ONE table: the tables with a stability column that
	// name at least one census type. Anything else with a stability column
	// (a CLI page, a feature matrix) is not about these types.
	type contractTable struct {
		page string
		rows []stabilityRow
	}
	var tables []contractTable
	bestPage, bestMentions := "", 0
	pages := docPages(t, e)
	paths := make([]string, 0, len(pages))
	for path := range pages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		page := pages[path]
		for _, rows := range stabilityTables(page) {
			for _, r := range rows {
				if _, ok := known[r.symbol]; ok {
					tables = append(tables, contractTable{path, rows})
					break
				}
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

	declared := map[string]int{} // census entry → rows declaring it
	var dangling, badValue []string
	for _, tb := range tables {
		for _, r := range tb.rows {
			c, ok := known[r.symbol]
			if !ok {
				dangling = append(dangling, r.symbol)
				continue
			}
			declared[c.qualified()]++
			if !stabilityValues[r.stability] || !extensionValues[r.extension] {
				badValue = append(badValue, fmt.Sprintf("%s (stability %q, extension point %q)", r.symbol, r.stability, r.extension))
			}
		}
	}
	var missing, twice []string
	for _, c := range census {
		switch declared[c.qualified()] {
		case 0:
			missing = append(missing, c.qualified())
		case 1:
		default:
			twice = append(twice, c.qualified())
		}
	}

	kinds := map[string]int{}
	for _, c := range census {
		kinds[c.kind]++
	}
	t.Logf("census: %d implementable types (%d interfaces, %d function types, %d records of functions)",
		len(census), kinds["interface"], kinds["func"], kinds["record"])
	for _, tb := range tables {
		t.Logf("contract table on %s: %d rows", tb.page, len(tb.rows))
	}
	t.Logf("declared: %d of %d; not declared: %v; declared twice: %v; rows naming nothing in the census: %v; rows with a value outside the vocabulary: %v",
		len(declared), len(census), missing, twice, dangling, badValue)
	t.Logf("the page that names the most of them: %s, with %d of %d", bestPage, bestMentions, len(census))

	switch {
	case len(tables) == 1 && len(missing) == 0 && len(twice) == 0 && len(dangling) == 0 && len(badValue) == 0:
		return present
	case len(declared) > 0:
		return partial
	default:
		return absent
	}
}

// --- CON-02 -------------------------------------------------------------------
//
// The freeze is acceptance/apisurface.json, which CI regenerates and diffs on
// every pull request. Since A11 Q6 every func, method and type in it carries
// a "sig", so the question is no longer whether a member is NAMED there but
// whether the file records the shape the compiler sees for every member a
// third-party implementation depends on: then any change to one — a
// parameter's type, a result added, a predicate added to a record of
// functions — moves the file, and the freshness check fails with the diff.
//
// The probe renders the expected shape from the compiler's export data by
// the generator's rules (acceptance/cmd/gen-apisurface: qualifier, funcSig,
// typeSig). The generator lives in a module this one cannot import, so the
// rules are written twice; a difference between the two renderings shows up
// here as a member whose recorded sig is not the compiler's, with both
// printed.

const surfaceModule = "github.com/jcsvwinston/quark"

func surfaceQualifier(self string) types.Qualifier {
	return func(p *types.Package) string {
		switch path := p.Path(); {
		case path == self:
			return ""
		case path == surfaceModule:
			return "quark"
		case strings.HasPrefix(path, surfaceModule+"/"):
			return strings.TrimPrefix(path, surfaceModule+"/")
		default:
			return p.Name()
		}
	}
}

func surfaceTuple(t *types.Tuple, variadic bool, q types.Qualifier) []string {
	out := make([]string, 0, t.Len())
	for i := 0; i < t.Len(); i++ {
		typ := t.At(i).Type()
		if sl, ok := typ.(*types.Slice); ok && variadic && i == t.Len()-1 {
			out = append(out, "..."+types.TypeString(sl.Elem(), q))
			continue
		}
		out = append(out, types.TypeString(typ, q))
	}
	return out
}

func surfaceSigBody(sig *types.Signature, q types.Qualifier) string {
	s := "(" + strings.Join(surfaceTuple(sig.Params(), sig.Variadic(), q), ", ") + ")"
	switch res := surfaceTuple(sig.Results(), false, q); len(res) {
	case 0:
	case 1:
		s += " " + res[0]
	default:
		s += " (" + strings.Join(res, ", ") + ")"
	}
	return s
}

func surfaceTypeParams(tps *types.TypeParamList, q types.Qualifier) string {
	if tps == nil || tps.Len() == 0 {
		return ""
	}
	parts := make([]string, tps.Len())
	for i := 0; i < tps.Len(); i++ {
		parts[i] = tps.At(i).Obj().Name() + " " + types.TypeString(tps.At(i).Constraint(), q)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func surfaceFuncSig(sig *types.Signature, q types.Qualifier) string {
	return "func" + surfaceTypeParams(sig.TypeParams(), q) + surfaceSigBody(sig, q)
}

// frozenShape returns, for one census entry, the symbols of apisurface.json
// that fix what an implementation depends on, each with the sig the compiler
// says it should carry: an interface's methods (or, for an alias of an
// internal interface, the alias's own entry, which spells them out), a
// function type's signature, a record's fields.
func frozenShape(a *apiTypes, c censusEntry) map[string]string {
	tn := a.lookup(c.pkg, c.name).(*types.TypeName)
	q := surfaceQualifier(c.pkg)
	prefix := ""
	if tn.IsAlias() {
		prefix = "= "
	}
	tparams := ""
	if named, ok := tn.Type().(*types.Named); ok && named.TypeParams().Len() > 0 {
		tparams = surfaceTypeParams(named.TypeParams(), q) + " "
	}
	out := map[string]string{}
	switch u := tn.Type().Underlying().(type) {
	case *types.Interface:
		var methods []string
		for i := 0; i < u.NumMethods(); i++ {
			m := u.Method(i)
			sig := m.Type().(*types.Signature)
			if tn.IsAlias() {
				methods = append(methods, m.Name()+surfaceSigBody(sig, q))
			} else {
				out[fmt.Sprintf("(%s).%s", c.name, m.Name())] = surfaceFuncSig(sig, q)
			}
		}
		if tn.IsAlias() {
			out[c.name] = "= interface{" + strings.Join(methods, "; ") + "}"
		}
	case *types.Signature:
		out[c.name] = tparams + prefix + surfaceFuncSig(u, q)
	case *types.Struct:
		var fields []string
		for i := 0; i < u.NumFields(); i++ {
			if f := u.Field(i); f.Exported() {
				fields = append(fields, f.Name()+" "+types.TypeString(f.Type(), q))
			}
		}
		out[c.name] = tparams + prefix + "struct{" + strings.Join(fields, "; ") + "}"
	}
	return out
}

func probeSurfaceFreeze(t *testing.T, e *env) verdict {
	api := e.loadAPI(t)
	census := implementable(api)
	raw, err := os.ReadFile(filepath.Join(e.root, "acceptance", "apisurface.json"))
	if err != nil {
		t.Fatalf("read the frozen surface: %v", err)
	}
	var surface struct {
		Symbols []struct {
			Pkg  string `json:"pkg"`
			Name string `json:"name"`
			Sig  string `json:"sig"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &surface); err != nil {
		t.Fatalf("parse apisurface.json: %v", err)
	}
	recorded := map[string]string{} // "pkg name" → sig
	for _, s := range surface.Symbols {
		recorded[s.Pkg+" "+s.Name] = s.Sig
	}

	var total, named, frozen int
	var notNamed, drifted []string
	for _, c := range census {
		shape := frozenShape(api, c)
		keys := make([]string, 0, len(shape))
		for k := range shape {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			total++
			got, ok := recorded[c.pkg+" "+k]
			switch {
			case !ok:
				notNamed = append(notNamed, c.qual+"."+k)
			case got != shape[k]:
				named++
				drifted = append(drifted, fmt.Sprintf("%s.%s: the file records %q, the compiler has %q", c.qual, k, got, shape[k]))
			default:
				named++
				frozen++
			}
		}
	}
	t.Logf("symbols that fix what a third-party implementation depends on: %d; in apisurface.json by name: %d; with the compiler's signature: %d", total, named, frozen)
	if len(notNamed) > 0 {
		t.Logf("not in the file: %v", notNamed)
	}
	for i, d := range drifted {
		if i == 10 {
			t.Logf("… and %d more", len(drifted)-10)
			break
		}
		t.Logf("drifted: %s", d)
	}

	switch {
	case total > 0 && frozen == total:
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

// seeingMiddleware records the statements that pass through it, the kind
// Quark says each is (quark.StatementKindFromContext), and the nesting order
// of the chain on the first statement.
type seeingMiddleware struct {
	quark.BaseMiddleware
	name  string
	mu    *sync.Mutex
	seen  *[]string
	kinds *[]quark.StatementKind
	order *[]string
}

func (m seeingMiddleware) note(ctx context.Context, s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	*m.seen = append(*m.seen, s)
	if m.kinds != nil {
		*m.kinds = append(*m.kinds, quark.StatementKindFromContext(ctx))
	}
	*m.order = append(*m.order, m.name)
}

func (m seeingMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, ex quark.Executor, s string, args []any) (sql.Result, error) {
		m.note(ctx, s)
		return next(ctx, ex, s, args)
	}
}

func (m seeingMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, ex quark.Executor, s string, args []any) (*sql.Rows, error) {
		m.note(ctx, s)
		return next(ctx, ex, s, args)
	}
}

func (m seeingMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, ex quark.Executor, s string, args []any) *sql.Row {
		m.note(ctx, s)
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

// allKinds are the statement kinds the battery has to produce, each at least
// once: a battery that stops exercising one would stop measuring it.
var allKinds = []quark.StatementKind{
	quark.StatementQuery, quark.StatementExec, quark.StatementDDL,
	quark.StatementIntrospection, quark.StatementSavepoint, quark.StatementRaw,
}

func probeInterception(t *testing.T, e *env) verdict {
	registerWireDriver()
	var mu sync.Mutex
	var seenA, seenB, order []string
	var kindsA []quark.StatementKind
	a := seeingMiddleware{name: "first", mu: &mu, seen: &seenA, kinds: &kindsA, order: &order}
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
	observedKinds := rec.kinds()
	missedByMiddleware := multisetMinus(wire, seenA)
	missedByObserver := multisetMinus(wire, observed)
	bothChains := slicesEqual(seenA, seenB)
	firstOutermost := len(order) >= 2 && order[0] == "first" && order[1] == "second"

	t.Logf("statements that reached the engine: %d; through the middleware: %d; through the observer: %d",
		len(wire), len(seenA), len(observed))
	t.Logf("reached the engine without passing the middleware: %d %v", len(missedByMiddleware), firstWords(missedByMiddleware))
	t.Logf("reached the engine without reaching the observer: %d %v", len(missedByObserver), firstWords(missedByObserver))
	t.Logf("both middlewares saw the same statements in the same order: %v; the first registered is the outermost: %v", bothChains, firstOutermost)

	if len(wire) == 0 || len(seenA) == 0 {
		t.Fatal("nothing recorded: the recording driver or the middleware is not wired, and the probe would measure that")
	}

	// In order: what the middleware saw, and what the observer saw, is the
	// engine's own sequence, statement for statement.
	inOrder := true
	if i, ok := firstDivergence(wire, seenA); !ok {
		inOrder = false
		t.Logf("the middleware's sequence leaves the engine's at statement %d: engine %q, middleware %q", i, at(wire, i), at(seenA, i))
	}
	if i, ok := firstDivergence(wire, observed); !ok {
		inOrder = false
		t.Logf("the observer's sequence leaves the engine's at statement %d: engine %q, observer %q", i, at(wire, i), at(observed, i))
	}

	// The kinds: the middleware is told the kind the observer's event
	// carries, every statement has one, and the battery produces all six.
	kindsAgree := len(kindsA) == len(observedKinds)
	for i := 0; kindsAgree && i < len(kindsA); i++ {
		if kindsA[i] != observedKinds[i] || kindsA[i] == "" {
			kindsAgree = false
			t.Logf("statement %d %q: the middleware is told kind %q, the observer's event says %q", i, at(seenA, i), kindsA[i], observedKinds[i])
		}
	}
	count := map[quark.StatementKind]int{}
	for _, k := range observedKinds {
		count[k]++
	}
	var missingKinds []string
	for _, k := range allKinds {
		if count[k] == 0 {
			missingKinds = append(missingKinds, string(k))
		}
	}
	t.Logf("statements by kind: %v; the middleware and the observer agree on every kind: %v", count, kindsAgree)
	if len(missingKinds) > 0 {
		t.Logf("kinds the battery produced none of: %v", missingKinds)
	}

	if inOrder && bothChains && firstOutermost && kindsAgree && len(missingKinds) == 0 {
		return present
	}
	if len(missedByMiddleware) == len(wire) {
		return absent
	}
	return partial
}

// firstDivergence returns the index of the first statement where got leaves
// want, and whether there is none (the sequences are equal).
func firstDivergence(want, got []string) (int, bool) {
	for i := 0; i < len(want) || i < len(got); i++ {
		if i >= len(want) || i >= len(got) || want[i] != got[i] {
			return i, false
		}
	}
	return 0, true
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "(nothing)"
}

func slicesEqual(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
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
