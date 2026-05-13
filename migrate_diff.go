// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"sort"
	"strings"
)

// --- Operation interface + concrete ops --------------------------------------

// Operation is one unit of schema change emitted by [Diff]. It's a
// dialect-neutral plan node: each op carries the identifiers and
// neutral shape needed to reconstruct a single DDL statement, and
// the executor (F3-3-execute, follow-up PR) translates it to
// per-dialect SQL via the existing migrator helpers.
//
// Operation is a sealed interface — the concrete types in this file
// are the only valid implementations. F3-3 deliberately models ops
// as values rather than method calls so the diff stays inspectable
// (the CLI plan command in F3-5 can render each op as text without
// touching SQL) and testable (unit tests assert on op structure,
// not on emitted SQL).
type Operation interface {
	// String returns a stable human-readable description for the
	// plan output and test failure messages. Format is
	// `<VERB> <subject>` — no DDL syntax, since DDL depends on the
	// dialect.
	String() string
	// isOperation is a method on the sealed interface — its only
	// job is to prevent external packages from inventing new
	// Operation types that the executor wouldn't know how to
	// handle. Add new ops to this file, not from outside.
	isOperation()
}

// OpCreateTable is emitted when the desired schema has a table that
// the current schema lacks. The full Table value (columns, indexes,
// FKs, checks) is carried so the executor can emit CREATE TABLE +
// CREATE INDEX + ALTER TABLE ADD CONSTRAINT in the right order.
type OpCreateTable struct {
	Table Table
}

func (o OpCreateTable) String() string { return fmt.Sprintf("CREATE TABLE %s", o.Table.Name) }
func (OpCreateTable) isOperation()     {}

// OpDropTable is emitted when the current schema has a table that
// the desired schema lacks. Destructive; F3-3 doesn't gate on a
// "safe mode" flag — that belongs to the executor / CLI (F3-4 +
// F3-5) which can prompt or refuse to drop without an explicit
// flag.
type OpDropTable struct {
	Table string
}

func (o OpDropTable) String() string { return fmt.Sprintf("DROP TABLE %s", o.Table) }
func (OpDropTable) isOperation()     {}

// OpAddColumn is emitted when the desired schema adds a column to a
// table both sides have. Carries the full [Column] so the executor
// can splice the right `<type> [NULL|NOT NULL] [DEFAULT ...]` into
// the ALTER TABLE ADD COLUMN statement.
type OpAddColumn struct {
	Table  string
	Column Column
}

func (o OpAddColumn) String() string {
	return fmt.Sprintf("ADD COLUMN %s.%s %s", o.Table, o.Column.Name, o.Column.Type)
}
func (OpAddColumn) isOperation() {}

// OpDropColumn is emitted when the current schema has a column the
// desired schema lacks. Destructive — same caveat as OpDropTable.
type OpDropColumn struct {
	Table  string
	Column string
}

func (o OpDropColumn) String() string {
	return fmt.Sprintf("DROP COLUMN %s.%s", o.Table, o.Column)
}
func (OpDropColumn) isOperation() {}

// OpAlterColumn is emitted when both sides have a column with the
// same name but at least one attribute differs (Type, Nullable, or
// Default). The op carries BOTH the old and the new column so the
// executor / CLI can render the delta precisely and so resumable
// migrations (F3-4) can decide whether the alter is safe to retry.
//
// Diff convention: we emit AT MOST ONE OpAlterColumn per (table,
// column). If both Type and Nullable changed, a single op describes
// both deltas; the executor decides whether per-attribute ALTERs
// are needed or a single multi-attribute ALTER is supported.
type OpAlterColumn struct {
	Table string
	Old   Column
	New   Column
}

func (o OpAlterColumn) String() string {
	var parts []string
	if o.Old.Type != o.New.Type {
		parts = append(parts, fmt.Sprintf("type %s→%s", o.Old.Type, o.New.Type))
	}
	if o.Old.Nullable != o.New.Nullable {
		parts = append(parts, fmt.Sprintf("nullable %v→%v", o.Old.Nullable, o.New.Nullable))
	}
	if !stringPtrEqual(o.Old.Default, o.New.Default) {
		parts = append(parts, fmt.Sprintf("default %s→%s", stringPtrPretty(o.Old.Default), stringPtrPretty(o.New.Default)))
	}
	return fmt.Sprintf("ALTER COLUMN %s.%s (%s)", o.Table, o.New.Name, strings.Join(parts, "; "))
}
func (OpAlterColumn) isOperation() {}

// OpCreateIndex / OpDropIndex are emitted by Diff when the index
// list of a table both sides have differs. We match indexes by name
// — there's no fuzzy "same columns, different name" matching in
// this PR. Renames look like DROP + CREATE, which is what the
// engines do anyway. If F3-3-execute later wants to detect renames
// for safety reasons, that's a separate pass.
type OpCreateIndex struct {
	Table string
	Index Index
}

func (o OpCreateIndex) String() string {
	uniq := ""
	if o.Index.Unique {
		uniq = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)", uniq, o.Index.Name, o.Table, strings.Join(o.Index.Columns, ", "))
}
func (OpCreateIndex) isOperation() {}

type OpDropIndex struct {
	Table string
	Index string
}

func (o OpDropIndex) String() string { return fmt.Sprintf("DROP INDEX %s ON %s", o.Index, o.Table) }
func (OpDropIndex) isOperation()     {}

// OpAddForeignKey / OpDropForeignKey — same model as indexes: name
// is the match key. SQLite's "" name for inline FKs (see
// schema.ForeignKey godoc) is handled by Diff matching on
// (Columns, RefTable, RefColumns) when Name is "" on both sides.
type OpAddForeignKey struct {
	Table      string
	ForeignKey ForeignKey
}

func (o OpAddForeignKey) String() string {
	return fmt.Sprintf("ADD FOREIGN KEY %s ON %s (%s) → %s (%s)",
		fkLabel(o.ForeignKey.Name),
		o.Table,
		strings.Join(o.ForeignKey.Columns, ", "),
		o.ForeignKey.RefTable,
		strings.Join(o.ForeignKey.RefColumns, ", "),
	)
}
func (OpAddForeignKey) isOperation() {}

type OpDropForeignKey struct {
	Table      string
	ForeignKey string // catalog name; "" on SQLite inline FKs
}

func (o OpDropForeignKey) String() string {
	return fmt.Sprintf("DROP FOREIGN KEY %s ON %s", fkLabel(o.ForeignKey), o.Table)
}
func (OpDropForeignKey) isOperation() {}

// OpAddCheck / OpDropCheck — emitted only when both schemas
// populate Checks (which means neither side is SQLite — see the
// SQLite Checks=nil contract in Check's godoc). When one side is
// SQLite, Diff skips the check comparison rather than treating
// Checks=nil as "no checks" (which would falsely emit DropCheck
// for every check on the other side).
type OpAddCheck struct {
	Table string
	Check Check
}

func (o OpAddCheck) String() string {
	return fmt.Sprintf("ADD CHECK %s ON %s (%s)", o.Check.Name, o.Table, o.Check.Expression)
}
func (OpAddCheck) isOperation() {}

type OpDropCheck struct {
	Table string
	Check string
}

func (o OpDropCheck) String() string {
	return fmt.Sprintf("DROP CHECK %s ON %s", o.Check, o.Table)
}
func (OpDropCheck) isOperation() {}

// --- Diff algorithm ----------------------------------------------------------

// Diff returns the ordered list of [Operation]s that, applied in
// order, would bring `current` into alignment with `desired`. Both
// arguments are dialect-neutral [Schema] values typically produced
// by [Client.IntrospectSchema] (for `current`) and by a future
// models-to-schema pass (for `desired`, F3-3-plan).
//
// Ordering rules:
//
//  1. Tables present in desired but not in current → OpCreateTable
//     first (so subsequent ops can reference them).
//  2. Per table that both sides have, in this exact order:
//     a) ADD COLUMN then ALTER COLUMN (so the new shape is in
//     place before in-place alters).
//     b) DROP CHECK → DROP FK → DROP INDEX → DROP COLUMN
//     (reverse-dependency order: drop the dependent constraint
//     before the column it references).
//     c) CREATE INDEX after all column changes (add/alter/drop)
//     so new indexes can reference new columns and don't trip
//     over dropped ones.
//     d) ADD FOREIGN KEY after CREATE INDEX (FKs typically
//     require an index on the referencing column).
//     e) ADD CHECK last.
//  3. Tables present in current but not in desired → OpDropTable
//     LAST (so FK references from other dropped tables are already
//     gone).
//
// Diff is pure and deterministic: same input always produces the
// same output, and tables/columns/indexes are sorted by name within
// each step so the plan is reviewable in tests and CLI output.
//
// Diff intentionally does NOT compare:
//
//   - Column.Type strings across dialects (PG "varchar(255)" vs
//     MSSQL "nvarchar(255)" — F3-2 doesn't normalise types, F3-3
//     compares the strings verbatim and emits an OpAlterColumn if
//     they differ. The caller is expected to feed two schemas from
//     the same dialect, OR explicitly accept the alter noise.).
//   - Check.Expression text (each dialect has its own canonical
//     form — see the Check godoc). When both sides have a check by
//     the same name, Diff treats them as equal regardless of
//     expression text. AST-level equivalence is out of scope for
//     this PR.
//   - Checks on a side where Checks=nil (the SQLite contract). When
//     desired.Checks=nil OR current.Checks=nil for a table, the
//     check comparison for that table is skipped entirely.
func Diff(desired, current Schema) []Operation {
	desiredTables := tablesByName(desired)
	currentTables := tablesByName(current)

	var ops []Operation

	// 1. CREATE TABLE for tables only in desired, sorted by name.
	for _, name := range sortedKeys(desiredTables) {
		if _, ok := currentTables[name]; ok {
			continue
		}
		ops = append(ops, OpCreateTable{Table: desiredTables[name]})
	}

	// 2. Per-table column / index / FK / check diffs for tables in both.
	for _, name := range sortedKeys(desiredTables) {
		cur, ok := currentTables[name]
		if !ok {
			continue
		}
		ops = append(ops, diffTable(name, cur, desiredTables[name])...)
	}

	// 3. DROP TABLE for tables only in current, sorted by name —
	//    emitted LAST so any FKs referencing them have already been
	//    dropped in step 2 (when the referring table itself isn't
	//    being dropped). FK chains between two dropped tables are
	//    handled by the executor / driver — Diff doesn't reorder
	//    drops across tables because the dependency DAG is the
	//    caller's responsibility once the surface is this small.
	for _, name := range sortedKeys(currentTables) {
		if _, ok := desiredTables[name]; ok {
			continue
		}
		ops = append(ops, OpDropTable{Table: name})
	}

	return ops
}

// diffTable computes the per-table delta. See [Diff] for ordering
// rules. Inputs are guaranteed by Diff to have the same `Name` —
// the per-column / index / FK / check matching keys off `Name`
// fields inside each.
func diffTable(table string, cur, des Table) []Operation {
	var add, alter, drop []Operation

	// --- Columns
	curCols := columnsByName(cur.Columns)
	desCols := columnsByName(des.Columns)

	for _, n := range sortedKeys(desCols) {
		dc := desCols[n]
		cc, exists := curCols[n]
		if !exists {
			add = append(add, OpAddColumn{Table: table, Column: dc})
			continue
		}
		if !columnsEqual(cc, dc) {
			alter = append(alter, OpAlterColumn{Table: table, Old: cc, New: dc})
		}
	}
	for _, n := range sortedKeys(curCols) {
		if _, ok := desCols[n]; ok {
			continue
		}
		drop = append(drop, OpDropColumn{Table: table, Column: n})
	}

	// --- Indexes (after columns: new indexes may reference new columns)
	var idxAdd, idxDrop []Operation
	curIdx := indexesByName(cur.Indexes)
	desIdx := indexesByName(des.Indexes)
	for _, n := range sortedKeys(desIdx) {
		di := desIdx[n]
		ci, ok := curIdx[n]
		if !ok {
			idxAdd = append(idxAdd, OpCreateIndex{Table: table, Index: di})
			continue
		}
		if !indexesEqual(ci, di) {
			// Index shape changed (columns / unique flag) — model
			// as DROP + CREATE. No engine supports ALTER INDEX
			// to change shape; the rebuild is the only path.
			idxDrop = append(idxDrop, OpDropIndex{Table: table, Index: n})
			idxAdd = append(idxAdd, OpCreateIndex{Table: table, Index: di})
		}
	}
	for _, n := range sortedKeys(curIdx) {
		if _, ok := desIdx[n]; ok {
			continue
		}
		idxDrop = append(idxDrop, OpDropIndex{Table: table, Index: n})
	}

	// --- Foreign keys (after indexes: FKs typically require an index)
	var fkAdd, fkDrop []Operation
	curFKs := foreignKeysByMatchKey(cur.ForeignKeys)
	desFKs := foreignKeysByMatchKey(des.ForeignKeys)
	for _, k := range sortedKeys(desFKs) {
		df := desFKs[k]
		cf, ok := curFKs[k]
		if !ok {
			fkAdd = append(fkAdd, OpAddForeignKey{Table: table, ForeignKey: df})
			continue
		}
		if !foreignKeysEqual(cf, df) {
			fkDrop = append(fkDrop, OpDropForeignKey{Table: table, ForeignKey: cf.Name})
			fkAdd = append(fkAdd, OpAddForeignKey{Table: table, ForeignKey: df})
		}
	}
	for _, k := range sortedKeys(curFKs) {
		if _, ok := desFKs[k]; ok {
			continue
		}
		fkDrop = append(fkDrop, OpDropForeignKey{Table: table, ForeignKey: curFKs[k].Name})
	}

	// --- Checks (last): skip entirely if either side has Checks=nil
	// (SQLite contract — see schema.Check godoc).
	var chkAdd, chkDrop []Operation
	if cur.Checks != nil && des.Checks != nil {
		curChks := checksByName(cur.Checks)
		desChks := checksByName(des.Checks)
		for _, n := range sortedKeys(desChks) {
			if _, ok := curChks[n]; ok {
				continue
			}
			chkAdd = append(chkAdd, OpAddCheck{Table: table, Check: desChks[n]})
		}
		for _, n := range sortedKeys(curChks) {
			if _, ok := desChks[n]; ok {
				continue
			}
			chkDrop = append(chkDrop, OpDropCheck{Table: table, Check: n})
		}
	}

	// Order per the Diff godoc:
	//   add cols → alter cols → drop checks → drop FKs → drop indexes
	//   → drop cols → create indexes → add FKs → add checks
	// Drops are reverse-dependency-order (checks first, columns last)
	// so the dropped-from-table is empty when the column finally goes.
	var ops []Operation
	ops = append(ops, add...)
	ops = append(ops, alter...)
	ops = append(ops, chkDrop...)
	ops = append(ops, fkDrop...)
	ops = append(ops, idxDrop...)
	ops = append(ops, drop...)
	ops = append(ops, idxAdd...)
	ops = append(ops, fkAdd...)
	ops = append(ops, chkAdd...)
	return ops
}

// --- helpers -----------------------------------------------------------------

func tablesByName(s Schema) map[string]Table {
	m := make(map[string]Table, len(s.Tables))
	for _, t := range s.Tables {
		m[t.Name] = t
	}
	return m
}

func columnsByName(cs []Column) map[string]Column {
	m := make(map[string]Column, len(cs))
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

func indexesByName(is []Index) map[string]Index {
	m := make(map[string]Index, len(is))
	for _, i := range is {
		m[i.Name] = i
	}
	return m
}

// checksByName indexes checks by their catalog-given name. NOTE:
// per the Check godoc, Diff matches checks by name only — there's no
// composite-key fallback like foreignKeysByMatchKey does for anonymous
// FKs. In practice every dialect that exposes CHECK constraints
// (PG/MySQL/MariaDB/MSSQL — SQLite returns Checks=nil) assigns a name
// to every constraint (dialect-generated if the user didn't provide
// one). If you somehow construct a Schema with two Check entries both
// named "" — outside the introspector's normal output — the second
// one overwrites the first here; that's undefined behaviour at the
// Diff level. See TestDiff_Checks_EmptyName_IsUndefined.
func checksByName(cs []Check) map[string]Check {
	m := make(map[string]Check, len(cs))
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

// foreignKeysByMatchKey indexes FKs for symmetric matching. When
// Name is non-empty (PG / MySQL / MariaDB / MSSQL), the key IS the
// name. When Name is empty (SQLite inline FKs), we build a composite
// key from (columns, ref_table, ref_columns) so the same FK on both
// sides matches even though both lack a name.
func foreignKeysByMatchKey(fks []ForeignKey) map[string]ForeignKey {
	m := make(map[string]ForeignKey, len(fks))
	for _, fk := range fks {
		k := fk.Name
		if k == "" {
			k = fmt.Sprintf("[%s]→%s[%s]",
				strings.Join(fk.Columns, ","),
				fk.RefTable,
				strings.Join(fk.RefColumns, ","))
		}
		m[k] = fk
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func columnsEqual(a, b Column) bool {
	return a.Name == b.Name &&
		normalizeType(a.Type) == normalizeType(b.Type) &&
		a.Nullable == b.Nullable &&
		stringPtrEqual(a.Default, b.Default)
}

// normalizeType produces a comparable canonical form for a SQL type
// string. Two type strings are treated as equal by [columnsEqual]
// when their normalised forms match — which lets the migrator's
// uppercase canonical (`BIGINT`, `VARCHAR(255)`) compare equal to
// the catalog's lowercase (`bigint`, `varchar(255)`) without
// generating spurious OpAlterColumn ops on every round-trip.
//
// Normalisation steps, in order:
//
//  1. Trim outer whitespace.
//  2. Lowercase. (Migrator emits canonical UPPER; catalogs all
//     return lowercase except SQLite which echoes the DDL verbatim.)
//  3. PG alias: `character varying` → `varchar`. PG's
//     information_schema returns `character varying(N)` for what
//     the migrator emits as `VARCHAR(N)` — they're aliases in PG's
//     grammar; we collapse to `varchar` since that's what every
//     other engine uses.
//  4. PG alias: `character(` → `char(`. Same rationale.
//  5. MySQL display widths: `int(11)` → `int`, `bigint(20)` →
//     `bigint`, etc. Older MySQL versions (5.7) emit display widths
//     in `INFORMATION_SCHEMA.COLUMNS.COLUMN_TYPE`; MySQL 8.0+
//     dropped them but a mixed-version cluster could still surface
//     them. Stripping is safe — the width was always cosmetic for
//     integer types.
//
// Deliberately NOT normalised in this pass:
//
//   - PG `int8`/`int4`/`int2` ↔ `bigint`/`integer`/`smallint`. PG's
//     `information_schema.columns.data_type` returns the SQL-standard
//     names (`bigint` etc.), not the binary aliases, so this case
//     never arises from the introspector. If a user constructs a
//     desired Schema with `int8` manually they'll see drift; that's
//     a tag-driven path and out of scope here.
//   - Whitespace within multi-word types (already handled by
//     `character varying` → `varchar` collapsing the only such
//     case in practice).
//   - MSSQL aliases. `bigint`/`int`/`smallint` are already the
//     canonical names in both directions; no aliasing needed.
//
// Pure function — no state, no IO, safe to call from anywhere.
func normalizeType(t string) string {
	s := strings.ToLower(strings.TrimSpace(t))
	s = strings.ReplaceAll(s, "character varying", "varchar")
	s = strings.ReplaceAll(s, "character(", "char(")
	s = stripMySQLDisplayWidth(s)
	return s
}

// stripMySQLDisplayWidth removes the `(N)` display width from
// integer-family types. Only applies to the family that historically
// carried display widths in MySQL: `int`, `bigint`, `smallint`,
// `tinyint`, `mediumint`. Decimal/varchar widths are NOT touched —
// those carry actual precision/length information.
//
// Matches as a word — `int(11)` becomes `int` but `point(2,2)` is
// left alone (the prefix doesn't end a word). The implementation
// uses a manual scan rather than regexp to keep the function
// allocation-free for the common case.
func stripMySQLDisplayWidth(s string) string {
	kws := []string{"bigint", "mediumint", "smallint", "tinyint", "int"}
	for _, kw := range kws {
		idx := strings.Index(s, kw+"(")
		if idx < 0 {
			continue
		}
		// Require the keyword to start at position 0 or after a
		// non-letter character — so `point(2,2)` (substring `int(`
		// at index 1) doesn't false-match.
		if idx > 0 {
			prev := s[idx-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') || prev == '_' {
				continue
			}
		}
		// Find the matching `)` after the keyword. Variable named
		// `closeIdx` rather than `close` to avoid shadowing the
		// `close` builtin (which staticcheck would flag).
		closeIdx := strings.Index(s[idx:], ")")
		if closeIdx < 0 {
			continue
		}
		// Body between `(` and `)` must be all digits for this to
		// be a display width (not e.g. `int(unsigned)` which doesn't
		// exist in real MySQL but we should be paranoid).
		body := s[idx+len(kw)+1 : idx+closeIdx]
		allDigits := body != ""
		for i := 0; i < len(body); i++ {
			if body[i] < '0' || body[i] > '9' {
				allDigits = false
				break
			}
		}
		if !allDigits {
			continue
		}
		s = s[:idx+len(kw)] + s[idx+closeIdx+1:]
	}
	return s
}

func indexesEqual(a, b Index) bool {
	if a.Unique != b.Unique || len(a.Columns) != len(b.Columns) {
		return false
	}
	for i := range a.Columns {
		if a.Columns[i] != b.Columns[i] {
			return false
		}
	}
	return true
}

func foreignKeysEqual(a, b ForeignKey) bool {
	if a.RefTable != b.RefTable {
		return false
	}
	if !stringSliceEqual(a.Columns, b.Columns) || !stringSliceEqual(a.RefColumns, b.RefColumns) {
		return false
	}
	// On the MySQL/MariaDB asymmetry — RESTRICT vs NO ACTION are
	// semantically equivalent in immediate-check mode (which is the
	// only mode either engine supports). The diff treats them as
	// equal so the catalog labelling divergence documented in the
	// ForeignKey godoc doesn't produce spurious DROP+ADD ops on
	// every introspection round-trip.
	if !fkActionsEqual(a.OnDelete, b.OnDelete) {
		return false
	}
	if !fkActionsEqual(a.OnUpdate, b.OnUpdate) {
		return false
	}
	return true
}

// fkActionsEqual treats NO ACTION and RESTRICT as equivalent — they
// are in SQL semantics for every engine Quark supports — so a FK
// introspected from MariaDB (RESTRICT) won't generate a diff against
// the same FK from MySQL (NO ACTION).
func fkActionsEqual(a, b string) bool {
	norm := func(s string) string {
		if s == "RESTRICT" {
			return "NO ACTION"
		}
		return s
	}
	return norm(a) == norm(b)
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func stringPtrPretty(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%q", *p)
}

func fkLabel(name string) string {
	if name == "" {
		return "<anonymous>"
	}
	return name
}
