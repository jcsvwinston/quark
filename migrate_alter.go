// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"regexp"
	"strings"
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
	if c.dialect.Name() == "sqlite" {
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
	if d.pk {
		return c.alterPrimaryKey(ctx, exec, o.Table, o.New.Name, o.New.PrimaryKey)
	}
	return nil
}

// alterColumnStatements renders the per-engine ALTERs for the type,
// nullable and default facets; the primary key is a separate step.
func (c *Client) alterColumnStatements(ctx context.Context, exec Executor, o OpAlterColumn, d colDelta) ([]string, error) {
	table, col := c.dialect.Quote(o.Table), c.dialect.Quote(o.New.Name)
	newType := c.mapColumnType(o.New.Type)
	var stmts []string
	switch c.dialect.Name() {
	case "postgres":
		if d.typ {
			stmts = append(stmts, c.dialect.AlterTableAlterColumn(o.Table, o.New.Name, newType))
		}
		if d.nullable {
			if o.New.Nullable {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL", table, col))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL", table, col))
			}
		}
		if d.def {
			if o.New.Default == nil {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT", table, col))
			} else {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s", table, col, *o.New.Default))
			}
		}
	case "mysql", "mariadb":
		if d.typ || d.nullable || d.def {
			// MODIFY restates the whole definition: the type, the
			// nullability and the default travel together or the ones left
			// out are reset.
			def := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", table, col, newType)
			if o.New.Nullable {
				def += " NULL"
			} else {
				def += " NOT NULL"
			}
			if o.New.Default != nil {
				def += " DEFAULT " + *o.New.Default
			}
			stmts = append(stmts, def)
		}
	case "mssql":
		if d.typ || d.nullable {
			null := " NOT NULL"
			if o.New.Nullable {
				null = " NULL"
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s%s", table, col, newType, null))
		}
		if d.def {
			// A default is a named constraint on SQL Server: the old one is
			// dropped by the name the catalog gives it, the new one gets a
			// name of ours.
			if o.Old.Default != nil {
				// Only a column that HAD a default has a constraint to drop.
				name, err := c.mssqlDefaultConstraintName(ctx, exec, o.Table, o.New.Name)
				if err != nil {
					return nil, err
				}
				if name != "" {
					stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", table, c.dialect.Quote(name)))
				}
			}
			if o.New.Default != nil {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s DEFAULT %s FOR %s",
					table, c.dialect.Quote("DF_"+o.Table+"_"+o.New.Name), *o.New.Default, col))
			}
		}
	case "oracle":
		// MODIFY takes only what changes: restating NOT NULL on a column
		// that already is one is ORA-01442.
		var parts []string
		if d.typ {
			parts = append(parts, newType)
		}
		if d.def {
			if o.New.Default == nil {
				parts = append(parts, "DEFAULT NULL")
			} else {
				parts = append(parts, "DEFAULT "+*o.New.Default)
			}
		}
		if d.nullable {
			if o.New.Nullable {
				parts = append(parts, "NULL")
			} else {
				parts = append(parts, "NOT NULL")
			}
		}
		if len(parts) > 0 {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY (%s %s)", table, col, strings.Join(parts, " ")))
		}
	default:
		return nil, fmt.Errorf("%w: ALTER COLUMN not implemented for dialect %s", ErrUnsupportedFeature, c.dialect.Name())
	}
	return stmts, nil
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

// alterPrimaryKey adds or drops a single-column PRIMARY KEY on the engines
// that can do it in place. The constraint has a name on PostgreSQL and SQL
// Server, read from the catalog; MySQL and Oracle address it by role.
func (c *Client) alterPrimaryKey(ctx context.Context, exec Executor, table, column string, add bool) error {
	qt, qc := c.dialect.Quote(table), c.dialect.Quote(column)
	var stmt string
	switch c.dialect.Name() {
	case "postgres", "mssql":
		if add {
			stmt = fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s PRIMARY KEY (%s)", qt, c.dialect.Quote("pk_"+table), qc)
		} else {
			name, err := c.primaryKeyConstraintName(ctx, exec, table)
			if err != nil {
				return err
			}
			if name == "" {
				return fmt.Errorf("alter column %s.%s: the catalog has no PRIMARY KEY constraint to drop", table, column)
			}
			stmt = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", qt, c.dialect.Quote(name))
		}
	case "mysql", "mariadb", "oracle":
		if add {
			stmt = fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s)", qt, qc)
		} else {
			stmt = fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY", qt)
		}
	default:
		return fmt.Errorf("%w: primary-key change not implemented for dialect %s", ErrUnsupportedFeature, c.dialect.Name())
	}
	if _, err := exec.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("alter column %s.%s primary key: %w", table, column, err)
	}
	return nil
}

// primaryKeyConstraintName reads the name of a table's PRIMARY KEY
// constraint on PostgreSQL and SQL Server; "" when there is none.
func (c *Client) primaryKeyConstraintName(ctx context.Context, exec Executor, table string) (string, error) {
	var q string
	switch c.dialect.Name() {
	case "postgres":
		q = `SELECT conname FROM pg_constraint WHERE contype = 'p' AND conrelid = to_regclass($1)`
	case "mssql":
		q = `SELECT name FROM sys.key_constraints WHERE type = 'PK' AND parent_object_id = OBJECT_ID(@p1)`
	default:
		return "", nil
	}
	rows, err := exec.QueryContext(ctx, q, c.dialect.Quote(table))
	if err != nil {
		return "", fmt.Errorf("read the primary key constraint of %s: %w", table, err)
	}
	defer rows.Close()
	name := ""
	if rows.Next() {
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
	}
	return name, rows.Err()
}

// mssqlDefaultConstraintName reads the name of the DEFAULT constraint on a
// column, or "" when the column has none.
func (c *Client) mssqlDefaultConstraintName(ctx context.Context, exec Executor, table, column string) (string, error) {
	rows, err := exec.QueryContext(ctx, `
		SELECT dc.name FROM sys.default_constraints dc
		  JOIN sys.columns col ON col.default_object_id = dc.object_id
		 WHERE dc.parent_object_id = OBJECT_ID(@p1) AND col.name = @p2`, c.dialect.Quote(table), column)
	if err != nil {
		return "", fmt.Errorf("read the default constraint of %s.%s: %w", table, column, err)
	}
	defer rows.Close()
	name := ""
	if rows.Next() {
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
	}
	return name, rows.Err()
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
