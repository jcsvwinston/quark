// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/jcsvwinston/quark/internal/migrate"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// Migrate creates tables for the given models if they don't exist.
// This is a simplistic auto-migration tool for development.
// It uses the "db" and "pk" tags to generate CREATE TABLE statements.
// It also creates join tables for many-to-many relations.
//
// Its statements pass the middleware chain and reach the observers as
// schema work (StatementDDL).
func (c *Client) Migrate(ctx context.Context, models ...any) error {
	exec := c.schemaExec(c.db)
	for _, model := range models {
		if err := c.createTable(ctx, exec, model); err != nil {
			return err
		}
		if err := c.createJoinTables(ctx, exec, model); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) createTable(ctx context.Context, exec Executor, model any) error {
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
	// And on any other invalid tag token (DX-8) — the DDL this function is
	// about to emit would silently drop whatever the typo meant to declare.
	if meta.TagError != nil {
		return fmt.Errorf("%w: %v", ErrInvalidTag, meta.TagError)
	}
	if c.logger != nil {
		for _, w := range meta.TagWarnings {
			c.logger.Warn("struct tag looks like the Nucleus pkg/model grammar", "detail", w)
		}
	}

	// The column types, the auto-increment key and the boolean literal are
	// the dialect's answers (quarkdriver.ColumnTyper, AutoIncrementer), never
	// a table keyed on its name (A11 Q2).
	types := c.schemaTypes()
	var columns []string
	for _, field := range meta.Fields {
		if field.Column == "" {
			continue
		}

		// For composite PKs, never mark individual columns as PRIMARY KEY —
		// we'll append a table-level constraint below instead.
		isPK := field.IsPK && !meta.HasCompositePK
		colDef := c.dialect.Quote(field.Column) + " " + migrate.ColumnSQL(types, field.Type, migrate.TypeOptions{
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
				def = migrate.NormalizeBoolDefaultWith(types.BoolLiteral, def)
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
	// The checks the model declares (quark:"check=…", db:"…,enum=…"), as
	// named table constraints in the shape applyCreateTable writes — the
	// one SQLite's rebuild can read back (A8 S5).
	for _, chk := range modelChecks(meta) {
		columns = append(columns, fmt.Sprintf("CONSTRAINT %s CHECK %s", c.dialect.Quote(chk.Name), wrapExpressionInParens(chk.Expression)))
	}

	if len(columns) == 0 {
		return fmt.Errorf("no database columns found for model %s", t.Name())
	}

	if err := c.createTableIfNotExists(ctx, exec, meta.Table, strings.Join(columns, ",\n  ")); err != nil {
		return fmt.Errorf("failed to create table %s: %w", meta.Table, err)
	}

	// The indexes the model declares (quark:"index"), created after the
	// table with the same idempotent helper CreateIndex uses. Migrate and
	// PlanMigration read the same declaration, so a freshly migrated model
	// plans to nothing (A8 S3).
	for _, idx := range modelIndexes(meta) {
		if err := c.createIndexOn(ctx, exec, meta.Table, idx.Name, idx.Columns, idx.Unique); err != nil {
			return fmt.Errorf("failed to create index %s on %s: %w", idx.Name, meta.Table, err)
		}
	}

	return nil
}

// modelChecks lists the CHECK constraints a model declares through its struct
// tags — quark:"check=<expr>" verbatim, db:"…,enum=a|b" as an IN list — one
// per column, named ck_<table>_<column>.
func modelChecks(meta *ModelMeta) []Check {
	var out []Check
	for _, f := range meta.Fields {
		if f.Column == "" || f.Check == "" {
			continue
		}
		out = append(out, Check{Name: "ck_" + meta.Table + "_" + f.Column, Expression: f.Check})
	}
	return out
}

// modelIndexes lists the secondary indexes a model declares through its
// struct tags: one per column tagged quark:"index" / quark:"index=<name>".
// A quark:"unique" column is NOT listed — Migrate renders it as a column
// constraint and the engine names the backing index itself, so the plan
// recognises it by shape (see diffTable) rather than by a name it cannot
// know in advance.
func modelIndexes(meta *ModelMeta) []Index {
	var out []Index
	for _, f := range meta.Fields {
		if f.Column == "" || f.IndexName == "" {
			continue
		}
		out = append(out, Index{Name: f.IndexName, Columns: []string{f.Column}})
	}
	return out
}

// CreateIndex creates an index on the given table and columns.
// If unique is true, a UNIQUE INDEX is created.
// If the index already exists the error is silently ignored for compatible dialects.
//
// Example:
//
//	client.CreateIndex(ctx, "users", "idx_users_email", []string{"email"}, true)
func (c *Client) CreateIndex(ctx context.Context, table, indexName string, columns []string, unique bool) error {
	return c.createIndexOn(ctx, c.schemaExec(c.db), table, indexName, columns, unique)
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
	// How the engine creates an index only when it is missing is the
	// dialect's answer (quarkdriver.IdempotentDDL): IF NOT EXISTS by default,
	// a sys.indexes guard on SQL Server, a plain CREATE whose "already
	// exists" error counts as success on MySQL and MariaDB.
	query := migrate.CreateIndexIfNotExists(c.dialect, table, indexName, columns, unique)
	if _, err := exec.ExecContext(ctx, query); err != nil {
		if migrate.IsAlreadyExists(c.dialect, quarkdriver.ObjectIndex, err) {
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
// onDelete and onUpdate are referential actions ("CASCADE", "SET NULL",
// "SET DEFAULT", "RESTRICT", "NO ACTION"); an empty one writes no clause, and
// the engine applies its default, NO ACTION. They are written as the
// dialect's engine takes them (quarkdriver.ReferentialActioner): Oracle has
// no clause for NO ACTION, so it is left out, and an action the engine does
// not have — RESTRICT on SQL Server; on Oracle anything but ON DELETE CASCADE
// and ON DELETE SET NULL — returns an error wrapping ErrUnsupportedFeature
// that names it, and nothing is sent.
//
// Example:
//
//	client.AddForeignKey(ctx, "orders", "fk_orders_user", []string{"user_id"}, "users", []string{"id"}, "CASCADE", "SET NULL")
func (c *Client) AddForeignKey(ctx context.Context, table, constraintName string, columns []string, refTable string, refColumns []string, onDelete, onUpdate string) error {
	return c.addForeignKeyOn(ctx, c.schemaExec(c.db), table, constraintName, columns, refTable, refColumns, onDelete, onUpdate)
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

	// The constraint's name is in the error already; "" keeps it out of
	// the refusal.
	actions, err := referentialClauses(c.dialect, "", onDelete, onUpdate)
	if err != nil {
		return fmt.Errorf("AddForeignKey %s: %w", constraintName, err)
	}

	query := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)%s",
		c.dialect.Quote(table),
		c.dialect.Quote(constraintName),
		strings.Join(quotedCols, ", "),
		c.dialect.Quote(refTable),
		strings.Join(quotedRefCols, ", "),
		actions,
	)

	if _, err := exec.ExecContext(ctx, query); err != nil {
		if migrate.IsAlreadyExists(c.dialect, quarkdriver.ObjectConstraint, err) {
			return nil // already exists
		}
		return fmt.Errorf("AddForeignKey %s: %w", constraintName, err)
	}
	return nil
}

// referentialClauses renders a foreign key's ON DELETE and ON UPDATE clauses
// as the dialect's engine takes them (quarkdriver.ReferentialActioner): an
// action it writes is written as the caller gave it, one it does without a
// clause is left out, and one it does not have fails the foreign key with
// ErrUnsupportedFeature naming every such action — before anything is sent.
// Without a ReferentialActioner every action is written as given, as it
// always was. An empty action writes no clause.
func referentialClauses(d Dialect, constraint, onDelete, onUpdate string) (string, error) {
	ra, asks := d.(quarkdriver.ReferentialActioner)
	clauses := ""
	var refused []string
	for _, a := range []struct {
		event  quarkdriver.ReferentialEvent
		action string
	}{{quarkdriver.OnDelete, onDelete}, {quarkdriver.OnUpdate, onUpdate}} {
		if a.action == "" {
			continue
		}
		support := quarkdriver.ActionWritten
		if asks {
			support = ra.ReferentialAction(a.event, normalizeReferentialAction(a.action))
		}
		switch support {
		case quarkdriver.ActionImplied:
			continue
		case quarkdriver.ActionUnsupported:
			refused = append(refused, a.event.String()+" "+normalizeReferentialAction(a.action))
			continue
		}
		clauses += " " + a.event.String() + " " + a.action
	}
	if len(refused) > 0 {
		name := ""
		if constraint != "" {
			name = " " + constraint
		}
		return "", fmt.Errorf("%w: foreign key%s: the %s dialect has no %s; declare an action its engine has, or none for the engine's default",
			ErrUnsupportedFeature, name, d.Name(), strings.Join(refused, " and no "))
	}
	return clauses, nil
}

// normalizeReferentialAction is the form a ReferentialActioner is asked
// about: upper case, its words single-spaced.
func normalizeReferentialAction(action string) string {
	return strings.Join(strings.Fields(strings.ToUpper(action)), " ")
}

// checkReferentialActions asks the dialect about every foreign key a plan
// would write, so that a plan with an action the engine lacks is refused
// before its first operation runs, not half-way through it on an engine
// whose DDL commits as it goes.
func (c *Client) checkReferentialActions(plan Plan) error {
	if _, asks := c.dialect.(quarkdriver.ReferentialActioner); !asks {
		return nil
	}
	for i, op := range plan.Ops {
		var fks []ForeignKey
		switch o := op.(type) {
		case OpAddForeignKey:
			fks = []ForeignKey{o.ForeignKey}
		case OpCreateTable:
			fks = o.Table.ForeignKeys
		}
		for _, fk := range fks {
			if _, err := referentialClauses(c.dialect, fk.Name, fk.OnDelete, fk.OnUpdate); err != nil {
				return fmt.Errorf("ApplyPlan: op %d (%s): %w", i, op.String(), err)
			}
		}
	}
	return nil
}

// createJoinTables creates join tables for many-to-many relations.
func (c *Client) createJoinTables(ctx context.Context, exec Executor, model any) error {
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

	types := c.schemaTypes()
	for _, rel := range meta.Relations {
		if rel.Type != "many_to_many" || rel.JoinTable == "" {
			continue
		}

		// Determine SQL types for FK columns (using int64 for simple auto-migration)
		thisFKType := types.Column(quarkdriver.ColumnSpec{Kind: quarkdriver.KindInt64})
		refFKType := thisFKType

		// Build join table columns
		columns := []string{
			fmt.Sprintf("%s %s", c.dialect.Quote(rel.JoinFK), thisFKType),
			fmt.Sprintf("%s %s", c.dialect.Quote(rel.JoinRefFK), refFKType),
		}

		// Create composite primary key
		pkConstraint := fmt.Sprintf("PRIMARY KEY (%s, %s)", c.dialect.Quote(rel.JoinFK), c.dialect.Quote(rel.JoinRefFK))
		columns = append(columns, pkConstraint)

		if err := c.createTableIfNotExists(ctx, exec, rel.JoinTable, strings.Join(columns, ",\n  ")); err != nil {
			return fmt.Errorf("failed to create join table %s: %w", rel.JoinTable, err)
		}
	}

	return nil
}

// schemaTypes is the column-type pipeline answered by this client's dialect:
// its quarkdriver.ColumnTyper and quarkdriver.AutoIncrementer when it
// implements them, the portable answers when it does not.
func (c *Client) schemaTypes() migrate.Asker {
	return migrate.DialectAsker(c.dialect)
}

// createTableIfNotExists creates table with body unless it exists, the way
// the dialect does that (quarkdriver.IdempotentDDL): CREATE TABLE IF NOT
// EXISTS by default, a guard on sys.tables on SQL Server, a plain CREATE
// whose ORA-00955 counts as success on Oracle.
func (c *Client) createTableIfNotExists(ctx context.Context, exec Executor, table, body string) error {
	_, err := exec.ExecContext(ctx, migrate.CreateTableIfNotExists(c.dialect, table, body))
	if err != nil && !migrate.IsAlreadyExists(c.dialect, quarkdriver.ObjectTable, err) {
		return err
	}
	return nil
}
