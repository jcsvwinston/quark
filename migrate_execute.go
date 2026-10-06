// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark/internal/migrate"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// ApplyPlan executes the operations in `plan` against the database
// in the order they appear. What each op writes is the dialect's answer
// (A11 Q2) — the `Dialect.AlterTable*` helpers, and the optional
// interfaces of quarkdriver: ColumnAlterer for an OpAlterColumn,
// ObjectDropper for the drops, IdempotentDDL for an index, and
// TableRebuilder for an engine that rebuilds a table instead of altering
// it (SQLite). A dialect that implements none of them gets the SQL
// standard's statements.
//
// **Transactional behaviour** (F3-4-tx): the dialect decides, through
// [Dialect.SupportsTransactionalDDL] — never its name (A11 Q2).
//
//   - **PostgreSQL, MSSQL, SQLite** — DDL is transactional on these
//     engines (SupportsTransactionalDDL is true). ApplyPlan opens a BEGIN,
//     runs all ops, and COMMITs.
//     On ANY failure the transaction is rolled back, leaving the
//     schema in its pre-plan state. This is the safety net users
//     should rely on when running migrations against production.
//
//   - **MySQL, MariaDB, Oracle** (SupportsTransactionalDDL is
//     false) — DDL implicitly commits the
//     current transaction on every statement, so wrapping is
//     pointless. Instead, ApplyPlan uses a **resumable** path
//     backed by a `quark_migration_state` checkpoint table
//     (F3-4-resumable). Each successfully applied op is recorded
//     by `(plan_hash, op_index)`; a re-invocation against the
//     same plan (identified by `Plan.Hash()`) skips ops that
//     were already recorded. A mid-plan failure can therefore be
//     fixed (the underlying constraint addressed, the connection
//     restored, etc.) and resumed simply by calling ApplyPlan
//     again with the same plan — no re-applying earlier
//     successful ops, no manual state management.
//
//     Drift detection: if the plan changed between runs (different
//     ops, different table names, anything that flips the hash),
//     the state from the prior run doesn't apply and the new
//     plan starts fresh from op 0. This is the safety boundary
//     that prevents "resume from op 3" against a plan whose op 3
//     means something different.
//
//     **Concurrency**: on non-transactional engines, two processes
//     calling ApplyPlan against the same plan simultaneously race
//     on the state table — both read `resumeFrom = -1`, both try
//     to apply op 0, one (or both) of them hit DDL errors (table
//     already exists) and a `duplicate-PK` on the state insert.
//     `Client.AcquireMigrationLock` (F3-1) is the right primitive
//     to serialise — wrap your ApplyPlan call:
//
//     lock, err := client.AcquireMigrationLock(ctx, "schema", 30*time.Second)
//     if err != nil { return err }
//     defer lock.Release(ctx)
//     err = client.ApplyPlan(ctx, plan)
//
//     The reason this isn't done automatically inside ApplyPlan:
//     not every dialect implements `MigrationLocker` yet (Oracle
//     is pending) and the lock name / timeout are workflow choices
//     that belong to the caller. Future: a `Client.MigrateAtomic`
//     wrapper that bundles lock + plan + apply, target for F3-5.
//
// The returned error carries the index of the op that failed and
// the op's String() rendering for debuggability, regardless of
// which path was taken.
//
// Operation-specific caveats:
//
//   - **OpAlterColumn** changes the type, nullability, default and
//     primary key in place (A8 S4). Dropping a primary key needs the
//     constraint's name from the engine's catalog, so a dialect without
//     a quarkdriver.ColumnAlterer gets ErrUnsupportedFeature for that
//     one change. Renaming a column is not an ALTER COLUMN and is
//     refused.
//   - **Referential actions** — the ON DELETE and ON UPDATE of an
//     OpAddForeignKey and of the foreign keys of an OpCreateTable — are
//     written as the dialect's engine takes them
//     (quarkdriver.ReferentialActioner): Oracle's NO ACTION is left out,
//     and an action the engine does not have (SQL Server's RESTRICT,
//     Oracle's ON UPDATE CASCADE) refuses the whole plan with
//     ErrUnsupportedFeature before its first operation runs (QK-49).
//   - **On SQLite** (a quarkdriver.TableRebuilder) a column change and
//     the constraint ops rebuild the table inside the plan's
//     transaction; a CHECK the rebuild cannot read back makes it
//     refuse with ErrUnsupportedFeature rather than lose the check.
//
// Every statement it sends — its own and those of the dialect's
// ColumnAlterer and TableRebuilder, which receive the same Executor —
// passes the middleware chain and reaches the observers: what it
// executes as StatementDDL, what it reads as StatementIntrospection.
func (c *Client) ApplyPlan(ctx context.Context, plan Plan) error {
	if err := c.checkReferentialActions(plan); err != nil {
		return err
	}
	if c.dialect.SupportsTransactionalDDL() {
		return c.applyPlanTx(ctx, plan)
	}
	return c.applyPlanNoTx(ctx, plan)
}

// applyPlanTx wraps the op loop in BEGIN/COMMIT. On any error the
// transaction is rolled back and the schema returns to its pre-plan
// state. The defer-Rollback pattern is the canonical Go form: it
// no-ops after a successful Commit (sql.ErrTxDone) but salvages the
// state if Commit was never reached.
func (c *Client) applyPlanTx(ctx context.Context, plan Plan) error {
	// Pass nil opts → driver default isolation level (READ COMMITTED
	// on PostgreSQL and MSSQL, deferred on SQLite). For DDL-only
	// workloads this is appropriate: schema-level locks are
	// orthogonal to row-level isolation, and elevating to
	// SERIALIZABLE would only add deadlock risk on MSSQL without
	// any semantic gain. Callers who need a different level should
	// wrap with their own BeginTx + manual op loop rather than
	// asking ApplyPlan for tunability — the helper is intentionally
	// opinionated for the common path.
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ApplyPlan: begin tx: %w", err)
	}
	defer func() {
		// Rollback is idempotent w.r.t. a committed tx — it returns
		// sql.ErrTxDone which we don't propagate.
		_ = tx.Rollback()
	}()
	exec := c.schemaExec(tx)
	for i, op := range plan.Ops {
		if err := c.applyOne(ctx, exec, op); err != nil {
			return fmt.Errorf("ApplyPlan: op %d (%s): %w", i, op.String(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ApplyPlan: commit: %w", err)
	}
	return nil
}

// applyPlanNoTx runs the ops without a transaction wrapper. Used on
// engines where DDL implicitly commits (MySQL / MariaDB / Oracle).
//
// **Resumable** (F3-4-resumable): a checkpoint table
// `quark_migration_state` records each successfully applied op
// keyed by `(plan_hash, op_index)`. On the next invocation against
// the SAME plan (same Plan.Hash()), the apply path skips ops that
// were already recorded — so a mid-plan failure can be fixed
// (manually, or by running ApplyPlan with the same plan after the
// underlying problem is resolved) and resumed without re-applying
// the earlier successful ops.
//
// Drift detection: the plan_hash key means two plans that differ
// in any way (different ops, different table names, different
// column types) won't share state. A user who modifies their
// models between runs starts fresh — there's no false "resume
// from op 3" against a plan whose op 3 means something different
// from the original.
//
// Empty plans skip the state table entirely (no ops to record,
// no resume to perform) so the noop case stays cheap.
func (c *Client) applyPlanNoTx(ctx context.Context, plan Plan) error {
	if len(plan.Ops) == 0 {
		return nil
	}
	exec := c.schemaExec(c.db)
	if err := c.ensureMigrationStateTable(ctx, exec); err != nil {
		return fmt.Errorf("ApplyPlan: %w", err)
	}
	planHash := plan.Hash()
	resumeFrom, err := c.lastAppliedOpIndex(ctx, exec, planHash)
	if err != nil {
		return fmt.Errorf("ApplyPlan: %w", err)
	}
	// resumeFrom is the highest op_index already applied, or -1
	// if none. The first op to (re-)apply is resumeFrom + 1.
	for i, op := range plan.Ops {
		if i <= resumeFrom {
			continue
		}
		if err := c.applyOne(ctx, exec, op); err != nil {
			return fmt.Errorf("ApplyPlan: op %d (%s): %w", i, op.String(), err)
		}
		if err := c.recordOpApplied(ctx, exec, planHash, i, op.String()); err != nil {
			// Rare: the op itself succeeded but recording the
			// state failed. Surface this so the caller knows the
			// schema is one step ahead of the checkpoint — they
			// can either run again (the duplicate-PK on retry
			// would reveal the recording failure as a separate
			// signal) or manually update the state table.
			return fmt.Errorf("ApplyPlan: op %d (%s) succeeded but state recording failed: %w", i, op.String(), err)
		}
	}
	return nil
}

// applyOne dispatches a single Operation to the appropriate
// per-dialect DDL path. Each branch is intentionally small and
// inline rather than living in a per-op-type helper, because the
// op universe is closed (sealed Operation interface) and the
// switch reads top-to-bottom in plan order — easier to audit
// against the Diff godoc.
//
// All identifier inputs (table name, column name, index name, FK
// name, check name) are validated via `c.guard.ValidateIdentifier`
// before they reach the splice site. A maliciously-constructed
// `Plan` passed to ApplyPlan would be rejected here rather than
// reaching `ExecContext`. The Op values are public, so they are
// untrusted input from the SQLGuard perspective even when produced
// by `Diff` (a defensive layer above `Diff`).
func (c *Client) applyOne(ctx context.Context, exec Executor, op Operation) error {
	switch o := op.(type) {
	case OpCreateTable:
		return c.applyCreateTable(ctx, exec, o.Table)
	case OpDropTable:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("drop table: %w", err)
		}
		_, err := exec.ExecContext(ctx, fmt.Sprintf("DROP TABLE %s", c.dialect.Quote(o.Table)))
		return err
	case OpAddColumn:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("add column: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Column.Name); err != nil {
			return fmt.Errorf("add column: %w", err)
		}
		ddl := c.dialect.AlterTableAddColumn(o.Table, o.Column.Name, c.mapColumnType(o.Column.Type))
		_, err := exec.ExecContext(ctx, ddl)
		return err
	case OpDropColumn:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("drop column: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Column); err != nil {
			return fmt.Errorf("drop column: %w", err)
		}
		ddl := c.dialect.AlterTableDropColumn(o.Table, o.Column)
		_, err := exec.ExecContext(ctx, ddl)
		return err
	case OpAlterColumn:
		return c.applyAlterColumn(ctx, exec, o)
	case OpCreateIndex:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Index.Name); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
		for _, col := range o.Index.Columns {
			if err := c.guard.ValidateIdentifier(col); err != nil {
				return fmt.Errorf("create index: %w", err)
			}
		}
		return c.createIndexOn(ctx, exec, o.Table, o.Index.Name, o.Index.Columns, o.Index.Unique)
	case OpDropIndex:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("drop index: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Index); err != nil {
			return fmt.Errorf("drop index: %w", err)
		}
		return c.dropIndex(ctx, exec, o.Table, o.Index)
	case OpAddForeignKey:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("add fk: %w", err)
		}
		if o.ForeignKey.Name != "" {
			if err := c.guard.ValidateIdentifier(o.ForeignKey.Name); err != nil {
				return fmt.Errorf("add fk: %w", err)
			}
		}
		if err := c.guard.ValidateIdentifier(o.ForeignKey.RefTable); err != nil {
			return fmt.Errorf("add fk: %w", err)
		}
		for _, col := range o.ForeignKey.Columns {
			if err := c.guard.ValidateIdentifier(col); err != nil {
				return fmt.Errorf("add fk: %w", err)
			}
		}
		for _, col := range o.ForeignKey.RefColumns {
			if err := c.guard.ValidateIdentifier(col); err != nil {
				return fmt.Errorf("add fk: %w", err)
			}
		}
		fk := o.ForeignKey
		if rebuildsTables(c.dialect) {
			return c.sqliteAddForeignKey(ctx, exec, o.Table, fk)
		}
		return c.addForeignKeyOn(ctx, exec, o.Table, fk.Name, fk.Columns, fk.RefTable, fk.RefColumns, fk.OnDelete, fk.OnUpdate)
	case OpDropForeignKey:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("drop fk: %w", err)
		}
		if o.ForeignKey != "" {
			if err := c.guard.ValidateIdentifier(o.ForeignKey); err != nil {
				return fmt.Errorf("drop fk: %w", err)
			}
		}
		if rebuildsTables(c.dialect) {
			return c.sqliteDropForeignKey(ctx, exec, o.Table, o.ForeignKey)
		}
		return c.dropForeignKey(ctx, exec, o.Table, o.ForeignKey)
	case OpAddCheck:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("add check: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Check.Name); err != nil {
			return fmt.Errorf("add check: %w", err)
		}
		if rebuildsTables(c.dialect) {
			return c.sqliteAddCheck(ctx, exec, o.Table, o.Check)
		}
		return c.addCheck(ctx, exec, o.Table, o.Check.Name, o.Check.Expression)
	case OpDropCheck:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("drop check: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Check); err != nil {
			return fmt.Errorf("drop check: %w", err)
		}
		if rebuildsTables(c.dialect) {
			return c.sqliteDropCheck(ctx, exec, o.Table, o.Check)
		}
		return c.dropCheck(ctx, exec, o.Table, o.Check)
	default:
		return fmt.Errorf("%w: unknown Operation type %T", ErrUnsupportedFeature, op)
	}
}

// applyCreateTable renders a CREATE TABLE statement from a neutral
// `Table` value. Distinct from `Client.Migrate`'s codepath, which
// builds the DDL from a Go model — here we build from the diff's
// already-neutralised Table, so column types and nullable flags
// come from the catalog or from `modelsToSchema` directly.
//
// PRIMARY KEY rendering mirrors the migrator (F3-2-pk): a single PK
// column of an integer family renders as the dialect's auto-increment
// fragment (its quarkdriver.AutoIncrementer, which the migrator asks too —
// BIGSERIAL, AUTO_INCREMENT, IDENTITY, AUTOINCREMENT…), any other single PK keeps
// its own type plus `PRIMARY KEY`, and a composite key renders as a
// table-level `PRIMARY KEY (a, b)` constraint. PK columns skip NOT NULL
// (implied) and — on the auto-increment path — skip DEFAULT too: the
// generation clause owns the value (a catalog-sourced `nextval(...)`
// default would conflict with SERIAL).
//
// The op carries the WHOLE table and this emits the whole table
// (QK-27): the foreign keys and the checks go inline, as table-level
// constraints — the only form SQLite has, and one every engine
// accepts, so a plan means the same thing on the six — and each index
// follows as its own CREATE INDEX through the same idempotent helper
// CreateIndex uses. Before A8 S3 this read t.Columns and returned nil,
// its godoc said the rest came "from subsequent ops", and Diff never
// produced those ops for a new table: the same desired schema
// re-proposed its indexes and keys after every apply. Diff orders the
// create-table ops so a referenced table already exists (see Diff);
// on SQLite the reference is not checked at CREATE time anyway.
func (c *Client) applyCreateTable(ctx context.Context, exec Executor, t Table) error {
	if len(t.Columns) == 0 {
		return fmt.Errorf("applyCreateTable %q: table has no columns", t.Name)
	}
	if err := c.guard.ValidateIdentifier(t.Name); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	var pkCols []string
	for _, col := range t.Columns {
		if col.PrimaryKey {
			pkCols = append(pkCols, col.Name)
		}
	}
	singlePK := len(pkCols) == 1
	cols := make([]string, 0, len(t.Columns))
	for _, col := range t.Columns {
		if err := c.guard.ValidateIdentifier(col.Name); err != nil {
			return fmt.Errorf("create table %s: %w", t.Name, err)
		}
		// NOTE: col.Type and col.Default are treated as trusted
		// catalog-emitted values (the catalog readers in F3-2 are
		// the only legitimate source), not as identifiers. They
		// flow into DDL as values, not as quoted names. A maliciously
		// constructed Plan with adversarial Type/Default strings is
		// out of scope — the same caveat applies to AddForeignKey's
		// OnDelete/OnUpdate.
		if col.PrimaryKey && singlePK {
			class := migrate.ClassifyPKType(col.Type)
			piece := c.dialect.Quote(col.Name) + " " + migrate.PKColumnSQLWith(c.schemaTypes(), class, c.mapColumnType(col.Type))
			// PRIMARY KEY implies NOT NULL; auto-increment owns the
			// value, so only a non-auto PK keeps a declared default.
			if class != migrate.PKInteger && col.Default != nil && !isAutoincrementDefault(*col.Default) {
				piece += " DEFAULT " + *col.Default
			}
			cols = append(cols, piece)
			continue
		}
		piece := c.dialect.Quote(col.Name) + " " + c.mapColumnType(col.Type)
		if !col.Nullable || col.PrimaryKey {
			// The catalog-side Type may already include NOT NULL
			// in the dialect-native form (PG's `bigint NOT NULL`
			// for a PK reassembly). We only append when the Type
			// string doesn't already carry the constraint, to
			// avoid double-NOT-NULL in the emitted DDL.
			if !strings.Contains(strings.ToUpper(col.Type), "NOT NULL") {
				piece += " NOT NULL"
			}
		}
		if col.Default != nil {
			piece += " DEFAULT " + *col.Default
		}
		cols = append(cols, piece)
	}
	// Composite key → table-level constraint, mirroring the migrator.
	if len(pkCols) > 1 {
		quoted := make([]string, len(pkCols))
		for i, n := range pkCols {
			quoted[i] = c.dialect.Quote(n)
		}
		cols = append(cols, fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(quoted, ", ")))
	}
	// Foreign keys, inline. Every identifier is validated as in the
	// OpAddForeignKey branch of applyOne; the actions are values.
	for _, fk := range t.ForeignKeys {
		clause, err := c.foreignKeyClause(fk)
		if err != nil {
			return fmt.Errorf("create table %s: %w", t.Name, err)
		}
		cols = append(cols, clause)
	}
	// Checks, inline. SQLite has no ALTER TABLE ADD CONSTRAINT, so this
	// is the only place a CHECK can be declared there.
	for _, chk := range t.Checks {
		if err := c.guard.ValidateIdentifier(chk.Name); err != nil {
			return fmt.Errorf("create table %s: check: %w", t.Name, err)
		}
		cols = append(cols, fmt.Sprintf("CONSTRAINT %s CHECK %s", c.dialect.Quote(chk.Name), wrapExpressionInParens(chk.Expression)))
	}
	ddl := fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", c.dialect.Quote(t.Name), strings.Join(cols, ",\n  "))
	if _, err := exec.ExecContext(ctx, ddl); err != nil {
		return err
	}
	// Indexes, each its own statement: there is no inline form that is
	// portable, and the helper already knows each engine's spelling.
	for _, idx := range t.Indexes {
		if err := c.guard.ValidateIdentifier(idx.Name); err != nil {
			return fmt.Errorf("create table %s: index: %w", t.Name, err)
		}
		for _, col := range idx.Columns {
			if err := c.guard.ValidateIdentifier(col); err != nil {
				return fmt.Errorf("create table %s: index %s: %w", t.Name, idx.Name, err)
			}
		}
		if err := c.createIndexOn(ctx, exec, t.Name, idx.Name, idx.Columns, idx.Unique); err != nil {
			return fmt.Errorf("create table %s: %w", t.Name, err)
		}
	}
	return nil
}

// foreignKeyClause renders one FOREIGN KEY table constraint for a CREATE
// TABLE, validating every identifier it splices. The constraint name is
// optional — SQLite reads back none, and a Schema built from such a
// catalog carries "" — and an unnamed constraint is legal everywhere.
func (c *Client) foreignKeyClause(fk ForeignKey) (string, error) {
	if len(fk.Columns) == 0 || len(fk.RefColumns) == 0 {
		return "", fmt.Errorf("foreign key on %v: columns and refColumns must not be empty", fk.Columns)
	}
	if fk.Name != "" {
		if err := c.guard.ValidateIdentifier(fk.Name); err != nil {
			return "", fmt.Errorf("foreign key: %w", err)
		}
	}
	if err := c.guard.ValidateIdentifier(fk.RefTable); err != nil {
		return "", fmt.Errorf("foreign key: %w", err)
	}
	quoted := make([]string, len(fk.Columns))
	for i, col := range fk.Columns {
		if err := c.guard.ValidateIdentifier(col); err != nil {
			return "", fmt.Errorf("foreign key: %w", err)
		}
		quoted[i] = c.dialect.Quote(col)
	}
	quotedRef := make([]string, len(fk.RefColumns))
	for i, col := range fk.RefColumns {
		if err := c.guard.ValidateIdentifier(col); err != nil {
			return "", fmt.Errorf("foreign key: %w", err)
		}
		quotedRef[i] = c.dialect.Quote(col)
	}
	clause := ""
	if fk.Name != "" {
		clause = "CONSTRAINT " + c.dialect.Quote(fk.Name) + " "
	}
	clause += fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
		strings.Join(quoted, ", "), c.dialect.Quote(fk.RefTable), strings.Join(quotedRef, ", "))
	actions, err := referentialClauses(c.dialect, fk.Name, fk.OnDelete, fk.OnUpdate)
	if err != nil {
		return "", err
	}
	return clause + actions, nil
}

// dropIndex drops an index by name, as the dialect writes it
// (quarkdriver.ObjectDropper): DROP INDEX <name> by default — PostgreSQL,
// SQLite, Oracle — and DROP INDEX <name> ON <table> on MySQL, MariaDB and
// SQL Server.
func (c *Client) dropIndex(ctx context.Context, exec Executor, table, index string) error {
	ddl := ""
	if dr, ok := c.dialect.(quarkdriver.ObjectDropper); ok {
		ddl = dr.DropIndex(table, index)
	}
	if ddl == "" {
		ddl = "DROP INDEX " + c.dialect.Quote(index)
	}
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// dropForeignKey drops a foreign key by name, as the dialect writes it
// (quarkdriver.ObjectDropper): ALTER TABLE … DROP CONSTRAINT by default —
// PostgreSQL, SQL Server, Oracle — and DROP FOREIGN KEY on MySQL and
// MariaDB. A dialect that rebuilds its tables (SQLite) never gets here:
// applyOne routes it to the rebuild.
func (c *Client) dropForeignKey(ctx context.Context, exec Executor, table, fk string) error {
	if fk == "" {
		return fmt.Errorf("dropForeignKey: empty constraint name (SQLite inline FK?); cannot drop without rebuild")
	}
	ddl := ""
	if dr, ok := c.dialect.(quarkdriver.ObjectDropper); ok {
		ddl = dr.DropForeignKey(table, fk)
	}
	if ddl == "" {
		ddl = dropConstraint(c.dialect, table, fk)
	}
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// addCheck adds a CHECK constraint the SQL standard's way, which every
// engine that alters a table in place accepts:
// `ALTER TABLE ... ADD CONSTRAINT <name> CHECK (<expr>)` (MySQL 8.0.16+,
// MariaDB 10.2.1+). A dialect that rebuilds its tables (SQLite) never gets
// here: applyOne routes it to the rebuild.
func (c *Client) addCheck(ctx context.Context, exec Executor, table, name, expression string) error {
	ddl := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK %s",
		c.dialect.Quote(table), c.dialect.Quote(name), wrapExpressionInParens(expression))
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// dropCheck drops a CHECK constraint by name, as the dialect writes it
// (quarkdriver.ObjectDropper): ALTER TABLE … DROP CONSTRAINT by default —
// PostgreSQL, SQL Server, Oracle, MariaDB — and DROP CHECK on MySQL.
func (c *Client) dropCheck(ctx context.Context, exec Executor, table, name string) error {
	ddl := ""
	if dr, ok := c.dialect.(quarkdriver.ObjectDropper); ok {
		ddl = dr.DropCheck(table, name)
	}
	if ddl == "" {
		ddl = dropConstraint(c.dialect, table, name)
	}
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// wrapExpressionInParens ensures the CHECK expression is wrapped in
// at least one set of parens, matching what every engine expects in
// `ADD CONSTRAINT ... CHECK (...)`. The introspector strips the
// outer `CHECK ` keyword but preserves whatever paren depth the
// engine emitted, so the expression may already have parens — we
// don't double-wrap when the existing parens already balance across
// the entire string.
//
// Naïve `HasPrefix("(") && HasSuffix(")")` is INCORRECT: an
// expression like `(a > 0) AND (b < 0)` starts and ends with parens
// but the opening paren at position 0 doesn't pair with the closing
// paren at the end — so we'd emit `CHECK (a > 0) AND (b < 0)`,
// which is a SQL syntax error in every engine Quark supports. The
// balanced-paren walk catches this case correctly.
func wrapExpressionInParens(expr string) string {
	trim := strings.TrimSpace(expr)
	if isFullyParenthesised(trim) {
		return trim
	}
	return "(" + trim + ")"
}

// isFullyParenthesised reports whether `s` begins with `(` and the
// matching `)` is at the very end of the string. Returns false for
// the empty string, for strings that don't start with `(`, and for
// strings where the opening paren's match closes before the end
// (e.g. `(a) AND (b)`). Quotes inside the expression are tracked so
// `(a = ')')` is handled correctly.
func isFullyParenthesised(s string) bool {
	if len(s) < 2 || s[0] != '(' {
		return false
	}
	depth := 0
	inSingle := false
	inDouble := false
	for i, ch := range s {
		switch ch {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '(':
			if !inSingle && !inDouble {
				depth++
			}
		case ')':
			if !inSingle && !inDouble {
				depth--
				if depth == 0 {
					// The opening paren at index 0 has just been
					// closed. If we're at the last index, the
					// whole string is one balanced group.
					return i == len(s)-1
				}
			}
		}
	}
	return false
}
