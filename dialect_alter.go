// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// This file is the built-in dialects' answers to ApplyPlan's questions
// (A11 Q2): how each engine changes a column in place
// (quarkdriver.ColumnAlterer), how it drops an index or a constraint by name
// (quarkdriver.ObjectDropper), and — for SQLite — that it changes a table by
// rebuilding it (quarkdriver.TableRebuilder). ApplyPlan asks; before Q2 it
// switched on the dialect's name, so a dialect under any other name got
// ErrUnsupportedFeature for an ALTER COLUMN and SQLite's methods under
// another name never reached the rebuild.
//
// The statements are the ones ApplyPlan wrote for each engine before
// (A8 S4); only where they are chosen moved.

var (
	_ quarkdriver.ColumnAlterer  = (*PostgresDialect)(nil)
	_ quarkdriver.ColumnAlterer  = (*MySQLDialect)(nil)
	_ quarkdriver.ColumnAlterer  = (*MariaDBDialect)(nil)
	_ quarkdriver.ColumnAlterer  = (*MSSQLDialect)(nil)
	_ quarkdriver.ColumnAlterer  = (*OracleDialect)(nil)
	_ quarkdriver.ObjectDropper  = (*MySQLDialect)(nil)
	_ quarkdriver.ObjectDropper  = (*MariaDBDialect)(nil)
	_ quarkdriver.ObjectDropper  = (*MSSQLDialect)(nil)
	_ quarkdriver.TableRebuilder = (*SQLiteDialect)(nil)
)

// --- PostgreSQL: the standard forms, and the key's name from pg_constraint --

// AlterColumn writes the SQL standard's statements — ALTER COLUMN … TYPE,
// SET/DROP NOT NULL, SET/DROP DEFAULT — and adds or drops the primary key
// as a constraint; the name of the one to drop is read from pg_constraint.
func (p *PostgresDialect) AlterColumn(ctx context.Context, exec quarkdriver.Executor, c quarkdriver.ColumnChange) ([]string, error) {
	stmts := standardColumnFacets(p, c)
	if !c.PrimaryKeyChanged {
		return stmts, nil
	}
	if c.PrimaryKey {
		return append(stmts, addPrimaryKeyConstraint(p, c.Table, c.Column)), nil
	}
	name, err := catalogName(ctx, exec, `SELECT conname FROM pg_constraint WHERE contype = 'p' AND conrelid = to_regclass($1)`, p.Quote(c.Table))
	if err != nil {
		return nil, fmt.Errorf("read the primary key constraint of %s: %w", c.Table, err)
	}
	if name == "" {
		return nil, fmt.Errorf("alter column %s.%s: the catalog has no PRIMARY KEY constraint to drop", c.Table, c.Column)
	}
	return append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", p.Quote(c.Table), p.Quote(name))), nil
}

// --- MySQL and MariaDB -------------------------------------------------------

// AlterColumn restates the whole column with MODIFY COLUMN — the type, the
// nullability and the default travel together or the ones left out are
// reset — and adds or drops the primary key by role.
func (m *MySQLDialect) AlterColumn(_ context.Context, _ quarkdriver.Executor, c quarkdriver.ColumnChange) ([]string, error) {
	table, col := m.Quote(c.Table), m.Quote(c.Column)
	var stmts []string
	if c.TypeChanged || c.NullableChanged || c.DefaultChanged {
		def := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", table, col, c.Type)
		if c.Nullable {
			def += " NULL"
		} else {
			def += " NOT NULL"
		}
		if c.Default != nil {
			def += " DEFAULT " + *c.Default
		}
		stmts = append(stmts, def)
	}
	if c.PrimaryKeyChanged {
		if c.PrimaryKey {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s)", table, col))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY", table))
		}
	}
	return stmts, nil
}

// DropIndex names the table: DROP INDEX … ON ….
func (m *MySQLDialect) DropIndex(table, index string) string {
	return fmt.Sprintf("DROP INDEX %s ON %s", m.Quote(index), m.Quote(table))
}

// DropForeignKey is ALTER TABLE … DROP FOREIGN KEY.
func (m *MySQLDialect) DropForeignKey(table, constraint string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s", m.Quote(table), m.Quote(constraint))
}

// DropCheck is ALTER TABLE … DROP CHECK (MySQL 8.0.16+).
func (m *MySQLDialect) DropCheck(table, constraint string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", m.Quote(table), m.Quote(constraint))
}

// DropCheck is the standard ALTER TABLE … DROP CONSTRAINT on MariaDB, which
// has no DROP CHECK: the MySQL spelling it inherited was a syntax error
// (Error 1064) on MariaDB 11.4, found by the dialect kit's ObjectDropper
// check (drivertest.VerifyDialect, A11 Q4).
func (m *MariaDBDialect) DropCheck(table, constraint string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", m.Quote(table), m.Quote(constraint))
}

// --- SQL Server --------------------------------------------------------------

// AlterColumn restates the type and the nullability with ALTER COLUMN, and
// treats a default as the named constraint it is on SQL Server: the old one
// is dropped by the name sys.default_constraints gives it, the new one is
// named DF_<table>_<column>. The primary key is a constraint named pk_<table>
// when added; the one to drop is found in sys.key_constraints.
func (m *MSSQLDialect) AlterColumn(ctx context.Context, exec quarkdriver.Executor, c quarkdriver.ColumnChange) ([]string, error) {
	table, col := m.Quote(c.Table), m.Quote(c.Column)
	var stmts []string
	if c.TypeChanged || c.NullableChanged {
		null := " NOT NULL"
		if c.Nullable {
			null = " NULL"
		}
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s%s", table, col, c.Type, null))
	}
	if c.DefaultChanged {
		if c.HadDefault {
			// Only a column that HAD a default has a constraint to drop.
			name, err := catalogName(ctx, exec, `
		SELECT dc.name FROM sys.default_constraints dc
		  JOIN sys.columns col ON col.default_object_id = dc.object_id
		 WHERE dc.parent_object_id = OBJECT_ID(@p1) AND col.name = @p2`, table, c.Column)
			if err != nil {
				return nil, fmt.Errorf("read the default constraint of %s.%s: %w", c.Table, c.Column, err)
			}
			if name != "" {
				stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", table, m.Quote(name)))
			}
		}
		if c.Default != nil {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s DEFAULT %s FOR %s",
				table, m.Quote("DF_"+c.Table+"_"+c.Column), *c.Default, col))
		}
	}
	if c.PrimaryKeyChanged {
		if c.PrimaryKey {
			stmts = append(stmts, addPrimaryKeyConstraint(m, c.Table, c.Column))
		} else {
			name, err := catalogName(ctx, exec, `SELECT name FROM sys.key_constraints WHERE type = 'PK' AND parent_object_id = OBJECT_ID(@p1)`, table)
			if err != nil {
				return nil, fmt.Errorf("read the primary key constraint of %s: %w", c.Table, err)
			}
			if name == "" {
				return nil, fmt.Errorf("alter column %s.%s: the catalog has no PRIMARY KEY constraint to drop", c.Table, c.Column)
			}
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", table, m.Quote(name)))
		}
	}
	return stmts, nil
}

// DropIndex names the table: DROP INDEX … ON ….
func (m *MSSQLDialect) DropIndex(table, index string) string {
	return fmt.Sprintf("DROP INDEX %s ON %s", m.Quote(index), m.Quote(table))
}

// DropForeignKey is the standard ALTER TABLE … DROP CONSTRAINT.
func (m *MSSQLDialect) DropForeignKey(table, constraint string) string {
	return dropConstraint(m, table, constraint)
}

// DropCheck is the standard ALTER TABLE … DROP CONSTRAINT.
func (m *MSSQLDialect) DropCheck(table, constraint string) string {
	return dropConstraint(m, table, constraint)
}

// --- Oracle ------------------------------------------------------------------

// AlterColumn writes one MODIFY with only what changes — restating NOT NULL
// on a column that already is one is ORA-01442 — and adds or drops the
// primary key by role.
func (o *OracleDialect) AlterColumn(_ context.Context, _ quarkdriver.Executor, c quarkdriver.ColumnChange) ([]string, error) {
	table, col := o.Quote(c.Table), o.Quote(c.Column)
	var parts []string
	if c.TypeChanged {
		parts = append(parts, c.Type)
	}
	if c.DefaultChanged {
		if c.Default == nil {
			parts = append(parts, "DEFAULT NULL")
		} else {
			parts = append(parts, "DEFAULT "+*c.Default)
		}
	}
	if c.NullableChanged {
		if c.Nullable {
			parts = append(parts, "NULL")
		} else {
			parts = append(parts, "NOT NULL")
		}
	}
	var stmts []string
	if len(parts) > 0 {
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s MODIFY (%s %s)", table, col, strings.Join(parts, " ")))
	}
	if c.PrimaryKeyChanged {
		if c.PrimaryKey {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ADD PRIMARY KEY (%s)", table, col))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY", table))
		}
	}
	return stmts, nil
}

// --- SQLite: the table rebuild -----------------------------------------------

// RebuildsTables reports true: SQLite has no ALTER COLUMN, no ADD CONSTRAINT
// and no DROP CONSTRAINT, so ApplyPlan changes a column and adds or drops a
// foreign key or a CHECK by rebuilding the table (migrate_alter.go).
func (*SQLiteDialect) RebuildsTables() bool { return true }

// --- helpers -------------------------------------------------------------------

// dropConstraint is the SQL standard's ALTER TABLE … DROP CONSTRAINT.
func dropConstraint(d Dialect, table, constraint string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", d.Quote(table), d.Quote(constraint))
}

// catalogName reads one name from the catalog, or "" when the query returns
// no row.
func catalogName(ctx context.Context, exec quarkdriver.Executor, query string, args ...any) (string, error) {
	rows, err := exec.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
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
