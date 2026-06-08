// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/jcsvwinston/quark/internal/migrate"
)

// Migrate creates tables for the given models if they don't exist.
// This is a simplistic auto-migration tool for development.
// It uses the "db" and "pk" tags to generate CREATE TABLE statements.
// It also creates join tables for many-to-many relations.
func (c *Client) Migrate(ctx context.Context, models ...any) error {
	for _, model := range models {
		if err := c.createTable(ctx, model); err != nil {
			return err
		}
		if err := c.createJoinTables(ctx, model); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) createTable(ctx context.Context, model any) error {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return fmt.Errorf("model must be a struct, got %s", t.Kind())
	}

	meta := GetModelMetaByType(t)
	if meta == nil {
		return fmt.Errorf("failed to get metadata for %s", t.Name())
	}
	// Fail fast on an invalid quark:"tz=..." tag before emitting any DDL.
	if meta.TZError != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTimezone, meta.TZError)
	}

	var columns []string
	for _, field := range meta.Fields {
		if field.Column == "" {
			continue
		}

		// For composite PKs, never mark individual columns as PRIMARY KEY —
		// we'll append a table-level constraint below instead.
		isPK := field.IsPK && !meta.HasCompositePK
		colDef := c.dialect.Quote(field.Column) + " " + migrate.SQLTypeWithOpts(c.dialect.Name(), field.Type, migrate.TypeOptions{
			Size:      field.Size,
			Precision: field.Precision,
			Scale:     field.Scale,
			IsPK:      isPK,
		})

		// Append NOT NULL constraint (skip for PKs — already included in SQLType)
		if !isPK && field.NotNull {
			colDef += " NOT NULL"
		}
		// Append DEFAULT value. A boolean default is normalized to the literal
		// the dialect accepts (TRUE/FALSE on PostgreSQL, 1/0 elsewhere) — no raw
		// bool literal is portable across all six engines, so passing the tag
		// through verbatim would break the migration on PG/MSSQL/Oracle.
		if field.Default != "" {
			def := field.Default
			if migrate.IsBoolColumn(field.Type) {
				def = migrate.NormalizeBoolDefault(c.dialect.Name(), def)
			}
			colDef += " DEFAULT " + def
		}
		// Append UNIQUE constraint
		if field.Unique && !isPK {
			colDef += " UNIQUE"
		}
		columns = append(columns, colDef)
	}

	// Composite PK: append table-level PRIMARY KEY constraint
	if meta.HasCompositePK {
		pkCols := make([]string, len(meta.CompositePK))
		for i, pk := range meta.CompositePK {
			pkCols[i] = c.dialect.Quote(pk.Column)
		}
		columns = append(columns, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(pkCols, ", ")))
	}

	if len(columns) == 0 {
		return fmt.Errorf("no database columns found for model %s", t.Name())
	}

	var query string
	switch c.dialect.Name() {
	case "mysql", "mariadb", "postgres", "sqlite":
		query = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n);",
			c.dialect.Quote(meta.Table),
			strings.Join(columns, ",\n  "),
		)
	case "mssql":
		query = fmt.Sprintf(`IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = '%s') 
		CREATE TABLE %s (
			%s
		);`, meta.Table, c.dialect.Quote(meta.Table), strings.Join(columns, ",\n  "))
	case "oracle":
		query = fmt.Sprintf("CREATE TABLE %s (\n  %s\n)",
			c.dialect.Quote(meta.Table),
			strings.Join(columns, ",\n  "),
		)
	default:
		query = fmt.Sprintf("CREATE TABLE %s (\n  %s\n)",
			c.dialect.Quote(meta.Table),
			strings.Join(columns, ",\n  "),
		)
	}

	_, err := c.db.ExecContext(ctx, query)
	if err != nil {
		if c.dialect.Name() == "oracle" && strings.Contains(err.Error(), "ORA-00955") {
			return nil
		}
		return fmt.Errorf("failed to create table %s: %w", meta.Table, err)
	}

	return nil
}

// CreateIndex creates an index on the given table and columns.
// If unique is true, a UNIQUE INDEX is created.
// If the index already exists the error is silently ignored for compatible dialects.
//
// Example:
//
//	client.CreateIndex(ctx, "users", "idx_users_email", []string{"email"}, true)
func (c *Client) CreateIndex(ctx context.Context, table, indexName string, columns []string, unique bool) error {
	return c.createIndexOn(ctx, c.db, table, indexName, columns, unique)
}

// createIndexOn is the [Executor]-parameterised variant of
// [Client.CreateIndex] that the transactional ApplyPlan path
// (F3-4-tx) uses to route DDL through a `*sql.Tx`. The public
// CreateIndex wraps this with `c.db` as the executor.
//
// Splitting this out keeps the public API stable while letting
// ApplyPlan's transactional wrapper share the same per-dialect
// quirks (MSSQL IF NOT EXISTS guard, MySQL 1061 silent-ignore,
// Oracle ORA-01408 silent-ignore).
func (c *Client) createIndexOn(ctx context.Context, exec Executor, table, indexName string, columns []string, unique bool) error {
	if len(columns) == 0 {
		return fmt.Errorf("CreateIndex: at least one column required")
	}
	quotedCols := make([]string, len(columns))
	for i, col := range columns {
		quotedCols[i] = c.dialect.Quote(col)
	}
	uniqueKW := ""
	if unique {
		uniqueKW = "UNIQUE "
	}

	var query string
	switch c.dialect.Name() {
	case "mssql":
		query = fmt.Sprintf("IF NOT EXISTS (SELECT name FROM sys.indexes WHERE name = '%s') CREATE %sINDEX %s ON %s (%s)",
			indexName, uniqueKW, c.dialect.Quote(indexName), c.dialect.Quote(table), strings.Join(quotedCols, ", "))
	case "mysql", "mariadb":
		// MySQL/MariaDB do not support IF NOT EXISTS on CREATE INDEX;
		// use CREATE INDEX directly and ignore "Duplicate key name" (1061).
		query = fmt.Sprintf("CREATE %sINDEX %s ON %s (%s)",
			uniqueKW, c.dialect.Quote(indexName), c.dialect.Quote(table), strings.Join(quotedCols, ", "))
	default:
		query = fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			uniqueKW, c.dialect.Quote(indexName), c.dialect.Quote(table), strings.Join(quotedCols, ", "))
	}

	_, err := exec.ExecContext(ctx, query)
	if err != nil {
		errStr := err.Error()
		// Oracle: index already exists
		if c.dialect.Name() == "oracle" && strings.Contains(errStr, "ORA-01408") {
			return nil
		}
		// MySQL/MariaDB: Duplicate key name (error 1061)
		if (c.dialect.Name() == "mysql" || c.dialect.Name() == "mariadb") &&
			strings.Contains(errStr, "1061") {
			return nil
		}
		return fmt.Errorf("CreateIndex %s: %w", indexName, err)
	}
	return nil
}

// AddForeignKey adds a FOREIGN KEY constraint to an existing table.
// constraintName is the constraint identifier; refTable is the referenced table;
// columns and refColumns are matched by position.
//
// Example:
//
//	client.AddForeignKey(ctx, "orders", "fk_orders_user", []string{"user_id"}, "users", []string{"id"}, "CASCADE", "SET NULL")
func (c *Client) AddForeignKey(ctx context.Context, table, constraintName string, columns []string, refTable string, refColumns []string, onDelete, onUpdate string) error {
	return c.addForeignKeyOn(ctx, c.db, table, constraintName, columns, refTable, refColumns, onDelete, onUpdate)
}

// addForeignKeyOn is the [Executor]-parameterised variant of
// [Client.AddForeignKey]. Same role as `createIndexOn` —
// transactional ApplyPlan uses it to route the ALTER TABLE
// through a `*sql.Tx`, while the public AddForeignKey wraps with
// `c.db`.
func (c *Client) addForeignKeyOn(ctx context.Context, exec Executor, table, constraintName string, columns []string, refTable string, refColumns []string, onDelete, onUpdate string) error {
	if len(columns) == 0 || len(refColumns) == 0 {
		return fmt.Errorf("AddForeignKey: columns and refColumns must not be empty")
	}
	quotedCols := make([]string, len(columns))
	for i, col := range columns {
		quotedCols[i] = c.dialect.Quote(col)
	}
	quotedRefCols := make([]string, len(refColumns))
	for i, col := range refColumns {
		quotedRefCols[i] = c.dialect.Quote(col)
	}

	actions := ""
	if onDelete != "" {
		actions += " ON DELETE " + onDelete
	}
	if onUpdate != "" {
		actions += " ON UPDATE " + onUpdate
	}

	query := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)%s",
		c.dialect.Quote(table),
		c.dialect.Quote(constraintName),
		strings.Join(quotedCols, ", "),
		c.dialect.Quote(refTable),
		strings.Join(quotedRefCols, ", "),
		actions,
	)

	_, err := exec.ExecContext(ctx, query)
	if err != nil {
		if c.dialect.Name() == "oracle" && strings.Contains(err.Error(), "ORA-02264") {
			return nil // already exists
		}
		return fmt.Errorf("AddForeignKey %s: %w", constraintName, err)
	}
	return nil
}

// createJoinTables creates join tables for many-to-many relations.
func (c *Client) createJoinTables(ctx context.Context, model any) error {
	t := reflect.TypeOf(model)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil
	}

	meta := GetModelMetaByType(t)
	if meta == nil {
		return nil
	}

	for _, rel := range meta.Relations {
		if rel.Type != "many_to_many" || rel.JoinTable == "" {
			continue
		}

		// Determine SQL types for FK columns (using int64 for simple auto-migration)
		thisFKType := migrate.SQLType(c.dialect.Name(), reflect.TypeOf(int64(0)), false)
		refFKType := migrate.SQLType(c.dialect.Name(), reflect.TypeOf(int64(0)), false)

		// Build join table columns
		columns := []string{
			fmt.Sprintf("%s %s", c.dialect.Quote(rel.JoinFK), thisFKType),
			fmt.Sprintf("%s %s", c.dialect.Quote(rel.JoinRefFK), refFKType),
		}

		// Create composite primary key
		pkConstraint := fmt.Sprintf("PRIMARY KEY (%s, %s)", c.dialect.Quote(rel.JoinFK), c.dialect.Quote(rel.JoinRefFK))
		columns = append(columns, pkConstraint)

		// Build CREATE TABLE query
		var query string
		switch c.dialect.Name() {
		case "mysql", "mariadb", "postgres", "sqlite":
			query = fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n  %s\n);",
				c.dialect.Quote(rel.JoinTable),
				strings.Join(columns, ",\n  "),
			)
		case "mssql":
			query = fmt.Sprintf(`IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = '%s')
			CREATE TABLE %s (
				%s
			);`, rel.JoinTable, c.dialect.Quote(rel.JoinTable), strings.Join(columns, ",\n				"))
		case "oracle":
			query = fmt.Sprintf("CREATE TABLE %s (\n  %s\n)",
				c.dialect.Quote(rel.JoinTable),
				strings.Join(columns, ",\n  "),
			)
		default:
			query = fmt.Sprintf("CREATE TABLE %s (\n  %s\n)",
				c.dialect.Quote(rel.JoinTable),
				strings.Join(columns, ",\n  "),
			)
		}

		_, err := c.db.ExecContext(ctx, query)
		if err != nil {
			if c.dialect.Name() == "oracle" && strings.Contains(err.Error(), "ORA-00955") {
				continue
			}
			return fmt.Errorf("failed to create join table %s: %w", rel.JoinTable, err)
		}
	}

	return nil
}
