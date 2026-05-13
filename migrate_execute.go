// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"strings"
)

// ApplyPlan executes the operations in `plan` against the database
// in the order they appear. It dispatches each op to the appropriate
// per-dialect DDL — column ops via the `Dialect.AlterTable*` helpers,
// index / FK / table ops via inline dispatch using the existing
// `Client.CreateIndex` / `Client.AddForeignKey` where applicable.
//
// **Transactional behaviour** (F3-4-tx):
//
//   - **PostgreSQL, MSSQL, SQLite** — DDL is transactional on these
//     engines. ApplyPlan opens a BEGIN, runs all ops, and COMMITs.
//     On ANY failure the transaction is rolled back, leaving the
//     schema in its pre-plan state. This is the safety net users
//     should rely on when running migrations against production.
//
//   - **MySQL, MariaDB, Oracle** — DDL implicitly commits the
//     current transaction on every statement, so wrapping is
//     pointless: a failure mid-plan still leaves the schema
//     partially applied, and the executor follows the old
//     no-transaction path (return the error with op index, let
//     the caller resume manually). The eventual F3-4-resumable
//     follow-up adds a `quark_migration_state` checkpoint table
//     for these engines so a manual resume can pick up where the
//     plan left off.
//
// The returned error carries the index of the op that failed and
// the op's String() rendering for debuggability, regardless of
// which path was taken.
//
// Operation-specific caveats:
//
//   - **OpAlterColumn** today only emits DDL for the Type change
//     via `Dialect.AlterTableAlterColumn`. Nullable and Default
//     deltas are NOT emitted as DDL yet — they're logged as TODO
//     and the column lands in the requested type but keeps its
//     old nullable/default. F3-3-execute-alter follow-up will
//     close this.
//   - **OpDropForeignKey on SQLite** returns `ErrUnsupportedFeature`
//     because SQLite doesn't support `ALTER TABLE DROP CONSTRAINT`;
//     a real drop would require a full table rebuild via the
//     12-step procedure documented in the SQLite manual. That
//     belongs to F3-3-execute-sqlite-rebuild, a separate
//     follow-up.
//   - **OpDropCheck on SQLite** has the same limitation as
//     OpDropForeignKey for the same reason — returns
//     `ErrUnsupportedFeature`.
//
// All other ops work uniformly across the 4 CI dialects + SQLite
// (for the supported subset).
func (c *Client) ApplyPlan(ctx context.Context, plan Plan) error {
	if supportsTransactionalDDL(c.dialect.Name()) {
		return c.applyPlanTx(ctx, plan)
	}
	return c.applyPlanNoTx(ctx, plan)
}

// TODO(F3-4-resumable): elevate to `Dialect.SupportsTransactionalDDL() bool`
// when the checkpoint path needs the same info — see TASKS.md
// §F3-4-resumable. For now a private function is enough.
//
// supportsTransactionalDDL reports whether the named dialect can
// run DDL inside a transaction with the usual all-or-nothing
// semantics. The list is empirically driven, not aspirational:
//
//   - **postgres**: full transactional DDL — including ALTER TABLE,
//     CREATE/DROP INDEX, ADD/DROP CONSTRAINT. ROLLBACK reverts every
//     DDL since BEGIN. PG's signature feature.
//   - **mssql**: most DDL is transactional. Notable exceptions
//     (CREATE DATABASE, CREATE FULLTEXT INDEX) are outside Quark's
//     migration surface.
//   - **sqlite**: DDL is transactional EXCEPT for VACUUM and a few
//     PRAGMA-driven cases. Our migration ops don't touch those.
//   - **mysql / mariadb**: NO — every DDL implicitly commits the
//     transaction. Wrapping is harmless but pointless.
//   - **oracle**: NO — same implicit-commit behaviour as MySQL.
//
// The dialect-name check is intentional rather than a method on
// the Dialect interface; F3-4-resumable will likely lift this to
// the interface once it needs the same info for the checkpoint
// path, but as a single private helper it's fine for now.
func supportsTransactionalDDL(dialect string) bool {
	switch dialect {
	case "postgres", "mssql", "sqlite":
		return true
	default:
		return false
	}
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
	for i, op := range plan.Ops {
		if err := c.applyOne(ctx, tx, op); err != nil {
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
// A mid-plan failure here leaves the schema partially applied.
func (c *Client) applyPlanNoTx(ctx context.Context, plan Plan) error {
	for i, op := range plan.Ops {
		if err := c.applyOne(ctx, c.db, op); err != nil {
			return fmt.Errorf("ApplyPlan: op %d (%s): %w", i, op.String(), err)
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
		ddl := c.dialect.AlterTableAddColumn(o.Table, o.Column.Name, o.Column.Type)
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
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("alter column: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.New.Name); err != nil {
			return fmt.Errorf("alter column: %w", err)
		}
		// F3-3-execute only emits DDL for Type changes. Nullable
		// and Default deltas need per-dialect ALTER syntax that
		// we don't expose via Dialect yet. Rather than silently
		// no-op (which would leave the schema drifted forever and
		// confuse the user re-running PlanMigration), fail loud
		// with ErrUnsupportedFeature so the caller knows the gap
		// is real. F3-3-execute-alter follow-up will close this.
		if normalizeType(o.Old.Type) == normalizeType(o.New.Type) {
			return fmt.Errorf("%w: OpAlterColumn for %s.%s: nullable/default-only changes need F3-3-execute-alter (only type changes are emitted today)",
				ErrUnsupportedFeature, o.Table, o.New.Name)
		}
		ddl := c.dialect.AlterTableAlterColumn(o.Table, o.New.Name, o.New.Type)
		_, err := exec.ExecContext(ctx, ddl)
		return err
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
		return c.dropForeignKey(ctx, exec, o.Table, o.ForeignKey)
	case OpAddCheck:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("add check: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Check.Name); err != nil {
			return fmt.Errorf("add check: %w", err)
		}
		return c.addCheck(ctx, exec, o.Table, o.Check.Name, o.Check.Expression)
	case OpDropCheck:
		if err := c.guard.ValidateIdentifier(o.Table); err != nil {
			return fmt.Errorf("drop check: %w", err)
		}
		if err := c.guard.ValidateIdentifier(o.Check); err != nil {
			return fmt.Errorf("drop check: %w", err)
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
// Index / FK / check creation are NOT folded into the CREATE TABLE
// emitted here; they come from subsequent ops in the plan
// (OpCreateIndex / OpAddForeignKey / OpAddCheck). This keeps the
// dispatch single-op-per-DDL — F3-4 transactional wrapper, when it
// lands, will batch them cleanly.
func (c *Client) applyCreateTable(ctx context.Context, exec Executor, t Table) error {
	if len(t.Columns) == 0 {
		return fmt.Errorf("applyCreateTable %q: table has no columns", t.Name)
	}
	if err := c.guard.ValidateIdentifier(t.Name); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
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
		piece := c.dialect.Quote(col.Name) + " " + col.Type
		if !col.Nullable {
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
	ddl := fmt.Sprintf("CREATE TABLE %s (\n  %s\n)", c.dialect.Quote(t.Name), strings.Join(cols, ",\n  "))
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// dropIndex renders the per-dialect DROP INDEX DDL. SQLite and PG
// use `DROP INDEX <name>`; MySQL/MariaDB and MSSQL require the
// table qualification `DROP INDEX <name> ON <table>`.
func (c *Client) dropIndex(ctx context.Context, exec Executor, table, index string) error {
	var ddl string
	switch c.dialect.Name() {
	case "sqlite", "postgres":
		ddl = fmt.Sprintf("DROP INDEX %s", c.dialect.Quote(index))
	case "mysql", "mariadb", "mssql":
		ddl = fmt.Sprintf("DROP INDEX %s ON %s", c.dialect.Quote(index), c.dialect.Quote(table))
	case "oracle":
		ddl = fmt.Sprintf("DROP INDEX %s", c.dialect.Quote(index))
	default:
		return fmt.Errorf("%w: dropIndex not implemented for dialect %s", ErrUnsupportedFeature, c.dialect.Name())
	}
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// dropForeignKey renders the per-dialect DROP FK DDL. SQLite does
// NOT support `ALTER TABLE DROP CONSTRAINT` — dropping an FK
// requires the 12-step table-rebuild procedure documented in the
// SQLite manual, which is out of scope for F3-3-execute. We return
// `ErrUnsupportedFeature` so the caller knows the gap is real and
// not a typo.
//
// PG / MSSQL use `ALTER TABLE ... DROP CONSTRAINT <name>`; MySQL /
// MariaDB use `ALTER TABLE ... DROP FOREIGN KEY <name>`. Oracle
// matches PG / MSSQL.
func (c *Client) dropForeignKey(ctx context.Context, exec Executor, table, fk string) error {
	if fk == "" {
		return fmt.Errorf("dropForeignKey: empty constraint name (SQLite inline FK?); cannot drop without rebuild")
	}
	var ddl string
	switch c.dialect.Name() {
	case "postgres", "mssql", "oracle":
		ddl = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", c.dialect.Quote(table), c.dialect.Quote(fk))
	case "mysql", "mariadb":
		ddl = fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s", c.dialect.Quote(table), c.dialect.Quote(fk))
	case "sqlite":
		return fmt.Errorf("%w: dropForeignKey on SQLite requires the 12-step table-rebuild procedure (F3-3-execute-sqlite-rebuild)", ErrUnsupportedFeature)
	default:
		return fmt.Errorf("%w: dropForeignKey not implemented for dialect %s", ErrUnsupportedFeature, c.dialect.Name())
	}
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// addCheck renders the per-dialect ADD CHECK DDL. PG / MSSQL /
// Oracle: `ALTER TABLE ... ADD CONSTRAINT <name> CHECK (<expr>)`.
// MySQL 8.0.16+ / MariaDB 10.2.1+ same. SQLite returns
// `ErrUnsupportedFeature` for the same reason as drop FK (no
// `ALTER TABLE ADD CONSTRAINT` — requires rebuild).
func (c *Client) addCheck(ctx context.Context, exec Executor, table, name, expression string) error {
	if c.dialect.Name() == "sqlite" {
		return fmt.Errorf("%w: addCheck on SQLite requires the 12-step table-rebuild procedure (F3-3-execute-sqlite-rebuild)", ErrUnsupportedFeature)
	}
	ddl := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK %s",
		c.dialect.Quote(table), c.dialect.Quote(name), wrapExpressionInParens(expression))
	_, err := exec.ExecContext(ctx, ddl)
	return err
}

// dropCheck renders the per-dialect DROP CHECK DDL. PG / MSSQL /
// Oracle: `ALTER TABLE ... DROP CONSTRAINT <name>`. MySQL 8.0.16+
// uses `ALTER TABLE ... DROP CHECK <name>`; MariaDB 10.2.1+ does
// too. SQLite returns `ErrUnsupportedFeature`.
func (c *Client) dropCheck(ctx context.Context, exec Executor, table, name string) error {
	var ddl string
	switch c.dialect.Name() {
	case "postgres", "mssql", "oracle":
		ddl = fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s", c.dialect.Quote(table), c.dialect.Quote(name))
	case "mysql", "mariadb":
		ddl = fmt.Sprintf("ALTER TABLE %s DROP CHECK %s", c.dialect.Quote(table), c.dialect.Quote(name))
	case "sqlite":
		return fmt.Errorf("%w: dropCheck on SQLite requires the 12-step table-rebuild procedure (F3-3-execute-sqlite-rebuild)", ErrUnsupportedFeature)
	default:
		return fmt.Errorf("%w: dropCheck not implemented for dialect %s", ErrUnsupportedFeature, c.dialect.Name())
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
