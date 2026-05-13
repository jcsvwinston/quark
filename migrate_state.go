// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// migrationStateTableName is the name of the catalog-side state
// table that records per-op application progress. Filtered out of
// `IntrospectSchema` by the `quark_*` exclusion patterns in each
// dialect's lister — won't appear in user plans.
const migrationStateTableName = "quark_migration_state"

// Hash returns a deterministic SHA-256 hex digest of the Plan's
// operation sequence. Used by [Client.ApplyPlan]'s resumable path
// to detect plan drift between runs: if the same plan_hash carries
// over from a partially-applied run, the resume can pick up where
// it left off; if the hash differs, the Plan has changed and the
// resume is unsafe (e.g. the user added a column to their model
// between runs).
//
// The digest input is the line-joined `op.String()` output of
// every op. Op.String() formats are documented as stable in the
// F3-3-core godoc, so the hash is reproducible across processes
// and binaries on the same Plan value. A change in any op's
// content — even cosmetic, like a renamed table — produces a new
// hash, which is the right safety boundary for resume.
//
// Empty plans hash to the sha256 of the empty string (a constant)
// so two empty plans compare equal.
func (p Plan) Hash() string {
	parts := make([]string, len(p.Ops))
	for i, op := range p.Ops {
		parts[i] = op.String()
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

// ensureMigrationStateTable creates `quark_migration_state` if it
// doesn't already exist. Idempotent — uses each dialect's
// CREATE TABLE IF NOT EXISTS (MSSQL uses an IF NOT EXISTS guard
// since it doesn't support the keyword). Called by the resumable
// ApplyPlan path before any op is applied; on engines that
// support transactional DDL (PG, MSSQL, SQLite) the resumable
// path isn't used, so this function isn't invoked there.
//
// Schema rationale:
//   - `plan_hash CHAR(64)` — sha256 hex digest of the plan.
//     Joined with `op_index` it's the primary key.
//   - `op_index INT` — position of the op in the plan (0-based).
//   - `op_string TEXT` — the op's String() rendering at the time
//     of recording, kept for debugging post-mortem. NOT used in
//     resume logic.
//   - `applied_at TIMESTAMP` — when the op landed. For audit.
//
// The table is filtered out of `IntrospectSchema` by the existing
// `quark_*` exclusion (per `dialect_introspection.go`), so it
// never appears in user plans.
func (c *Client) ensureMigrationStateTable(ctx context.Context, exec Executor) error {
	// Schema-text rendering varies per dialect for CHAR length,
	// TIMESTAMP keyword, and IF NOT EXISTS support. Kept inline
	// per-dialect rather than via Dialect interface methods
	// because this is the only place migrate_state.go needs DDL
	// and the surface is tiny (one CREATE statement per engine).
	var ddl string
	switch c.dialect.Name() {
	case "mysql", "mariadb":
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			plan_hash CHAR(64) NOT NULL,
			op_index INT NOT NULL,
			op_string TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (plan_hash, op_index)
		)`, c.dialect.Quote(migrationStateTableName))
	case "mssql":
		// MSSQL has no `CREATE TABLE IF NOT EXISTS` — use the
		// sys.tables guard, same pattern as `migrator.go` uses for
		// its own CREATE TABLE on this dialect. Reached when a
		// caller manually opts MSSQL into the resumable path (it
		// shouldn't normally because supportsTransactionalDDL
		// returns true for MSSQL); kept explicit so a future
		// refactor that changes that classification doesn't emit
		// invalid DDL.
		ddl = fmt.Sprintf(`IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = '%s')
			CREATE TABLE %s (
				plan_hash CHAR(64) NOT NULL,
				op_index INT NOT NULL,
				op_string NVARCHAR(MAX) NOT NULL,
				applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (plan_hash, op_index)
			)`, migrationStateTableName, c.dialect.Quote(migrationStateTableName))
	case "postgres", "sqlite":
		// Same reasoning as MSSQL — these dialects use the tx
		// wrapper and don't normally reach this path, but the case
		// is here explicitly so a refactor doesn't fall to the
		// `default` and silently emit unsupported DDL.
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			plan_hash CHAR(64) NOT NULL,
			op_index INT NOT NULL,
			op_string TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (plan_hash, op_index)
		)`, c.dialect.Quote(migrationStateTableName))
	case "oracle":
		// Oracle has no CREATE TABLE IF NOT EXISTS — use the
		// "create and ignore ORA-00955" pattern that Migrate already
		// uses for its own CREATE TABLE.
		ddl = fmt.Sprintf(`CREATE TABLE %s (
			plan_hash CHAR(64) NOT NULL,
			op_index NUMBER(10) NOT NULL,
			op_string CLOB NOT NULL,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
			CONSTRAINT pk_%s PRIMARY KEY (plan_hash, op_index)
		)`, c.dialect.Quote(migrationStateTableName), migrationStateTableName)
	default:
		// Unknown dialect — fail loud rather than guess. The known
		// engines all have explicit cases above; landing here means
		// a new engine was added without updating this function.
		return fmt.Errorf("%w: ensureMigrationStateTable not implemented for dialect %s",
			ErrUnsupportedFeature, c.dialect.Name())
	}
	_, err := exec.ExecContext(ctx, ddl)
	if err != nil {
		// Oracle: ORA-00955 = table already exists. Idempotent.
		if c.dialect.Name() == "oracle" && strings.Contains(err.Error(), "ORA-00955") {
			return nil
		}
		return fmt.Errorf("ensureMigrationStateTable: %w", err)
	}
	return nil
}

// recordOpApplied inserts a row into `quark_migration_state` to
// mark that op `index` of the plan identified by `planHash` has
// been successfully applied. Called after each successful op in
// the resumable ApplyPlan path.
//
// We don't UPDATE on conflict — the resume path's contract is that
// once an op is recorded as applied, it stays applied. A retry
// that hits a duplicate-PK violation here is a bug in the resume
// detection, not something to silently overwrite.
func (c *Client) recordOpApplied(ctx context.Context, exec Executor, planHash string, opIndex int, opString string) error {
	p1, p2, p3 := c.dialect.Placeholder(1), c.dialect.Placeholder(2), c.dialect.Placeholder(3)
	ddl := fmt.Sprintf(`INSERT INTO %s (plan_hash, op_index, op_string) VALUES (%s, %s, %s)`,
		c.dialect.Quote(migrationStateTableName), p1, p2, p3)
	_, err := exec.ExecContext(ctx, ddl, planHash, opIndex, opString)
	if err != nil {
		return fmt.Errorf("recordOpApplied: %w", err)
	}
	return nil
}

// lastAppliedOpIndex returns the highest `op_index` that's been
// recorded as applied for the given `planHash`, or -1 if no rows
// exist for that hash. The caller uses `result + 1` as the
// resume-start index.
//
// A return value of -1 means "fresh start, no prior partial
// run"; a non-negative return means "resume from N+1".
func (c *Client) lastAppliedOpIndex(ctx context.Context, exec Executor, planHash string) (int, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(op_index), -1) FROM %s WHERE plan_hash = %s`,
		c.dialect.Quote(migrationStateTableName), c.dialect.Placeholder(1))
	row := exec.QueryRowContext(ctx, q, planHash)
	var idx int
	if err := row.Scan(&idx); err != nil {
		return 0, fmt.Errorf("lastAppliedOpIndex: %w", err)
	}
	return idx, nil
}
