// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// This file is the ALTER half of ApplyPlan (A8 S4, control MIG-07): what an
// OpAlterColumn does on each engine, and the table rebuild that SQLite —
// which has no ALTER COLUMN, no ADD CONSTRAINT and no DROP CONSTRAINT — uses
// for every change to a column, a foreign key or a check.
//
// Before this the executor emitted DDL for a type change only, refused the
// nullable, default and primary-key deltas with ErrUnsupportedFeature, and on
// SQLite the type change was rendered as a SQL COMMENT: ApplyPlan returned
// nil and the column kept its type.

// applyAlterColumn brings a column from o.Old to o.New. The deltas — type,
// nullable, default, primary key — are computed the way Diff computes them,
// so a plan Diff produced is applied exactly and a hand-built op that changes
// nothing is a no-op rather than a spurious statement.
//
// What it writes is the dialect's answer (A11 Q2): a quarkdriver.TableRebuilder
// gets SQLite's table rebuild, a quarkdriver.ColumnAlterer writes its own
// statements, and any other dialect gets the SQL standard's.
func (c *Client) applyAlterColumn(ctx context.Context, exec Executor, o OpAlterColumn) error {
	if err := c.guard.ValidateIdentifier(o.Table); err != nil {
		return fmt.Errorf("alter column: %w", err)
	}
	if err := c.guard.ValidateIdentifier(o.New.Name); err != nil {
		return fmt.Errorf("alter column: %w", err)
	}
	if o.Old.Name != "" && o.Old.Name != o.New.Name {
		return fmt.Errorf("%w: OpAlterColumn for %s: renaming %s to %s is not an ALTER COLUMN — use Sync's rename tag or a versioned migration",
			ErrUnsupportedFeature, o.Table, o.Old.Name, o.New.Name)
	}
	d := columnDelta(o.Old, o.New)
	if !d.any() {
		return nil
	}
	if rebuildsTables(c.dialect) {
		return c.sqliteRebuild(ctx, exec, o.Table, func(t *Table) error {
			for i := range t.Columns {
				if t.Columns[i].Name == o.New.Name {
					t.Columns[i] = o.New
					return nil
				}
			}
			return fmt.Errorf("alter column: %s has no column %s", o.Table, o.New.Name)
		})
	}
	stmts, err := c.alterColumnStatements(ctx, exec, o, d)
	if err != nil {
		return err
	}
	for _, s := range stmts {
		if _, err := exec.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("alter column %s.%s: %w", o.Table, o.New.Name, err)
		}
	}
	return nil
}

// alterColumnStatements is the dialect's answer for an in-place change: its
// quarkdriver.ColumnAlterer when it has one, the SQL standard's statements
// otherwise.
func (c *Client) alterColumnStatements(ctx context.Context, exec Executor, o OpAlterColumn, d colDelta) ([]string, error) {
	change := quarkdriver.ColumnChange{
		Table:             o.Table,
		Column:            o.New.Name,
		Type:              c.mapColumnType(o.New.Type),
		Nullable:          o.New.Nullable,
		Default:           o.New.Default,
		PrimaryKey:        o.New.PrimaryKey,
		TypeChanged:       d.typ,
		NullableChanged:   d.nullable,
		DefaultChanged:    d.def,
		PrimaryKeyChanged: d.pk,
		HadDefault:        o.Old.Default != nil,
	}
	if a, ok := c.dialect.(quarkdriver.ColumnAlterer); ok {
		return a.AlterColumn(ctx, exec, change)
	}
	return standardAlterColumn(c.dialect, change)
}

// rebuildsTables reports whether the dialect's engine changes a table by
// rebuilding it (quarkdriver.TableRebuilder) — SQLite among the built-ins.
func rebuildsTables(d Dialect) bool {
	r, ok := d.(quarkdriver.TableRebuilder)
	return ok && r.RebuildsTables()
}

// standardAlterColumn is what a dialect without a quarkdriver.ColumnAlterer
// gets: the SQL standard's statements for each facet, and a named PRIMARY
// KEY constraint for a new key. Dropping a key needs the constraint's name,
// which only the engine's catalog knows.
func standardAlterColumn(d Dialect, c quarkdriver.ColumnChange) ([]string, error) {
	stmts := standardColumnFacets(d, c)
	if c.PrimaryKeyChanged {
		if !c.PrimaryKey {
			return nil, fmt.Errorf("%w: dropping the primary key of %s needs the constraint's name from the engine's catalog, and dialect %s does not implement quarkdriver.ColumnAlterer",
				ErrUnsupportedFeature, c.Table, d.Name())
		}
		stmts = append(stmts, addPrimaryKeyConstraint(d, c.Table, c.Column))
	}
	return stmts, nil
}

// standardColumnFacets writes the type, nullable and default facets the SQL
// standard's way — PostgreSQL's: the dialect's own AlterTableAlterColumn for
// the type, then SET/DROP NOT NULL and SET/DROP DEFAULT.
func standardColumnFacets(d Dialect, c quarkdriver.ColumnChange) []string {
	table, col := d.Quote(c.Table), d.Quote(c.Column)
	var stmts []string
	if c.TypeChanged {
		stmts = append(stmts, d.AlterTableAlterColumn(c.Table, c.Column, c.Type))
	}
	if c.NullableChanged {
		if c.Nullable {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL", table, col))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", table, col))
		}
	}
	if c.DefaultChanged {
		if c.Default == nil {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, col))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", table, col, *c.Default))
		}
	}
	return stmts
}

// addPrimaryKeyConstraint adds a single-column key as a constraint named
// pk_<table>.
func addPrimaryKeyConstraint(d Dialect, table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s PRIMARY KEY (%s)", d.Quote(table), d.Quote("pk_"+table), d.Quote(column))
}

type colDelta struct{ typ, nullable, def, pk bool }

func (d colDelta) any() bool { return d.typ || d.nullable || d.def || d.pk }

// columnDelta is Diff's own comparison, split by facet.
func columnDelta(old, cur Column) colDelta {
	return colDelta{
		typ:      normalizeType(old.Type) != normalizeType(cur.Type),
		nullable: old.Nullable != cur.Nullable,
		def:      !defaultsEqual(old.Default, cur.Default),
		pk:       old.PrimaryKey != cur.PrimaryKey,
	}
}

// ---------------------------------------------------------------------------
// SQLite: the table rebuild
// ---------------------------------------------------------------------------

// sqliteRebuild changes a table the only way SQLite allows for anything
// beyond ADD/DROP/RENAME COLUMN — the procedure its manual documents:
// read the table, create the changed one under a temporary name, copy the
// rows, drop the old, rename the new, recreate indexes and triggers. The
// whole thing runs on the caller's executor, which under ApplyPlan is the
// plan's transaction: a failure anywhere leaves the table as it was.
//
// What it refuses, loudly: a CHECK constraint it cannot read back — SQLite
// keeps checks only in the CREATE TABLE text, and this reads the form
// applyCreateTable writes (`CONSTRAINT "name" CHECK (expr)`); a hand-written
// check in another shape would be silently lost by the rebuild, so the
// rebuild does not happen. Foreign-key names come from the same text; a key
// created without one stays nameless and matches by its columns.
//
// PRAGMA foreign_keys cannot change inside a transaction; when it is on, the
// child rows referencing the table would see it vanish for an instant, so
// the check is deferred to COMMIT — by which time the rows are back.
func (c *Client) sqliteRebuild(ctx context.Context, exec Executor, table string, transform func(*Table) error) error {
	if err := c.guard.ValidateIdentifier(table); err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}
	cur, ddl, err := c.sqliteReadTable(ctx, exec, table)
	if err != nil {
		return err
	}
	want := cur
	want.Columns = append([]Column(nil), cur.Columns...)
	want.ForeignKeys = append([]ForeignKey(nil), cur.ForeignKeys...)
	want.Checks = append([]Check(nil), cur.Checks...)
	if err := transform(&want); err != nil {
		return err
	}

	var fkOn int
	if row := exec.QueryRowContext(ctx, "PRAGMA foreign_keys"); row != nil {
		_ = row.Scan(&fkOn)
	}
	if fkOn == 1 {
		if _, err := exec.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
			return fmt.Errorf("rebuild %s: defer foreign keys: %w", table, err)
		}
	}

	tmp := table + "__quark_rebuild"
	if err := c.guard.ValidateIdentifier(tmp); err != nil {
		return fmt.Errorf("rebuild %s: the temporary name is too long for the identifier rules: %w", table, err)
	}
	tmpTable := want
	tmpTable.Name = tmp
	tmpTable.Indexes = nil // recreated after the rename, by their own DDL
	if err := c.applyCreateTable(ctx, exec, tmpTable); err != nil {
		return fmt.Errorf("rebuild %s: %w", table, err)
	}

	// Copy the columns both shapes have; a column the transform removed is
	// dropped with the old table, one it added starts empty or with its
	// default.
	oldCols := map[string]bool{}
	for _, col := range cur.Columns {
		oldCols[col.Name] = true
	}
	var copyCols []string
	for _, col := range want.Columns {
		if oldCols[col.Name] {
			copyCols = append(copyCols, c.dialect.Quote(col.Name))
		}
	}
	if len(copyCols) > 0 {
		list := strings.Join(copyCols, ", ")
		if _, err := exec.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s",
			c.dialect.Quote(tmp), list, list, c.dialect.Quote(table))); err != nil {
			return fmt.Errorf("rebuild %s: copy rows: %w", table, err)
		}
	}
	if _, err := exec.ExecContext(ctx, "DROP TABLE "+c.dialect.Quote(table)); err != nil {
		return fmt.Errorf("rebuild %s: drop: %w", table, err)
	}
	if _, err := exec.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s RENAME TO %s", c.dialect.Quote(tmp), c.dialect.Quote(table))); err != nil {
		return fmt.Errorf("rebuild %s: rename: %w", table, err)
	}
	// Indexes: those with their own DDL come back verbatim (it names the
	// table, which has its name back); a UNIQUE column's automatic index has
	// none, and comes back as a named unique index of the same shape — which
	// is how Diff recognises it.
	for _, idx := range ddl.indexes {
		if _, err := exec.ExecContext(ctx, idx); err != nil {
			return fmt.Errorf("rebuild %s: recreate index: %w", table, err)
		}
	}
	for _, idx := range cur.Indexes {
		if !strings.HasPrefix(idx.Name, "sqlite_autoindex_") || !idx.Unique {
			continue
		}
		name := "uq_" + table + "_" + strings.Join(idx.Columns, "_")
		if err := c.createIndexOn(ctx, exec, table, name, idx.Columns, true); err != nil {
			return fmt.Errorf("rebuild %s: recreate unique %s: %w", table, name, err)
		}
	}
	for _, trg := range ddl.triggers {
		if _, err := exec.ExecContext(ctx, trg); err != nil {
			return fmt.Errorf("rebuild %s: recreate trigger: %w", table, err)
		}
	}
	return nil
}

// sqliteDDL is what the catalog text holds and the PRAGMAs do not.
type sqliteDDL struct {
	indexes  []string // CREATE INDEX statements, verbatim
	triggers []string // CREATE TRIGGER statements, verbatim
}

var (
	sqliteCheckClause = regexp.MustCompile(`(?m)^\s*CONSTRAINT "([A-Za-z_][A-Za-z0-9_]*)" CHECK (\(.*\)),?\s*$`)
	sqliteFKClause    = regexp.MustCompile(`(?m)^\s*CONSTRAINT "([A-Za-z_][A-Za-z0-9_]*)" FOREIGN KEY \(([^)]*)\) REFERENCES "([A-Za-z_][A-Za-z0-9_]*)" \(([^)]*)\)`)
)

// sqliteReadTable reads a table the way the introspector does, plus what
// only the CREATE TABLE text knows: the checks (in the shape applyCreateTable
// writes) and the names of the foreign keys. A check in another shape makes
// the read refuse, because a rebuild would lose it.
func (c *Client) sqliteReadTable(ctx context.Context, exec Executor, table string) (Table, sqliteDDL, error) {
	cols, err := sqliteListColumns(ctx, exec, table)
	if err != nil {
		return Table{}, sqliteDDL{}, fmt.Errorf("rebuild %s: %w", table, err)
	}
	if len(cols) == 0 {
		return Table{}, sqliteDDL{}, fmt.Errorf("rebuild %s: no such table", table)
	}
	idx, err := sqliteListIndexes(ctx, exec, table)
	if err != nil {
		return Table{}, sqliteDDL{}, fmt.Errorf("rebuild %s: %w", table, err)
	}
	fks, err := sqliteListForeignKeys(ctx, exec, table)
	if err != nil {
		return Table{}, sqliteDDL{}, fmt.Errorf("rebuild %s: %w", table, err)
	}
	t := Table{Name: table, Columns: cols, Indexes: idx, ForeignKeys: fks}

	rows, err := exec.QueryContext(ctx,
		`SELECT type, name, sql FROM sqlite_master WHERE tbl_name = ? AND sql IS NOT NULL ORDER BY type, name`, table)
	if err != nil {
		return Table{}, sqliteDDL{}, fmt.Errorf("rebuild %s: read sqlite_master: %w", table, err)
	}
	defer rows.Close()
	var ddl sqliteDDL
	createSQL := ""
	for rows.Next() {
		var typ, name, sqlText string
		if err := rows.Scan(&typ, &name, &sqlText); err != nil {
			return Table{}, sqliteDDL{}, err
		}
		switch typ {
		case "table":
			createSQL = sqlText
		case "index":
			ddl.indexes = append(ddl.indexes, sqlText)
		case "trigger":
			ddl.triggers = append(ddl.triggers, sqlText)
		}
	}
	if err := rows.Err(); err != nil {
		return Table{}, sqliteDDL{}, err
	}

	// Checks: every CHECK in the text has to be one this can read back.
	for _, m := range sqliteCheckClause.FindAllStringSubmatch(createSQL, -1) {
		t.Checks = append(t.Checks, Check{Name: m[1], Expression: m[2]})
	}
	if n := strings.Count(strings.ToUpper(createSQL), "CHECK"); n != len(t.Checks) {
		return Table{}, sqliteDDL{}, fmt.Errorf("%w: rebuild %s: the table declares a CHECK constraint in a form this rebuild cannot carry (%d found, %d readable) — rewrite it as CONSTRAINT <name> CHECK (<expr>) on its own line, or change the table by hand",
			ErrUnsupportedFeature, table, n, len(t.Checks))
	}
	// Foreign-key names: the PRAGMA has none; the text has the ones we wrote.
	for _, m := range sqliteFKClause.FindAllStringSubmatch(createSQL, -1) {
		cols := unquoteList(m[2])
		ref := m[3]
		refCols := unquoteList(m[4])
		for i := range t.ForeignKeys {
			fk := &t.ForeignKeys[i]
			if fk.Name == "" && fk.RefTable == ref && stringSliceEqual(fk.Columns, cols) && stringSliceEqual(fk.RefColumns, refCols) {
				fk.Name = m[1]
				break
			}
		}
	}
	return t, ddl, nil
}

// unquoteList turns `"a", "b"` into [a b].
func unquoteList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		out = append(out, strings.Trim(strings.TrimSpace(part), `"`))
	}
	return out
}

// sqliteAddForeignKey / sqliteDropForeignKey / sqliteAddCheck /
// sqliteDropCheck are the rebuild transforms behind the constraint ops on
// SQLite, where ALTER TABLE ADD/DROP CONSTRAINT does not exist.
func (c *Client) sqliteAddForeignKey(ctx context.Context, exec Executor, table string, fk ForeignKey) error {
	return c.sqliteRebuild(ctx, exec, table, func(t *Table) error {
		key := foreignKeysByMatchKey([]ForeignKey{fk})
		for k := range foreignKeysByMatchKey(t.ForeignKeys) {
			if _, dup := key[k]; dup {
				return fmt.Errorf("add fk: %s already has a foreign key on %v → %s", table, fk.Columns, fk.RefTable)
			}
		}
		t.ForeignKeys = append(t.ForeignKeys, fk)
		return nil
	})
}

func (c *Client) sqliteDropForeignKey(ctx context.Context, exec Executor, table, name string) error {
	if name == "" {
		return fmt.Errorf("%w: drop fk on %s: the operation names no constraint, and SQLite keeps no other handle", ErrInvalidQuery, table)
	}
	return c.sqliteRebuild(ctx, exec, table, func(t *Table) error {
		kept := t.ForeignKeys[:0]
		found := false
		for _, fk := range t.ForeignKeys {
			if fk.Name == name {
				found = true
				continue
			}
			kept = append(kept, fk)
		}
		if !found {
			return fmt.Errorf("drop fk: %s has no foreign key named %s (a key created without a name can only be dropped by rebuilding the table by hand)", table, name)
		}
		t.ForeignKeys = kept
		return nil
	})
}

func (c *Client) sqliteAddCheck(ctx context.Context, exec Executor, table string, chk Check) error {
	return c.sqliteRebuild(ctx, exec, table, func(t *Table) error {
		for _, have := range t.Checks {
			if have.Name == chk.Name {
				return fmt.Errorf("add check: %s already has a check named %s", table, chk.Name)
			}
		}
		t.Checks = append(t.Checks, chk)
		return nil
	})
}

func (c *Client) sqliteDropCheck(ctx context.Context, exec Executor, table, name string) error {
	return c.sqliteRebuild(ctx, exec, table, func(t *Table) error {
		kept := t.Checks[:0]
		found := false
		for _, chk := range t.Checks {
			if chk.Name == name {
				found = true
				continue
			}
			kept = append(kept, chk)
		}
		if !found {
			return fmt.Errorf("drop check: %s has no check named %s", table, name)
		}
		t.Checks = kept
		return nil
	})
}
