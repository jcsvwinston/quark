// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"strings"
)

// backfillStateTableName is the catalog-side state table that
// records backfill resume tokens. Filtered out of `IntrospectSchema`
// by the `quark_*` exclusion patterns in each dialect's lister —
// won't appear in user plans.
const backfillStateTableName = "quark_backfill_state"

// BackfillSpec describes a single backfill operation. A backfill
// iterates a table by primary key in batches, invokes a user
// callback per batch, and persists the highest PK seen so a process
// kill (or a deliberate retry) resumes at the next un-processed
// row rather than re-running the entire table.
//
// The user callback receives the slice of PKs in the current batch
// and is responsible for whatever data work the backfill needs —
// the helper deliberately doesn't read row contents itself.
// Why: backfill SQL is rarely "SELECT * + transform"; it's usually
// "UPDATE ... WHERE id IN (...)" or "INSERT ... SELECT ... WHERE
// id IN (...)" where the user knows the relevant columns and the
// helper would just be in the way trying to scan them generically.
type BackfillSpec struct {
	// Name uniquely identifies this backfill across runs. Used as
	// the primary key of `quark_backfill_state`. Two backfills with
	// the same Name share the same resume token — that's how a
	// retry resumes. Different backfills MUST use different Names
	// or they'll trample each other's state.
	Name string

	// Table is the source table being iterated.
	Table string

	// PKColumn is the column the helper orders + paginates by.
	// Defaults to "id" if empty. The column must be a sortable
	// integer-like type (int64 / bigint) — text PKs aren't
	// supported in F3-6-core. Composite PKs aren't supported
	// either; the helper takes the first PK column only.
	PKColumn string

	// BatchSize is the number of PKs fetched per round-trip.
	// Defaults to 1000. Larger batches are more efficient but
	// hold locks longer; tune for your workload.
	BatchSize int

	// Process is invoked once per batch with the PK list in
	// ascending order. The callback can do any SQL it wants
	// (typically UPDATE ... WHERE id IN (...) or similar). Errors
	// abort the backfill and propagate; the state table records
	// the highest PK from successful batches, so a retry resumes
	// from there.
	Process func(ctx context.Context, batchPKs []int64) error
}

// Backfill iterates [BackfillSpec.Table] by primary key in batches,
// invoking [BackfillSpec.Process] for each batch. Persists the
// highest PK seen in `quark_backfill_state` so a kill / error /
// deliberate retry resumes at the next un-processed PK instead of
// re-running the entire table.
//
// Workflow when something goes wrong:
//
//  1. Backfill processes batches 0..N successfully; state.last_pk
//     records the max PK from batch N.
//  2. Batch N+1 fails (callback error, process killed, DB
//     connection lost, etc.). State remains at batch N's max.
//  3. Caller fixes the underlying issue and re-invokes Backfill
//     with the same Name. The helper reads state.last_pk and
//     starts from `WHERE pk > last_pk` — no re-processing of
//     batches 0..N.
//
// Once the helper sees an empty batch (no PKs left), the backfill
// is complete. A subsequent re-invocation with the same Name
// finds nothing to do and returns nil immediately — idempotent.
//
// Concurrency: like ApplyPlan's resumable path, Backfill is not
// safe for concurrent invocations against the same Name. Wrap
// with [Client.AcquireMigrationLock] if you need cross-process
// serialisation. The state table's primary key on Name prevents
// silent corruption — concurrent runs would race on the UPDATE,
// not produce divergent state.
func (c *Client) Backfill(ctx context.Context, spec BackfillSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("Backfill: Name is required")
	}
	if spec.Table == "" {
		return fmt.Errorf("Backfill: Table is required")
	}
	if spec.Process == nil {
		return fmt.Errorf("Backfill: Process callback is required")
	}
	pkCol := spec.PKColumn
	if pkCol == "" {
		pkCol = "id"
	}
	batchSize := spec.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	// Validate identifiers — Name is a constraint key in the state
	// table; Table and PKColumn are spliced into the iteration SQL.
	if err := c.guard.ValidateIdentifier(spec.Table); err != nil {
		return fmt.Errorf("Backfill: bad table name: %w", err)
	}
	if err := c.guard.ValidateIdentifier(pkCol); err != nil {
		return fmt.Errorf("Backfill: bad PK column name: %w", err)
	}

	if err := c.ensureBackfillStateTable(ctx); err != nil {
		return fmt.Errorf("Backfill: %w", err)
	}

	lastPK, err := c.readBackfillState(ctx, spec.Name)
	if err != nil {
		return fmt.Errorf("Backfill: %w", err)
	}

	for {
		pks, err := c.fetchBackfillBatch(ctx, spec.Table, pkCol, lastPK, batchSize)
		if err != nil {
			return fmt.Errorf("Backfill: fetch batch: %w", err)
		}
		if len(pks) == 0 {
			// Done.
			return nil
		}
		if err := spec.Process(ctx, pks); err != nil {
			return fmt.Errorf("Backfill: process batch (pk %d..%d): %w", pks[0], pks[len(pks)-1], err)
		}
		newLastPK := pks[len(pks)-1]
		if err := c.writeBackfillState(ctx, spec.Name, newLastPK); err != nil {
			return fmt.Errorf("Backfill: record state after pk %d: %w", newLastPK, err)
		}
		lastPK = newLastPK
	}
}

// ensureBackfillStateTable creates `quark_backfill_state` if it
// doesn't exist. Same per-dialect pattern as
// `ensureMigrationStateTable` — MSSQL uses sys.tables guard, Oracle
// swallows ORA-00955, others use CREATE TABLE IF NOT EXISTS.
//
// Schema:
//   - `name VARCHAR(255)` — the BackfillSpec.Name. PK.
//   - `last_pk BIGINT NOT NULL` — highest PK successfully processed.
//   - `updated_at TIMESTAMP` — audit only; not used for resume.
func (c *Client) ensureBackfillStateTable(ctx context.Context) error {
	var ddl string
	switch c.dialect.Name() {
	case "mysql", "mariadb":
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			name VARCHAR(255) NOT NULL PRIMARY KEY,
			last_pk BIGINT NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`, c.dialect.Quote(backfillStateTableName))
	case "mssql":
		ddl = fmt.Sprintf(`IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = '%s')
			CREATE TABLE %s (
				name NVARCHAR(255) NOT NULL PRIMARY KEY,
				last_pk BIGINT NOT NULL,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`, backfillStateTableName, c.dialect.Quote(backfillStateTableName))
	case "postgres", "sqlite":
		ddl = fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			name VARCHAR(255) NOT NULL PRIMARY KEY,
			last_pk BIGINT NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`, c.dialect.Quote(backfillStateTableName))
	case "oracle":
		ddl = fmt.Sprintf(`CREATE TABLE %s (
			name VARCHAR2(255) NOT NULL,
			last_pk NUMBER(19) NOT NULL,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL,
			CONSTRAINT pk_%s PRIMARY KEY (name)
		)`, c.dialect.Quote(backfillStateTableName), backfillStateTableName)
	default:
		return fmt.Errorf("%w: ensureBackfillStateTable not implemented for dialect %s",
			ErrUnsupportedFeature, c.dialect.Name())
	}
	_, err := c.db.ExecContext(ctx, ddl)
	if err != nil {
		if c.dialect.Name() == "oracle" && strings.Contains(err.Error(), "ORA-00955") {
			return nil
		}
		return fmt.Errorf("ensureBackfillStateTable: %w", err)
	}
	return nil
}

// readBackfillState returns the last_pk for the given backfill
// Name, or 0 if no row exists (fresh start; the next batch will
// be `WHERE pk > 0`, which is the entire table for positive PKs).
//
// PKs ≤ 0 are unusual but technically valid; for those a fresh
// backfill could miss negative-PK rows. Documented as a known
// limitation — F3-6 assumes positive integer PKs as the common
// case. Users with negative PKs should pre-seed state with the
// minimum desired starting point.
func (c *Client) readBackfillState(ctx context.Context, name string) (int64, error) {
	q := fmt.Sprintf(`SELECT COALESCE(MAX(last_pk), 0) FROM %s WHERE name = %s`,
		c.dialect.Quote(backfillStateTableName), c.dialect.Placeholder(1))
	row := c.db.QueryRowContext(ctx, q, name)
	var lastPK int64
	if err := row.Scan(&lastPK); err != nil {
		return 0, fmt.Errorf("readBackfillState: %w", err)
	}
	return lastPK, nil
}

// writeBackfillState upserts the (name, last_pk) row. Uses a
// portable two-step: try UPDATE first; if no row was affected,
// fall back to INSERT. The UPDATE-first ordering optimises the
// hot path (after the initial batch, every subsequent call is an
// UPDATE on an existing row — one round-trip). Only the very
// first batch of a new spec.Name pays the two-round-trip cost.
//
// Dialect-native UPSERT (MERGE / INSERT ... ON DUPLICATE KEY
// UPDATE / INSERT ... ON CONFLICT DO UPDATE) would be cleaner but
// would need per-dialect branching; the portable form works
// uniformly and the cost of one extra round-trip on FIRST insert
// is negligible for backfill cadences.
func (c *Client) writeBackfillState(ctx context.Context, name string, lastPK int64) error {
	// Try UPDATE first — most common case is "row exists, update it".
	updQ := fmt.Sprintf(`UPDATE %s SET last_pk = %s, updated_at = CURRENT_TIMESTAMP WHERE name = %s`,
		c.dialect.Quote(backfillStateTableName),
		c.dialect.Placeholder(1),
		c.dialect.Placeholder(2))
	res, err := c.db.ExecContext(ctx, updQ, lastPK, name)
	if err != nil {
		return fmt.Errorf("writeBackfillState update: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		// Some drivers (Oracle) don't report RowsAffected reliably;
		// fall through to INSERT and let the PK conflict tell us.
		rows = 0
	}
	if rows > 0 {
		return nil
	}
	// No row existed — INSERT.
	insQ := fmt.Sprintf(`INSERT INTO %s (name, last_pk) VALUES (%s, %s)`,
		c.dialect.Quote(backfillStateTableName),
		c.dialect.Placeholder(1),
		c.dialect.Placeholder(2))
	if _, err := c.db.ExecContext(ctx, insQ, name, lastPK); err != nil {
		return fmt.Errorf("writeBackfillState insert: %w", err)
	}
	return nil
}

// fetchBackfillBatch queries the next batch of PKs from `table`
// where `pk > lastPK`, ordered by PK ascending, limited to
// `batchSize`. Returns the PKs as an int64 slice; empty means
// done.
//
// LIMIT syntax varies — MSSQL uses TOP, Oracle uses FETCH NEXT.
// We use the per-dialect form rather than the (also-portable)
// row_number() over (...) trick because LIMIT/TOP is simpler and
// the helper isn't a hot path.
func (c *Client) fetchBackfillBatch(ctx context.Context, table, pkCol string, lastPK int64, batchSize int) ([]int64, error) {
	var q string
	switch c.dialect.Name() {
	case "mssql":
		q = fmt.Sprintf(`SELECT TOP (%d) %s FROM %s WHERE %s > %s ORDER BY %s ASC`,
			batchSize,
			c.dialect.Quote(pkCol),
			c.dialect.Quote(table),
			c.dialect.Quote(pkCol),
			c.dialect.Placeholder(1),
			c.dialect.Quote(pkCol))
	case "oracle":
		q = fmt.Sprintf(`SELECT %s FROM %s WHERE %s > %s ORDER BY %s ASC FETCH NEXT %d ROWS ONLY`,
			c.dialect.Quote(pkCol),
			c.dialect.Quote(table),
			c.dialect.Quote(pkCol),
			c.dialect.Placeholder(1),
			c.dialect.Quote(pkCol),
			batchSize)
	default:
		q = fmt.Sprintf(`SELECT %s FROM %s WHERE %s > %s ORDER BY %s ASC LIMIT %d`,
			c.dialect.Quote(pkCol),
			c.dialect.Quote(table),
			c.dialect.Quote(pkCol),
			c.dialect.Placeholder(1),
			c.dialect.Quote(pkCol),
			batchSize)
	}
	rows, err := c.db.QueryContext(ctx, q, lastPK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var pk int64
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}
