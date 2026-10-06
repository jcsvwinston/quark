// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// batchColDef maps a struct field to its quoted SQL column name and reflect index.
// Used internally by UpsertBatch and UpdateBatch.
type batchColDef struct {
	quoted string
	dbTag  string
	index  int
}

// batchChunkSize is the maximum number of primary key values per IN clause or rows
// per bulk statement. Oracle restricts IN lists to 1000 elements; using 1000 as a
// universal safe chunk size covers all supported dialects.
const batchChunkSize = 1000

// maxBatchBindParams is the fallback per-statement bind-parameter budget for
// bulk multi-row writes on dialects without a specific entry in
// batchBindParamCeiling (custom dialects, Oracle's rare multi-row paths).
// 2000 stays under SQL Server's ~2100 — the tightest documented ceiling —
// so it is safe everywhere.
const maxBatchBindParams = 2000

// batchBindParamCeiling returns the per-statement bind-parameter budget for
// bulk multi-row writes (CreateBatch, UpsertBatch) on the given dialect.
// Values sit slightly under each engine's documented hard limit so the
// statement's own overhead (and driver-added parameters) never tips it over:
//
//   - PostgreSQL: 65535 (uint16 parameter count in the extended protocol)
//   - MySQL/MariaDB: 65535 (2-byte num_params in COM_STMT_PREPARE)
//   - SQLite: 32766 (SQLITE_MAX_VARIABLE_NUMBER default since 3.32)
//   - SQL Server: ~2100 per sp_executesql call
//
// Oracle never builds multi-row statements (identity-sequence limitation —
// see CreateBatch/upsertBatchOracle) and takes the conservative fallback.
func batchBindParamCeiling(dialectName string) int {
	switch dialectName {
	case "postgres":
		return 65000
	case "mysql", "mariadb":
		return 65000
	case "sqlite":
		return 32000
	case "mssql":
		return 2000
	default:
		return maxBatchBindParams
	}
}

// queueOrRunAfterHook is the F5-4 dispatcher for `After*` hooks. It
// has two modes:
//
//   - When the Query is bound to an explicit transaction
//     (`q.tx != nil`, i.e. the caller used [ForTx]), the hook is
//     appended to the per-tx FIFO queue and the helper returns nil
//     immediately. [Tx.Commit] drains the queue after the database
//     confirms the commit; [Tx.Rollback] discards it. Any error
//     returned by the deferred hook is logged via the Client's
//     slog logger but cannot abort the already-committed work.
//
//   - When the Query is not bound to a transaction (the caller used
//     [For] against the Client directly), the hook is invoked
//     inline and its error is propagated to the CRUD caller.
//     Identical to the pre-F5-4 behaviour, so callers that never
//     touched explicit transactions see no semantic change.
//
// The dispatch is intentionally not "always queue, always
// post-commit" — opening an implicit transaction around every
// single-statement CRUD call adds two RPCs (BeginTx, Commit) and a
// connection pin per operation, and the safety it would buy is
// limited to a hook that wants to observe a rolled-back state
// (impossible in the no-tx case because there is no tx to roll
// back). The explicit-tx path is the one that produced the
// "after fired before commit" inconsistency in v0.x — that is the
// one F5-4 fixes.
func (q *BaseQuery) queueOrRunAfterHook(fn func() error) error {
	if q.tx != nil {
		q.tx.queueAfterHook(fn)
		return nil
	}
	return fn()
}

// atomically runs fn as one unit that either applies whole or not at all,
// for a write that spans several statements (UpdateBatch, UpsertBatch under
// RowLevelSecurityClient).
//
// On a query bound to a transaction ([ForTx]) the unit is a savepoint of
// that transaction ([Tx.Tx]): fn runs on the caller's connection, a failure
// rolls back to the savepoint — undoing fn's statements and the hooks they
// queued, and leaving the caller's transaction usable — and the caller's
// commit or rollback decides the rest. Otherwise the unit is a transaction
// of its own ([Client.Tx]).
//
// Never a transaction of its own inside the caller's (QK-57): that one runs
// on another connection of the pool, so it waits for the locks the caller
// holds — until the query timeout on SQLite, whose writer lock the caller
// has — and what it commits stays when the caller rolls back.
func (q *BaseQuery) atomically(ctx context.Context, fn func(tx *Tx) error) error {
	if q.tx != nil {
		return q.tx.Tx(ctx, fn)
	}
	return q.client.Tx(ctx, fn)
}

// emitEvent publishes a CRUD lifecycle [Event] to the Client's
// EventBus (F5-6), if one is configured. The timing mirrors the
// After* hook contract:
//
//   - Inside an explicit transaction (q.tx != nil) the publish is
//     registered via [Tx.OnCommit], so it runs after the commit is
//     durable and is discarded on rollback. A publish error there is
//     logged (event `quark.event.emit_failure`) but cannot propagate
//     — the commit already returned success.
//
//   - Outside a transaction the publish runs inline after the
//     statement and a failure is returned to the CRUD caller wrapped
//     in [ErrEventEmitFailed]. The write is already persisted; the
//     caller must NOT retry the write, only the emit (delivery is
//     at-least-once, no outbox — ADR-0013).
//
// Returns nil when no bus is configured (zero cost) or in the
// transactional path (the error, if any, is handled post-commit).
func (q *BaseQuery) emitEvent(kind string, entity any) error {
	bus := q.client.eventBus
	if bus == nil {
		return nil
	}
	ev := modelEvent{kind: kind, table: q.table, payload: entity}

	if q.tx != nil {
		q.tx.OnCommit(func(ctx context.Context) error {
			if err := bus.Publish(ctx, ev); err != nil && q.client.logger != nil {
				// Self-log the domain-specific failure and return nil
				// so the generic OnCommit drain does NOT also log it
				// (single quark.event.emit_failure line, not a
				// duplicate quark.hook.on_commit_error). The commit
				// already succeeded; per the F5-5 OnCommit contract
				// the error cannot propagate to the Client.Tx caller
				// regardless, so swallowing it after logging is the
				// honest, non-noisy choice.
				q.client.logger.Warn("event emit failed after commit",
					"event", "quark.event.emit_failure",
					"kind", kind, "table", q.table, "err", err)
			}
			return nil
		})
		return nil
	}

	// Non-transactional: emit inline. The write already executed; a
	// failure is surfaced to the caller wrapped in ErrEventEmitFailed
	// so it can distinguish "write failed" from "write OK, emit
	// failed".
	if err := bus.Publish(q.ctx, ev); err != nil {
		if q.client.logger != nil {
			q.client.logger.Warn("event emit failed",
				"event", "quark.event.emit_failure",
				"kind", kind, "table", q.table, "err", err)
		}
		return fmt.Errorf("%w: publish %s event for %s: %v",
			ErrEventEmitFailed, kind, q.table, err)
	}
	return nil
}

// executeExec runs an ExecContext through the execution seam, which reports
// it as an "EXEC". This is used for INSERT, UPDATE, DELETE operations.
//
// extraTags are additional invalidation tags emitted alongside q.table
// when the mutation succeeds. Callers that know the affected primary
// key (Update / UpdateFields / Tracked.Save / Delete by PK) pass
// `table:pk` so queries cached under that tag invalidate without
// blowing away every listing on the table (F4-6, per docs/playbooks/cache.md).
// Mutations that don't know the affected rows up-front (DeleteBatch
// WHERE-complex, raw Exec) pass nothing and fall back to the
// table-only invalidation that has been the historical default. The
// invalidation runs inside the chain, next to the engine (see stmt.write).
func (q *BaseQuery) executeExec(ctx context.Context, sqlStr string, args []any, extraTags ...string) (sql.Result, error) {
	if q.err != nil {
		return nil, q.err
	}
	return q.client.execStmt(ctx, q.exec, stmt{
		kind:    StatementExec,
		op:      "EXEC",
		table:   q.table,
		write:   true,
		rowTags: extraTags,
	}, sqlStr, args)
}

// isZeroPKValue checks if a primary key value is its zero value.
func isZeroPKValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.String:
		return v.String() == ""
	default:
		return false
	}
}

// isZeroCompositePK returns true when ALL pk columns are zero (i.e. the record is new).
func isZeroCompositePK(elem reflect.Value, pks []pkMeta) bool {
	for _, pk := range pks {
		if !isZeroPKValue(elem.Field(pk.Index)) {
			return false
		}
	}
	return true
}

// getPKValue returns the primary key value from a struct.
func getPKValue(v reflect.Value, pk pkMeta) any {
	return v.Field(pk.Index).Interface()
}

// setPKValue sets the primary key value on a struct.
func setPKValue(v reflect.Value, pk pkMeta, id int64) {
	field := v.Field(pk.Index)
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		field.SetInt(id)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		field.SetUint(uint64(id))
	}
}

// ensureTenantID stamps the resolved tenant on the entity's tenant field
// under RowLevelSecurityClient, before an INSERT or an UPDATE is built.
//
// It used to fill the field only when it was empty, so an entity that
// arrived carrying another tenant's id — from a request body, say — was
// inserted under that tenant, and an Update wrote it into the tenant column
// and moved the row out (QK-42). The router's tenant now wins, as it does
// for every other statement the query runs, and a foreign value is logged
// (event quark.tenant.foreign_value_replaced) rather than written.
func (q *BaseQuery) ensureTenantID(v reflect.Value) {
	if q.tenantID == "" || q.tenantCol == "" || q.meta == nil {
		return
	}
	fm, ok := q.meta.FieldByCol[strings.ToLower(q.tenantCol)]
	if !ok {
		return
	}
	field := v.Field(fm.Index)
	if field.Kind() != reflect.String || !field.CanSet() {
		return
	}
	if cur := field.String(); cur != "" && cur != q.tenantID {
		q.warnForeignTenantValue()
	}
	field.SetString(q.tenantID)
}

// saveAny persists an arbitrary struct to the database using its metadata.
// It handles recursive saving of associations if they are present.
//
// where is the condition list an UPDATE of this entity ANDs with its key
// (QK-40): the query's own conditions for the entity Update was called with,
// the tenant scope for an association (tenantScopeFor), nil for an insert.
// When the entity is updated and no row with its key satisfies where,
// nothing is written — not its row, not its associations — and saveAny
// returns errExcludedByWhere.
func (q *BaseQuery) saveAny(ctx context.Context, exec Executor, entity any, isUpdate bool, where []condition) (int64, error) {
	v := reflect.ValueOf(entity)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return 0, fmt.Errorf("entity must be a non-nil pointer")
	}
	elem := v.Elem()
	if elem.Kind() != reflect.Struct {
		return 0, fmt.Errorf("entity must be a struct")
	}

	meta := GetModelMetaByType(elem.Type())

	// Decide if we should Insert or Update.
	// If it's an update but ALL PKs are zero, it must be an insert (new record).
	actualUpdate := isUpdate
	if actualUpdate {
		if meta.HasCompositePK {
			if isZeroCompositePK(elem, meta.CompositePK) {
				actualUpdate = false
			}
		} else if isZeroPKValue(elem.Field(meta.PK.Index)) {
			actualUpdate = false
		}
	}

	// The query that writes the entity's own row. Built before the
	// associations so a guarded update can ask it first whether the row
	// passes the conditions (QK-40).
	dq := &BaseQuery{
		client:    q.client,
		ctx:       ctx,
		dialect:   q.dialect,
		guard:     q.guard,
		table:     meta.Table,
		pk:        meta.PK,
		exec:      exec,
		meta:      meta,
		tenantID:  q.tenantID,
		tenantCol: q.tenantCol,
		// schema must propagate so SchemaPerTenant writes hit the tenant's
		// schema, not the default search_path. Reads already honour q.schema
		// via fullTableName; without this, INSERT/UPDATE diverged from SELECT
		// and rows landed in the wrong schema (BB-8).
		schema: q.schema,
		// The conditions the UPDATE ANDs with the key (QK-40). Before, dq was
		// built with none, and Update wrote by the key alone whatever the
		// caller's Where said — another tenant's row included.
		where: where,
		err:   q.err,
	}
	guarded := actualUpdate && len(where) > 0

	// A belongs_to association is written BEFORE the entity's row, so the
	// UPDATE's own WHERE comes too late to keep it from being rewritten when
	// the conditions exclude the row. Ask first, only in that case.
	if guarded && !q.skipAssociations && hasLoadedBelongsTo(meta, elem) {
		passes, err := dq.keyPassesWhere(ctx, dq.pkValueOf(elem))
		if err != nil {
			return 0, err
		}
		if !passes {
			return 0, errExcludedByWhere
		}
	}

	// 1. Save BelongsTo associations FIRST (so we have their PKs).
	// Skipped entirely under WithoutAssociations (AQ-03): the caller asked
	// for a row-only write, so related records are never touched.
	belongsToRels := meta.Relations
	if q.skipAssociations {
		belongsToRels = nil
	}
	for _, rel := range belongsToRels {
		if rel.Type == "belongs_to" {
			field := elem.FieldByName(rel.Field)
			if !field.IsZero() {
				// Save related record
				relatedVal := field
				if relatedVal.Kind() != reflect.Ptr {
					relatedVal = field.Addr()
				}

				// Create a sub-query context for the related model, inheriting tenant info
				sq := &BaseQuery{
					client:    q.client,
					ctx:       ctx,
					dialect:   q.dialect,
					guard:     q.guard,
					table:     relMetaFromType(rel.RefType).Table,
					pk:        relMetaFromType(rel.RefType).PK,
					exec:      exec,
					meta:      relMetaFromType(rel.RefType),
					tenantID:  q.tenantID,
					tenantCol: q.tenantCol,
					schema:    q.schema,
					err:       q.err,
				}

				// The association is a by-key write of its own: it carries the
				// tenant scope when its model has the tenant column, and a row
				// of another tenant is left alone rather than rewritten.
				if _, err := sq.saveAny(ctx, exec, relatedVal.Interface(), actualUpdate, q.tenantScopeFor(relMetaFromType(rel.RefType))); err != nil && !errors.Is(err, errExcludedByWhere) {
					return 0, err
				}
				// Set foreign key on parent
				relMeta := GetModelMetaByType(rel.RefType)
				relPKVal := reflect.Indirect(field).Field(relMeta.PK.Index).Interface()

				if fm, ok := meta.FieldByCol[rel.JoinCol]; ok {
					parentFKField := elem.Field(fm.Index)
					if parentFKField.CanSet() {
						parentFKField.Set(reflect.ValueOf(relPKVal))
					}
				}
			}
		}
	}

	// 2. Save the main entity through dq.
	rowsAffected := int64(0)
	if actualUpdate {
		sqlStr, args, err := dq.buildUpdate(elem)
		if err != nil {
			return 0, err
		}
		// F4-6: pass the row tag so the same InvalidateTags call also
		// scopes to `<table>:<pk>`, in addition to the table tag.
		res, err := dq.executeExec(ctx, sqlStr, args, dq.rowTag(getPKValue(elem, meta.PK)))
		if err != nil {
			return 0, err
		}
		rowsAffected, _ = res.RowsAffected()

		// A guarded UPDATE that touched nothing: either no row with the key
		// satisfies the conditions — then nothing was written and nothing
		// more is (QK-40) — or the row passes them and something else held
		// the write back: the version predicate below, or, on MySQL and
		// MariaDB, values that were already there.
		if rowsAffected == 0 && guarded {
			passes, err := dq.keyPassesWhere(ctx, dq.pkValueOf(elem))
			if err != nil {
				return 0, err
			}
			if !passes {
				return 0, errExcludedByWhere
			}
		}

		// Optimistic locking: zero rows-affected when the model carries a
		// version column means the version predicate didn't match — another
		// writer bumped it after we loaded. Surface as ErrStaleEntity.
		// Otherwise bump the in-memory version so a subsequent Update on
		// the same struct sees the new value.
		if vfm := versionFieldOf(meta); vfm != nil {
			if rowsAffected == 0 {
				return 0, fmt.Errorf("%w: table %s pk=%v", ErrStaleEntity, meta.Table, getPKValue(elem, meta.PK))
			}
			bumpVersion(elem, vfm)
		}
	} else {
		sqlStr, args, err := dq.buildInsert(elem)
		if err != nil {
			return 0, err
		}

		if q.dialect.SupportsReturning() {
			if q.dialect.Name() == "oracle" {
				var id int64
				sqlWithOut := "BEGIN " + sqlStr + " INTO :ret_id; END;"
				_, err = dq.executeExec(ctx, sqlWithOut, append(args, sql.Named("ret_id", sql.Out{Dest: &id})))
				if err != nil {
					return 0, err
				}
				setPKValue(elem, meta.PK, id)
			} else {
				row := dq.executeQueryRow(ctx, sqlStr, args)
				if err := dq.scanReturning(row, elem); err != nil {
					return 0, err
				}
			}
			rowsAffected = 1
		} else {
			// Handle MSSQL/MySQL last id
			if q.dialect.Name() == "mssql" {
				if meta.HasCompositePK {
					// Composite PKs are user-supplied; SCOPE_IDENTITY() returns NULL.
					res, err := dq.executeExec(ctx, sqlStr, args)
					if err != nil {
						return 0, err
					}
					rowsAffected, _ = res.RowsAffected()
				} else {
					sqlBatch := sqlStr + "; " + q.dialect.LastInsertIDQuery(meta.Table, meta.PK.Column)
					// NullInt64, not int64, and the difference is not
					// cosmetic: the INSERT and SCOPE_IDENTITY() travel as ONE
					// batch, so when the INSERT is rejected the server still
					// returns a row for the SELECT — with NULL. Scanning that
					// into a plain int64 fails with "converting NULL to int64
					// is unsupported", and database/sql reports THAT conversion
					// error instead of the driver's, which is only delivered
					// once the scan itself succeeds. The engine's real message
					// —the unique violation, the foreign key, the check— was
					// being thrown away and replaced by a scan error that named
					// no constraint and no table. Scanning a nullable makes the
					// conversion succeed, so the driver's error surfaces and
					// stays classifiable.
					var lastID sql.NullInt64
					// wrapDBError for the same reason scanReturning wraps the
					// RETURNING path: without it the same duplicate insert
					// yields ErrConstraintViolation on PostgreSQL and a bare
					// driver error on SQL Server — one fact with two answers
					// depending on the engine underneath.
					err = wrapDBError(dq.executeQueryRow(ctx, sqlBatch, args).Scan(&lastID))
					if err != nil {
						return 0, err
					}
					if !lastID.Valid {
						// No error and no identity: the batch reported success
						// but the row is not there. Failing loudly beats
						// returning a zero PK the caller would use as real.
						return 0, fmt.Errorf("insert into %s reported success but SCOPE_IDENTITY() returned NULL", meta.Table)
					}
					setPKValue(elem, meta.PK, lastID.Int64)
					rowsAffected = 1
				}
			} else {
				res, err := dq.executeExec(ctx, sqlStr, args)
				if err != nil {
					return 0, err
				}
				// Only populate the PK from LastInsertId for single auto-generated PKs.
				// Composite PKs are always user-supplied; overwriting them would corrupt values.
				if q.dialect.SupportsLastInsertID() && !meta.HasCompositePK {
					lastID, _ := res.LastInsertId()
					setPKValue(elem, meta.PK, lastID)
				}
				rowsAffected, _ = res.RowsAffected()
			}
		}
	}

	// 3. For Inserts the PK was only revealed AFTER the exec populated it
	// (RETURNING, LastInsertId or scanReturning above). Invalidate the table
	// tag AND the fresh row tag here: the RETURNING / OUTPUT paths run through
	// executeQueryRow, which (unlike executeExec) invalidates nothing, so a
	// table-level cached read would otherwise go stale on Postgres / SQLite /
	// MariaDB / MSSQL. Idempotent on the executeExec (MySQL / Oracle) paths.
	if !actualUpdate && rowsAffected > 0 {
		dq.invalidateInsert(ctx, getPKValue(elem, meta.PK))
	}

	// 4. Save HasOne/HasMany associations AFTER — unless the caller opted
	// out with WithoutAssociations (AQ-03).
	if !q.skipAssociations {
		if err := dq.saveAssociations(elem, actualUpdate); err != nil {
			return rowsAffected, err
		}
	}

	return rowsAffected, nil
}

func relMetaFromType(t reflect.Type) *ModelMeta {
	return GetModelMetaByType(t)
}

// Create inserts a new record.
// The entity must have a db tag on fields to be persisted.
// Returns with the ID set from the database.
// Create inserts a new record and recursively saves associations.
func (q *Query[T]) Create(entity *T) error {
	// A query that failed to build — a tenant that did not resolve, a
	// strategy the dialect refuses — runs nothing, and runs no hook either.
	// Checked here, before validation and BeforeCreate, and again at the
	// executors: the write paths mint their own BaseQuery copies, and a copy
	// that forgot to carry err once let the statement through (QK-26). Every
	// mutator in this file opens the same way.
	if q.err != nil {
		return q.err
	}
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	// DX-9: the insert path needs the PK for RETURNING / LastInsertId; a
	// PK-less model used to die with "sql: no rows in result set", naming
	// neither the model nor the words "primary key".
	if q.pk.Column == "" {
		return fmt.Errorf("%w: model %T has no primary key — tag a field with pk:\"true\" or name a column db:\"id\"", ErrInvalidQuery, entity)
	}

	if err := q.client.Validate(q.ctx, entity); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	if hook, ok := any(entity).(BeforeCreateHook); ok {
		if err := hook.BeforeCreate(q.ctx); err != nil {
			return err
		}
	}

	// created_at/updated_at column convention (DX-20).
	if q.meta != nil {
		stampTimestamps(entity, q.meta, true)
	}

	// Operation-scoped ctx, the same pattern every other op in this file
	// uses. It is load-bearing under RowLevelSecurityNative: the implicit
	// transaction around INSERT … RETURNING commits when THIS ctx ends, so
	// the ctx must end with the operation. Passing q.ctx unwrapped tied the
	// commit to the caller's ctx instead — with a long-lived ctx (batch job
	// on context.Background(), CLI), every Create left one transaction
	// idle-in-transaction holding its connection and ACCESS SHARE locks
	// until the ctx died, any later DDL on the table blocked behind them,
	// and the write stayed invisible to reads in the same ctx (issue #252;
	// reproduced against PostgreSQL by
	// TestRowLevelSecurityNativeCreateReleasesImplicitTx). The wrapped ctx
	// flows through the whole saveAny cascade (associations included) via
	// the BaseQuery it builds.
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	if _, err := q.saveAny(ctx, q.exec, entity, false, nil); err != nil {
		return err
	}

	if hook, ok := any(entity).(AfterCreateHook); ok {
		if err := q.queueOrRunAfterHook(func() error { return hook.AfterCreate(q.ctx) }); err != nil {
			return err
		}
	}

	if err := q.recordAudit(q.ctx, eventCreated, entity); err != nil {
		return err
	}

	return q.emitEvent(eventCreated, entity)
}

// buildInsert constructs the INSERT SQL.
func (q *BaseQuery) buildInsert(v reflect.Value) (string, []any, error) {
	t := v.Type()
	q.ensureTenantID(v) // Inject tenant ID BEFORE processing fields

	var columns []string
	var placeholders []string
	var args []any
	argIndex := 1

	// Fast path: a generated INSERT binder (F6-3a) produces the (columns,
	// args) without reflection. Used only when the per-column timezone
	// feature is inactive (the binder emits raw values, matching
	// bindColumnArg's pass-through), the value is addressable (so we can hand
	// the binder the *T), and a compatible binder handles BindInsert. The
	// stub binder and the not-yet-generated BindUpdate return ErrGeneratedStub,
	// so the reflection loop below runs unchanged in every other case.
	// tenant injection and SQL assembly happen the same way afterwards.
	gathered := false
	if !q.tzActive() && v.CanAddr() {
		if bind, ok := lookupTypedBinder(t); ok {
			if rawCols, rawArgs, berr := bind(v.Addr().Interface(), BindInsert); berr == nil {
				for i, col := range rawCols {
					if err := q.guard.ValidateIdentifier(col); err != nil {
						return "", nil, err
					}
					columns = append(columns, q.dialect.Quote(col))
					placeholders = append(placeholders, q.dialect.Placeholder(argIndex))
					args = append(args, rawArgs[i])
					argIndex++
				}
				gathered = true
			}
		}
	}

	if !gathered {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			dbTag := columnFromDBTag(field.Tag.Get("db"))
			if dbTag == "" || dbTag == "-" {
				continue // Skip fields without db tag
			}

			// Skip PK columns that are zero (let DB assign auto-increment).
			// For composite PKs all columns must be included since they are not auto-generated.
			if !q.meta.HasCompositePK && i == q.pk.Index && isZeroPKValue(v.Field(i)) {
				continue
			}

			if err := q.guard.ValidateIdentifier(dbTag); err != nil {
				return "", nil, err
			}

			columns = append(columns, q.dialect.Quote(dbTag))
			placeholders = append(placeholders, q.dialect.Placeholder(argIndex))
			args = append(args, q.bindColumnArg(dbTag, v.Field(i).Interface()))
			argIndex++
		}
	}

	// Auto-inject tenant ID if needed (only if not already in columns)
	if q.tenantCol != "" {
		// Check if it's already in the columns
		found := false
		for _, col := range columns {
			// Compare lowercase and unquoted to avoid duplicates across dialects (MySQL, Oracle, etc)
			cleanCol := strings.Trim(strings.ToLower(col), "`'\"[]")
			if cleanCol == strings.ToLower(q.tenantCol) {
				found = true
				break
			}
		}
		if !found {
			if fm, ok := q.meta.FieldByCol[q.tenantCol]; ok {
				columns = append(columns, q.dialect.Quote(q.tenantCol))
				placeholders = append(placeholders, q.dialect.Placeholder(argIndex))
				args = append(args, q.bindColumnArg(q.tenantCol, v.Field(fm.Index).Interface()))
				argIndex++
			}
		}
	}

	var sqlStr strings.Builder
	sqlStr.WriteString("INSERT INTO ")
	sqlStr.WriteString(q.fullTableName())
	sqlStr.WriteString(" (")
	sqlStr.WriteString(strings.Join(columns, ", "))
	sqlStr.WriteString(") VALUES (")
	sqlStr.WriteString(strings.Join(placeholders, ", "))
	sqlStr.WriteString(")")

	// Add RETURNING if supported — use detected PK column
	if q.dialect.SupportsReturning() && q.pk.Column != "" {
		sqlStr.WriteString(" ")
		sqlStr.WriteString(q.dialect.Returning(q.pk.Column))
	}

	return sqlStr.String(), args, nil
}

// scanReturning scans RETURNING clause results into the entity's PK field.
func (q *BaseQuery) scanReturning(row *sql.Row, v reflect.Value) error {
	pkField := v.Field(q.pk.Index)

	if pkField.CanAddr() {
		return wrapDBError(row.Scan(pkField.Addr().Interface()))
	}

	// Fallback: scan into a temporary and set
	var id int64
	if err := row.Scan(&id); err != nil {
		return wrapDBError(err)
	}
	setPKValue(v, q.pk, id)
	return nil
}

// Update updates the entity by its primary key with partial-update semantics:
// only fields whose value is non-zero for their type are written.
//
// CAUTION — zero-value trap: because zero values are skipped, calling Update
// cannot write false to a bool, 0 to an integer, "" to a string, or nil to a
// pointer/slice/map. This is the documented semantics of Update on the v1
// line and is not scheduled to change within it. To write a zero value
// explicitly, use UpdateFields or UpdateMap; to write exactly the fields that
// changed since a read, load the entity with [Query.Track] and call
// [Tracked.Save], which diffs against the snapshot and writes zero values.
// When Update skips a scalar zero (false / 0 / ""), it logs a WARN so callers
// notice the silent skip; skipped nil pointers/slices/maps are the expected
// "absent" case and do not warn.
//
// The query's conditions — its Where calls, and the tenant predicate under
// RowLevelSecurityClient — are ANDed with the key, so they can only narrow
// which row is written (QK-40): Where("tenant_id", "=", t).Update(&e) writes
// e's row only when it belongs to t. When no row with the key satisfies them,
// Update writes nothing, returns (0, nil), and runs no AfterUpdate hook,
// audit entry or event. On a model with a version column, ErrStaleEntity
// means the row satisfies the conditions and its version moved. An entity
// whose key is zero is inserted, as before; the conditions do not apply to
// an insert. Returns the number of rows affected.
//
// CAUTION — recursive association save (AQ-03): Update recursively saves
// every loaded association, exactly like Create. An entity read with
// Find + Preload carries its children in memory, so updating one scalar
// field ALSO re-writes every loaded child from that in-memory snapshot —
// silently overwriting concurrent changes to those children, and without
// the extra writes showing in the returned rows-affected count. When
// associations are about to be written, Update logs a WARN naming them.
// Call [Query.WithoutAssociations] to write only the entity's own row.
func (q *Query[T]) Update(entity *T) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	// AQ-03: make the recursive save visible before it happens. A loaded
	// (non-zero) relation field is about to be re-written from the
	// in-memory snapshot; name the associations and the opt-out.
	if !q.skipAssociations && q.meta != nil && len(q.meta.Relations) > 0 {
		v := reflect.ValueOf(entity).Elem()
		var loaded []string
		for name, rel := range q.meta.Relations {
			f := v.FieldByName(rel.Field)
			if f.IsValid() && !f.IsZero() {
				loaded = append(loaded, name)
			}
		}
		if len(loaded) > 0 {
			sort.Strings(loaded)
			q.client.logger.Warn("Update will recursively save the loaded associations from their in-memory snapshot — concurrent changes to those rows will be overwritten. Use WithoutAssociations() to write only this entity's row.",
				"event", "quark.update.recursive_association_save",
				"table", q.table,
				"associations", strings.Join(loaded, ", "),
			)
		}
	}

	if hook, ok := any(entity).(BeforeUpdateHook); ok {
		if err := hook.BeforeUpdate(q.ctx); err != nil {
			return 0, err
		}
	}

	// created_at/updated_at column convention (DX-20).
	if q.meta != nil {
		stampTimestamps(entity, q.meta, false)
	}

	// Operation-scoped ctx — see the twin comment in Create. Update's
	// fallback branch for a zero PK is an INSERT … RETURNING, which under
	// RowLevelSecurityNative runs in an implicit transaction that commits
	// when this ctx ends; unwrapped, that meant "when the caller's ctx
	// ends" (issue #252). The pure-UPDATE branch commits synchronously and
	// only gains the standard per-operation timeout bound.
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	rowsAffected, err := q.saveAny(ctx, q.exec, entity, true, q.where)
	if errors.Is(err, errExcludedByWhere) {
		// No row with the key satisfies the conditions: nothing was
		// written, so there is nothing for an After hook, the audit log or
		// the event bus to report (QK-40).
		return 0, nil
	}
	if err != nil {
		return rowsAffected, err
	}

	if hook, ok := any(entity).(AfterUpdateHook); ok {
		if err := q.queueOrRunAfterHook(func() error { return hook.AfterUpdate(q.ctx) }); err != nil {
			return rowsAffected, err
		}
	}

	if err := q.recordAudit(q.ctx, eventUpdated, entity); err != nil {
		return rowsAffected, err
	}
	if err := q.emitEvent(eventUpdated, entity); err != nil {
		return rowsAffected, err
	}
	return rowsAffected, nil
}

// UpdateFields updates only the named fields on the entity, bypassing the
// zero-value filter that Update applies. This is the recommended API when
// you need to write false / 0 / "" / nil to a column — values that Update
// would silently skip.
//
// fields are matched against struct field db tags only — the same identifier
// resolution as Update and Find. Listing a struct field name without a db tag
// returns ErrInvalidQuery: there is one canonical name per column and we
// don't accept aliases here, to keep the resolution unambiguous.
//
// The primary key is never overwritten; listing a PK column returns an
// error. If the client is configured with the RowLevelSecurityClient tenant
// strategy, the tenant column is injected before the SET clause is built;
// callers do not need to (and should not) list it explicitly.
//
// Example:
//
//	user := User{ID: 42, Active: false}
//	rows, err := quark.For[User](ctx, client).UpdateFields(&user, "active")
//	// emitted: UPDATE "users" SET "active" = $1 WHERE "id" = $2  args=[false, 42]
//
// The query's conditions are ANDed with the key, as in Update: when no row
// with the key satisfies them, UpdateFields writes nothing, returns (0, nil)
// and runs no AfterUpdate hook, audit entry or event (QK-40).
//
// Returns the number of rows affected.
func (q *Query[T]) UpdateFields(entity *T, fields ...string) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if len(fields) == 0 {
		return 0, fmt.Errorf("%w: UpdateFields requires at least one field name", ErrInvalidQuery)
	}

	if hook, ok := any(entity).(BeforeUpdateHook); ok {
		if err := hook.BeforeUpdate(q.ctx); err != nil {
			return 0, err
		}
	}

	// created_at/updated_at column convention (DX-20).
	if q.meta != nil {
		stampTimestamps(entity, q.meta, false)
	}

	v := reflect.ValueOf(entity).Elem()
	q.ensureTenantID(v)

	// db-tag-only lookup. We deliberately do not register the struct field
	// name as an alias: the rest of the ORM (Update, Find, Where) resolves
	// columns by db tag, and accepting both creates ambiguity if a field's
	// db tag happens to collide with another field's struct name (silent
	// last-write-wins on the map insert).
	t := v.Type()
	idxByName := make(map[string]int, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		fld := t.Field(i)
		dbTag := columnFromDBTag(fld.Tag.Get("db"))
		if dbTag == "" || dbTag == "-" {
			continue
		}
		idxByName[dbTag] = i
	}

	// Skip PK columns from the SET clause regardless of whether the caller
	// listed them — overwriting the PK is never the intent and would corrupt
	// the row's identity.
	pkCols := map[string]struct{}{}
	if q.meta.HasCompositePK {
		for _, cpk := range q.meta.CompositePK {
			pkCols[cpk.Column] = struct{}{}
		}
	} else if q.pk.Column != "" {
		pkCols[q.pk.Column] = struct{}{}
	}

	var setClauses []string
	var args []any
	argIndex := 1

	for _, name := range fields {
		idx, ok := idxByName[name]
		if !ok {
			return 0, fmt.Errorf("%w: UpdateFields: unknown field %q on %s", ErrInvalidQuery, name, t.Name())
		}
		dbTag := columnFromDBTag(t.Field(idx).Tag.Get("db"))
		if dbTag == "" {
			dbTag = name
		}
		if _, isPK := pkCols[dbTag]; isPK {
			return 0, fmt.Errorf("%w: UpdateFields: cannot overwrite primary key column %q", ErrInvalidQuery, dbTag)
		}
		if err := q.guard.ValidateIdentifier(dbTag); err != nil {
			return 0, err
		}
		// Safe Sprintf: dbTag is validated by the guard above, and
		// dialect.Placeholder emits only literal placeholder syntax.
		setClauses = append(setClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(dbTag), q.dialect.Placeholder(argIndex)))
		args = append(args, q.bindColumnArg(dbTag, v.Field(idx).Interface()))
		argIndex++
	}

	// Optimistic-locking SET (version = version + 1). Same shape as Update:
	// append after the user-named columns so the placeholder indices for the
	// regular fields don't shift.
	if vfm := versionFieldOf(q.meta); vfm != nil {
		if err := q.guard.ValidateIdentifier(vfm.Column); err != nil {
			return 0, err
		}
		quoted := q.dialect.Quote(vfm.Column)
		setClauses = append(setClauses, fmt.Sprintf("%s = %s + 1", quoted, quoted))
	}

	var sqlBuf strings.Builder
	sqlBuf.WriteString("UPDATE ")
	sqlBuf.WriteString(q.fullTableName())
	sqlBuf.WriteString(" SET ")
	sqlBuf.WriteString(strings.Join(setClauses, ", "))
	sqlBuf.WriteString(" WHERE ")

	if q.meta.HasCompositePK {
		for j, cpk := range q.meta.CompositePK {
			if j > 0 {
				sqlBuf.WriteString(" AND ")
			}
			sqlBuf.WriteString(q.dialect.Quote(cpk.Column))
			sqlBuf.WriteString(" = ")
			sqlBuf.WriteString(q.dialect.Placeholder(argIndex))
			args = append(args, v.Field(cpk.Index).Interface())
			argIndex++
		}
	} else {
		if q.pk.Column == "" {
			return 0, fmt.Errorf("%w: UpdateFields requires a primary key", ErrInvalidModel)
		}
		sqlBuf.WriteString(q.dialect.Quote(q.pk.Column))
		sqlBuf.WriteString(" = ")
		sqlBuf.WriteString(q.dialect.Placeholder(argIndex))
		args = append(args, getPKValue(v, q.pk))
		argIndex++
	}

	// Optimistic-locking predicate: AND version = <loaded_version>.
	if vfm := versionFieldOf(q.meta); vfm != nil {
		sqlBuf.WriteString(" AND ")
		sqlBuf.WriteString(q.dialect.Quote(vfm.Column))
		sqlBuf.WriteString(" = ")
		sqlBuf.WriteString(q.dialect.Placeholder(argIndex))
		args = append(args, readVersion(v, vfm))
		argIndex++
	}

	// The caller's Where() conditions narrow the key predicate, rendered by
	// the same renderer as a SELECT (QK-39).
	whereSQL, whereArgs, err := q.whereForWrite(argIndex)
	if err != nil {
		return 0, err
	}
	if whereSQL != "" {
		sqlBuf.WriteString(" AND ")
		sqlBuf.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	// F4-6: pass row tag (no-op for composite PKs — see rowTag).
	var pkTag string
	if !q.meta.HasCompositePK {
		pkTag = q.rowTag(getPKValue(v, q.pk))
	}
	result, err := q.executeExec(ctx, sqlBuf.String(), args, pkTag)
	if err != nil {
		return 0, fmt.Errorf("UpdateFields failed: %w", err)
	}
	rowsAffected := int64(0)
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
	}

	// Nothing written under conditions: when no row with the key satisfies
	// them, report zero rows and run no After hook, audit entry or event
	// (QK-40). Before, a versioned model reported ErrStaleEntity here — a
	// version conflict — for a row the caller's own Where had left out.
	if rowsAffected == 0 && len(q.where) > 0 {
		passes, err := q.keyPassesWhere(ctx, q.pkValueOf(v))
		if err != nil {
			return 0, fmt.Errorf("UpdateFields failed: %w", err)
		}
		if !passes {
			return 0, nil
		}
	}

	// Optimistic locking: stale → ErrStaleEntity. Otherwise bump in memory.
	if vfm := versionFieldOf(q.meta); vfm != nil {
		if rowsAffected == 0 {
			return 0, fmt.Errorf("%w: table %s pk=%v", ErrStaleEntity, q.meta.Table, getPKValue(v, q.pk))
		}
		bumpVersion(v, vfm)
	}

	if hook, ok := any(entity).(AfterUpdateHook); ok {
		if err := q.queueOrRunAfterHook(func() error { return hook.AfterUpdate(q.ctx) }); err != nil {
			return rowsAffected, err
		}
	}
	if err := q.recordAudit(q.ctx, eventUpdated, entity); err != nil {
		return rowsAffected, err
	}
	if err := q.emitEvent(eventUpdated, entity); err != nil {
		return rowsAffected, err
	}
	return rowsAffected, nil
}

// UpdateMap updates fields using a map (for partial updates without full entity).
// Requires Where clause for safety.
//
// Under RowLevelSecurityClient a map that names the tenant column writes the
// resolved tenant into it, whatever value the map holds (QK-44); a different
// value is logged (event quark.tenant.foreign_value_replaced), as Create and
// the updates by entity do. The caller's map is not modified.
// Returns the number of rows affected.
func (q *Query[T]) UpdateMap(data map[string]any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if len(data) == 0 {
		return 0, fmt.Errorf("%w: no fields to update", ErrInvalidQuery)
	}

	// Require WHERE clause for safety — validate BEFORE building SQL
	if len(q.where) == 0 {
		return 0, fmt.Errorf("%w: UpdateMap requires Where clause to prevent accidental full table update", ErrInvalidQuery)
	}

	// Under RowLevelSecurityClient the tenant column holds the resolved
	// tenant whatever the map says (QK-44).
	data = q.confineTenantColumn(data)

	// Build UPDATE from map
	sql, args, err := q.buildUpdateMap(data)
	if err != nil {
		return 0, err
	}

	// Execute with timeout
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	result, err := q.executeExec(ctx, sql, args)

	if err != nil {
		return 0, fmt.Errorf("update failed: %w", err)
	}

	rowsAffected := int64(0)
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
	}

	return rowsAffected, nil
}

// buildUpdate constructs UPDATE SQL from entity (partial update of non-zero fields).
// Merges PK-based WHERE with any additional Where() conditions from the builder.
func (q *BaseQuery) buildUpdate(v reflect.Value) (string, []any, error) {
	t := v.Type()
	q.ensureTenantID(v) // Inject tenant ID BEFORE processing fields

	var setClauses []string
	var skippedZero []string
	var args []any
	argIndex := 1

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		dbTag := columnFromDBTag(field.Tag.Get("db"))
		if dbTag == "" || dbTag == "-" {
			continue
		}

		// Skip primary key column(s) in SET clause.
		if q.meta.HasCompositePK {
			isPKCol := false
			for _, cpk := range q.meta.CompositePK {
				if i == cpk.Index {
					isPKCol = true
					break
				}
			}
			if isPKCol {
				continue
			}
		} else if i == q.pk.Index {
			continue
		}

		fieldValue := v.Field(i)

		// Skip the optimistic-locking version column from the normal SET
		// path — it gets a dedicated "version = version + 1" assignment
		// below, and a "AND version = ?" predicate in WHERE.
		if vfm := versionFieldOf(q.meta); vfm != nil && vfm.Index == i {
			continue
		}

		// Skip zero values (partial update). A skipped *scalar* zero
		// (false / 0 / "") is the P0-4 trap worth a WARN below: the caller
		// may have meant to persist it. A nil pointer/slice/map is the
		// idiomatic "absent / not applicable" case (e.g. deleted_at on every
		// soft-delete model), so it is skipped silently — warning on it is
		// just noise. Either way the field is omitted from the SET clause.
		if isZeroValue(fieldValue) {
			if isWarnableZero(fieldValue) {
				skippedZero = append(skippedZero, dbTag)
			}
			continue
		}

		if err := q.guard.ValidateIdentifier(dbTag); err != nil {
			return "", nil, err
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(dbTag), q.dialect.Placeholder(argIndex)))
		args = append(args, q.bindColumnArg(dbTag, fieldValue.Interface()))
		argIndex++
	}

	// If the model carries quark:"version", include the version-bump in the
	// SET clause. Done after the field loop so it's append-only and doesn't
	// shift placeholder indices for the regular columns.
	if vfm := versionFieldOf(q.meta); vfm != nil {
		if err := q.guard.ValidateIdentifier(vfm.Column); err != nil {
			return "", nil, err
		}
		quoted := q.dialect.Quote(vfm.Column)
		setClauses = append(setClauses, fmt.Sprintf("%s = %s + 1", quoted, quoted))
	}

	if len(skippedZero) > 0 && q.client != nil && q.client.logger != nil {
		q.client.logger.Warn(
			"Update skipped zero-value fields; use UpdateFields(entity, ...) or UpdateMap to write false / 0 / \"\" explicitly",
			"table", q.table,
			"skipped", skippedZero,
		)
	}

	if len(setClauses) == 0 {
		return "", nil, fmt.Errorf("%w: no non-zero fields to update", ErrInvalidQuery)
	}

	var sql strings.Builder
	sql.WriteString("UPDATE ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" SET ")
	sql.WriteString(strings.Join(setClauses, ", "))
	sql.WriteString(" WHERE ")

	// Build WHERE clause: composite or single PK
	if q.meta.HasCompositePK {
		for j, cpk := range q.meta.CompositePK {
			if j > 0 {
				sql.WriteString(" AND ")
			}
			sql.WriteString(q.dialect.Quote(cpk.Column))
			sql.WriteString(" = ")
			sql.WriteString(q.dialect.Placeholder(argIndex))
			args = append(args, v.Field(cpk.Index).Interface())
			argIndex++
		}
	} else {
		sql.WriteString(q.dialect.Quote(q.pk.Column))
		sql.WriteString(" = ")
		sql.WriteString(q.dialect.Placeholder(argIndex))
		args = append(args, getPKValue(v, q.pk))
		argIndex++
	}

	// Optimistic-locking predicate: AND version = <loaded_version>. The
	// caller must check rows-affected and surface ErrStaleEntity on zero;
	// buildUpdate is the SQL builder, not the executor.
	if vfm := versionFieldOf(q.meta); vfm != nil {
		sql.WriteString(" AND ")
		sql.WriteString(q.dialect.Quote(vfm.Column))
		sql.WriteString(" = ")
		sql.WriteString(q.dialect.Placeholder(argIndex))
		args = append(args, readVersion(v, vfm))
		argIndex++
	}

	// Merge any additional Where() conditions, rendered by the same
	// renderer as a SELECT (QK-39).
	whereSQL, whereArgs, err := q.whereForWrite(argIndex)
	if err != nil {
		return "", nil, err
	}
	if whereSQL != "" {
		sql.WriteString(" AND ")
		sql.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	return sql.String(), args, nil
}

// buildUpdateMap constructs UPDATE SQL from map.
// Keys are sorted for deterministic query generation.
func (q *BaseQuery) buildUpdateMap(data map[string]any) (string, []any, error) {
	// Sort keys for deterministic SQL output
	keys := make([]string, 0, len(data))
	for col := range data {
		keys = append(keys, col)
	}
	sort.Strings(keys)

	var setClauses []string
	var args []any
	argIndex := 1

	for _, col := range keys {
		val := data[col]
		if err := q.guard.ValidateIdentifier(col); err != nil {
			return "", nil, err
		}

		// An Expr value is SQL, not data: `stock = stock - 1` has to reach
		// the statement as an expression over the column's current value.
		// Before this it was handed to database/sql as a driver argument,
		// which failed with "unsupported type quark.colExpr, a struct" —
		// and the read-modify-write alternative is not equivalent, because
		// it loses the atomicity that is the reason to write it in SQL.
		if e, ok := val.(Expr); ok {
			rendered, eargs, err := e.ToSQL(qmarkDialect{Dialect: q.dialect}, q.guard)
			if err != nil {
				return "", nil, err
			}
			sub, n, err := substitutePathMarkers(rendered, len(eargs), q.dialect, argIndex)
			if err != nil {
				return "", nil, err
			}
			if n != len(eargs) {
				return "", nil, fmt.Errorf("%w: UpdateMap expression for %q expected %d markers, substituted %d",
					ErrInvalidQuery, col, len(eargs), n)
			}
			setClauses = append(setClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(col), sub))
			args = append(args, eargs...)
			argIndex += len(eargs)
			continue
		}

		setClauses = append(setClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(col), q.dialect.Placeholder(argIndex)))
		args = append(args, q.bindColumnArg(col, val))
		argIndex++
	}

	var sql strings.Builder
	sql.WriteString("UPDATE ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" SET ")
	sql.WriteString(strings.Join(setClauses, ", "))

	// WHERE clause from query conditions, rendered by the same renderer as
	// a SELECT (QK-39); its placeholders continue after the SET arguments.
	whereSQL, whereArgs, err := q.whereForWrite(argIndex)
	if err != nil {
		return "", nil, err
	}
	if whereSQL != "" {
		sql.WriteString(" WHERE ")
		sql.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	return sql.String(), args, nil
}

// isZeroValue checks if a reflect.Value is the zero value for its type.
func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.String() == ""
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map:
		return v.IsNil()
	default:
		return false
	}
}

// isWarnableZero reports whether a skipped zero value is worth a WARN: a scalar
// zero (false / 0 / "") the caller may have intended to persist via Update.
// Nil pointers, interfaces, slices, and maps are the idiomatic "absent / not
// applicable" case — most models carry at least one (e.g. a nil deleted_at), so
// warning on them would fire on nearly every partial update. They are still
// skipped from the SET clause; they just don't trigger the WARN.
func isWarnableZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map:
		return false
	default:
		return true
	}
}

// Delete performs a soft delete by setting deleted_at = NOW().
// If the model doesn't have deleted_at field, performs hard delete.
//
// The query's conditions — its Where calls, and the tenant predicate under
// RowLevelSecurityClient — are ANDed with the key (QK-40): when no row with
// the key satisfies them, Delete removes nothing, returns (0, nil), and runs
// no AfterDelete hook, audit entry or event.
// Returns the number of rows affected.
func (q *Query[T]) Delete(entity *T) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if hook, ok := any(entity).(BeforeDeleteHook); ok {
		if err := hook.BeforeDelete(q.ctx); err != nil {
			return 0, err
		}
	}

	v := reflect.ValueOf(entity).Elem()
	t := v.Type()

	if q.pk.Column == "" {
		return 0, fmt.Errorf("%w: no primary key field found", ErrInvalidModel)
	}

	hasDeletedAt := false
	for i := 0; i < t.NumField(); i++ {
		if columnFromDBTag(t.Field(i).Tag.Get("db")) == "deleted_at" {
			hasDeletedAt = true
			break
		}
	}

	pkValue := q.pkValueOf(v)

	var rows int64
	var err error
	if hasDeletedAt {
		rows, err = q.softDelete(pkValue)
	} else {
		rows, err = q.hardDeleteByPK(pkValue)
	}

	// Under conditions, a delete that removed nothing found no row with the
	// key that satisfies them: nothing happened, so nothing is reported to
	// the hooks, the audit log or the event bus (QK-40).
	if err == nil && rows == 0 && len(q.where) > 0 {
		return 0, nil
	}

	if err == nil {
		if hook, ok := any(entity).(AfterDeleteHook); ok {
			if hErr := q.queueOrRunAfterHook(func() error { return hook.AfterDelete(q.ctx) }); hErr != nil {
				return rows, hErr
			}
		}
		if aErr := q.recordAudit(q.ctx, eventDeleted, entity); aErr != nil {
			return rows, aErr
		}
		if eErr := q.emitEvent(eventDeleted, entity); eErr != nil {
			return rows, eErr
		}
	}

	return rows, err
}

// DeleteBy performs a hard delete with WHERE conditions.
// Requires Where clause for safety.
func (q *Query[T]) DeleteBy() (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if len(q.where) == 0 {
		return 0, fmt.Errorf("%w: DeleteBy requires Where clause to prevent accidental full table delete", ErrInvalidQuery)
	}

	return q.hardDeleteWhere()
}

// HardDelete permanently deletes the entity by its primary key.
//
// The query's conditions are ANDed with the key, as in Delete: when no row
// with the key satisfies them, HardDelete removes nothing, returns (0, nil),
// and runs no AfterDelete hook, audit entry or event (QK-40).
func (q *Query[T]) HardDelete(entity *T) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if hook, ok := any(entity).(BeforeDeleteHook); ok {
		if err := hook.BeforeDelete(q.ctx); err != nil {
			return 0, err
		}
	}

	if q.pk.Column == "" {
		return 0, fmt.Errorf("%w: no primary key field found", ErrInvalidModel)
	}

	v := reflect.ValueOf(entity).Elem()
	pkValue := q.pkValueOf(v)

	rows, err := q.hardDeleteByPK(pkValue)
	if err == nil && rows == 0 && len(q.where) > 0 {
		// Nothing removed under conditions: nothing to report (QK-40).
		return 0, nil
	}
	if err == nil {
		if hook, ok := any(entity).(AfterDeleteHook); ok {
			if hErr := q.queueOrRunAfterHook(func() error { return hook.AfterDelete(q.ctx) }); hErr != nil {
				return rows, hErr
			}
		}
		if aErr := q.recordAudit(q.ctx, eventDeleted, entity); aErr != nil {
			return rows, aErr
		}
		if eErr := q.emitEvent(eventDeleted, entity); eErr != nil {
			return rows, eErr
		}
	}

	return rows, err
}

// softDelete performs a soft delete (sets deleted_at = NOW()) of the row with
// the key, ANDed with the query's conditions (QK-40).
func (q *Query[T]) softDelete(pkValue any) (int64, error) {
	var sql strings.Builder

	sql.WriteString("UPDATE ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" SET ")
	sql.WriteString(q.dialect.Quote("deleted_at"))
	sql.WriteString(" = ")
	sql.WriteString(q.dialect.CurrentTimestamp())
	sql.WriteString(" WHERE ")

	keySQL, args := q.keyWhere(pkValue, 1)
	sql.WriteString(keySQL)

	// Add deleted_at IS NULL to ensure we don't update already deleted rows
	sql.WriteString(" AND ")
	sql.WriteString(q.dialect.Quote("deleted_at"))
	sql.WriteString(" IS NULL")

	whereSQL, whereArgs, err := q.whereForWrite(len(args) + 1)
	if err != nil {
		return 0, err
	}
	if whereSQL != "" {
		sql.WriteString(" AND ")
		sql.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	// F4-6: row tag for single-PK deletes (composite returns "" — gap).
	result, err := q.executeExec(ctx, sql.String(), args, q.rowTag(pkValue))
	if err != nil {
		return 0, fmt.Errorf("soft delete failed: %w", err)
	}

	rowsAffected := int64(0)
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
	}

	return rowsAffected, nil
}

// hardDeleteByPK performs a hard delete by primary key (single or composite),
// ANDed with the query's conditions (QK-40).
// For single-PK models pass the pk value; for composite PKs pass a []any of values
// in the same order as ModelMeta.CompositePK.
func (q *Query[T]) hardDeleteByPK(pkValue any) (int64, error) {
	var sql strings.Builder

	sql.WriteString("DELETE FROM ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" WHERE ")

	keySQL, args := q.keyWhere(pkValue, 1)
	sql.WriteString(keySQL)

	whereSQL, whereArgs, err := q.whereForWrite(len(args) + 1)
	if err != nil {
		return 0, err
	}
	if whereSQL != "" {
		sql.WriteString(" AND ")
		sql.WriteString(whereSQL)
		args = append(args, whereArgs...)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	// F4-6: row tag for single-PK deletes (composite returns "" — gap).
	result, err := q.executeExec(ctx, sql.String(), args, q.rowTag(pkValue))
	if err != nil {
		return 0, fmt.Errorf("delete failed: %w", err)
	}

	rowsAffected := int64(0)
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
	}

	return rowsAffected, nil
}

// hardDeleteWhere performs a hard delete with WHERE conditions.
func (q *Query[T]) hardDeleteWhere() (int64, error) {
	var sql strings.Builder

	sql.WriteString("DELETE FROM ")
	sql.WriteString(q.fullTableName())

	// WHERE clause, rendered by the same renderer as a SELECT (QK-39): the
	// rows DeleteBy removes are the rows List returns for the same
	// conditions. Before, WhereNot lost its NOT here, and DeleteBy removed
	// the rows the caller had excluded.
	whereSQL, args, err := q.whereForWrite(1)
	if err != nil {
		return 0, err
	}
	if whereSQL != "" {
		sql.WriteString(" WHERE ")
		sql.WriteString(whereSQL)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	result, err := q.executeExec(ctx, sql.String(), args)
	if err != nil {
		return 0, fmt.Errorf("delete failed: %w", err)
	}

	rowsAffected := int64(0)
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
	}

	return rowsAffected, nil
}

// saveAssociations recursively saves related models.
func (q *BaseQuery) saveAssociations(v reflect.Value, isUpdate bool) error {
	for _, rel := range q.meta.Relations {
		field := v.FieldByName(rel.Field)
		if !field.IsValid() || field.IsZero() {
			continue
		}

		switch rel.Type {
		case "has_one":
			pkVal := getPKValue(v, q.pk)
			relatedVal := field
			if relatedVal.Kind() != reflect.Ptr {
				relatedVal = field.Addr()
			}

			// Set foreign key on related
			relMeta := GetModelMetaByType(rel.RefType)
			if fm, ok := relMeta.FieldByCol[rel.JoinCol]; ok {
				reflect.Indirect(relatedVal).Field(fm.Index).Set(reflect.ValueOf(pkVal))
			}

			if _, err := q.saveAny(q.ctx, q.exec, relatedVal.Interface(), isUpdate, q.tenantScopeFor(relMeta)); err != nil && !errors.Is(err, errExcludedByWhere) {
				return err
			}

		case "has_many":
			pkVal := getPKValue(v, q.pk)
			relMeta := GetModelMetaByType(rel.RefType)

			for i := 0; i < field.Len(); i++ {
				item := field.Index(i)
				itemPtr := item.Addr()

				// Set foreign key
				if fm, ok := relMeta.FieldByCol[rel.JoinCol]; ok {
					item.Field(fm.Index).Set(reflect.ValueOf(pkVal))
				}

				if _, err := q.saveAny(q.ctx, q.exec, itemPtr.Interface(), isUpdate, q.tenantScopeFor(relMeta)); err != nil && !errors.Is(err, errExcludedByWhere) {
					return err
				}
			}

		case "many_to_many":
			pkVal := getPKValue(v, q.pk)
			relMeta := GetModelMetaByType(rel.RefType)

			for i := 0; i < field.Len(); i++ {
				item := field.Index(i)
				itemPtr := item.Addr()

				// Only save related item if it is new (zero PK).
				// If it already has a PK, it was created beforehand — just link it.
				if isZeroPKValue(item.Field(relMeta.PK.Index)) {
					if _, err := q.saveAny(q.ctx, q.exec, itemPtr.Interface(), isUpdate, q.tenantScopeFor(relMeta)); err != nil && !errors.Is(err, errExcludedByWhere) {
						return err
					}
				}

				// Link in join table
				itemPK := getPKValue(item, relMeta.PK)
				if err := q.linkM2M(*rel, pkVal, itemPK); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Upsert inserts or updates a record depending on whether a conflict occurs on conflictCols.
// updateCols specifies which columns to update on conflict.
//
// An empty updateCols does not mean the same on every engine (QK-48). On
// PostgreSQL, SQLite, MySQL and MariaDB the conflicting row is left as it
// was — insert-or-ignore — and Upsert returns nil; on PostgreSQL and SQLite
// the entity's key is then not written back. On SQL Server and Oracle the
// MERGE updates every column the insert writes except the conflict columns,
// the primary key and created_at, which keeps the row's creation time
// (QK-62). Pass updateCols for behaviour that does not depend on the engine.
//
// The conflict columns name the key the update branch is for on PostgreSQL,
// SQLite, SQL Server and Oracle. MySQL and MariaDB have no conflict target:
// ON DUPLICATE KEY UPDATE fires on a duplicate of any unique key, the
// primary key included, so with updateCols the row that holds the duplicate
// is updated whichever key it is; without updateCols it is left as it was
// (QK-61). On PostgreSQL and SQLite a duplicate of a key other than the
// conflict columns fails with ErrConstraintViolation.
//
// Under RowLevelSecurityClient the update branch only touches a row of the
// resolved tenant (QK-43). When the conflicting row belongs to another
// tenant, nothing is written and Upsert returns an error wrapping
// ErrConstraintViolation — what a Create of the same key returns.
//
// The key written into the entity (QK-63, QK-64). When Upsert returns nil
// and the model's key is one integer column, the entity's key is the key of
// the row the statement inserted or updated, read from that statement,
// whether the entity carried a key or not: RETURNING on PostgreSQL, SQLite
// and MariaDB; the statement's own insert id on MySQL, whose clause ends
// with `<pk> = LAST_INSERT_ID(<pk>)` so that the update branch reports the
// row it met; MERGE … OUTPUT on SQL Server and MERGE … RETURNING on Oracle,
// where an inserted row has the key the identity assigned, not the one the
// entity carried. When nothing is written on a conflict — DO NOTHING on
// PostgreSQL and SQLite, a MERGE with no update branch — the entity keeps
// its key; MySQL and MariaDB report the conflicting row's. A row of another
// tenant gives no key. The entity is never given the key of another row.
//
// Example:
//
//	quark.For[User](ctx, client).Upsert(&user, []string{"email"}, []string{"name", "updated_at"})
func (q *Query[T]) Upsert(entity *T, conflictCols []string, updateCols []string) error {
	if q.err != nil {
		return q.err
	}
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	// An empty conflict target has no portable meaning and used to diverge
	// wildly by engine (PG: DO NOTHING, MySQL: panic on conflictCols[0],
	// MSSQL/Oracle: invalid MERGE). Fail the same way everywhere.
	if len(conflictCols) == 0 {
		return fmt.Errorf("%w: Upsert requires at least one conflict column", ErrInvalidQuery)
	}
	if err := q.checkTenantUpsert(); err != nil {
		return err
	}
	if err := q.client.Validate(q.ctx, entity); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Upsert prepares the row as an insert (it inserts, or updates the
	// conflicting row's updateCols). Run BeforeCreate so timestamps / defaults /
	// derived fields are set before binding — matching single Create (Finding I).
	// On a conflict the configured updateCols still win. BeforeUpdate is NOT run:
	// the insert-or-update outcome isn't known at call time, so only the insert
	// prep hook fires (owner decision; see the hooks Limitations doc).
	if hook, ok := any(entity).(BeforeCreateHook); ok {
		if err := hook.BeforeCreate(q.ctx); err != nil {
			return err
		}
	}

	// created_at/updated_at column convention (DX-20).
	if q.meta != nil {
		stampTimestamps(entity, q.meta, true)
	}

	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	// Build the base INSERT SQL (same as buildInsert)
	baseSQL, args, err := q.buildInsert(v)
	if err != nil {
		return err
	}

	// Strip RETURNING clause if present — upsert appends conflict handling first
	returningIdx := strings.Index(baseSQL, " RETURNING ")
	insertSQL := baseSQL
	returningClause := ""
	if returningIdx != -1 {
		insertSQL = baseSQL[:returningIdx]
		returningClause = baseSQL[returningIdx:]
	}

	dialectName := q.dialect.Name()
	argOffset := len(args) + 1

	switch dialectName {
	case "mssql", "oracle":
		// MERGE syntax — build the full MERGE statement
		mergeSQL, mergeArgs, hasUpdate, mergeErr := q.buildMerge(v, conflictCols, updateCols)
		if mergeErr != nil {
			return mergeErr
		}
		// Under the tenant guard a MERGE that changed no row matched a row of
		// another tenant: WHEN MATCHED did not fire for it, and WHEN NOT
		// MATCHED cannot, because it matched (QK-43).
		foreign := q.tenantGuarded() && hasUpdate
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()
		// The key of the row the MERGE inserted or updated, read back from
		// the MERGE itself (OUTPUT on SQL Server, RETURNING on Oracle):
		// whatever key the entity carried, as the identity assigns the key
		// of an inserted row (QK-64). See upsert_key.go.
		if q.upsertIntegerKey() {
			n, key, err := q.mergeRowKeys(ctx, mergeSQL, mergeArgs)
			if err != nil {
				return err
			}
			switch {
			case n == 0 && foreign:
				return q.errUpsertOutsideTenant()
			case n == 1:
				setPKValue(v, q.pk, key)
			}
			// n == 0 without the guard: the MERGE met a row and has no
			// update branch, nothing was written and no key is written
			// back, as on PostgreSQL and SQLite. n > 1: the conflict
			// columns matched several rows, and the entity's is not known.
			return nil
		}
		res, execErr := q.executeExec(ctx, mergeSQL, mergeArgs)
		if execErr != nil {
			return execErr
		}
		if foreign {
			if n, _ := res.RowsAffected(); n == 0 {
				return q.errUpsertOutsideTenant()
			}
		}
		return nil
	default:
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		if q.tenantGuarded() {
			return q.upsertGuardedInsertStyle(ctx, v, insertSQL, returningClause, args, conflictCols, updateCols)
		}

		upsertFragment := q.dialect.UpsertSQL(conflictCols, updateCols, argOffset)
		fullSQL := insertSQL + upsertFragment + returningClause
		carried := q.pk.Column != "" && !isZeroPKValue(v.Field(q.pk.Index))

		if q.dialect.SupportsReturning() && q.pk.Column != "" {
			row := q.executeQueryRow(ctx, fullSQL, args)
			err := q.scanReturning(row, v)
			if len(updateCols) == 0 && errors.Is(err, sql.ErrNoRows) {
				// No update branch: on a conflict PostgreSQL and SQLite do
				// nothing, and RETURNING has no row to hand back. The row
				// stays as it was, which is what the upsert asked for; it
				// used to surface as sql.ErrNoRows here while MySQL, MariaDB,
				// SQL Server and Oracle answered nil (QK-48). The entity's
				// key is not written back.
				return nil
			}
			if err != nil {
				return err
			}
			// The statement went through the single-row query primitive,
			// which drops nothing from the cache: drop the table tag and the
			// tag of the row it inserted or updated, whose key RETURNING
			// just wrote into the entity. Nothing did, and a cached List
			// kept answering what the table held before the upsert (QK-66).
			q.invalidateInsert(ctx, getPKValue(v, q.pk))
			return nil
		}
		// MySQL, and MariaDB through the MySQL dialect: the key comes from
		// this statement's result, with the update branch made to report
		// the key of the row it met (QK-63, see upsert_key.go). It used to
		// come from SELECT LAST_INSERT_ID() on whichever connection the
		// pool handed out, which on the update branch is that connection's
		// last generated key. A dialect quark has no such clause for gets
		// no key back.
		keyAssign, _ := q.duplicateKeyKeyAssignment(upsertFragment, argOffset)
		res, execErr := q.executeExec(ctx, insertSQL+upsertFragment+keyAssign+returningClause, args)
		if execErr != nil {
			return execErr
		}
		if keyAssign != "" {
			if key, found, write := duplicateKeyRowKey(res, carried); found {
				if write {
					setPKValue(v, q.pk, key)
				}
				// executeExec dropped the table tag; the row's tag can only
				// be dropped now that its key is known (QK-66).
				q.invalidateRowTags(ctx, getPKValue(v, q.pk))
			}
		}
		return nil
	}
}

// buildMerge constructs a MERGE (UPSERT) statement for MSSQL and Oracle.
// hasUpdate reports whether it has a WHEN MATCHED branch. Under
// RowLevelSecurityClient that branch only fires for a row of the resolved
// tenant (QK-43).
func (q *BaseQuery) buildMerge(v reflect.Value, conflictCols []string, updateCols []string) (string, []any, bool, error) {
	// Stamp the resolved tenant: UpsertBatch on Oracle reaches here without
	// going through buildInsert, which is where Upsert stamps it.
	q.ensureTenantID(v)
	t := v.Type()
	type colVal struct {
		col string
		val any
	}
	var allCols []colVal
	argIndex := 1

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		dbTag := columnFromDBTag(field.Tag.Get("db"))
		if dbTag == "" || dbTag == "-" {
			continue
		}
		if !q.meta.HasCompositePK && i == q.pk.Index && isZeroPKValue(v.Field(i)) {
			continue
		}
		allCols = append(allCols, colVal{col: dbTag, val: q.bindColumnArg(dbTag, v.Field(i).Interface())})
	}

	conflictSet := make(map[string]bool, len(conflictCols))
	for _, c := range conflictCols {
		conflictSet[c] = true
	}

	table := q.fullTableName()
	alias := "src"

	// Source values: (SELECT $1 AS col1, $2 AS col2 …)
	var srcCols []string
	var args []any
	for _, cv := range allCols {
		srcCols = append(srcCols, fmt.Sprintf("%s AS %s", q.dialect.Placeholder(argIndex), q.dialect.Quote(cv.col)))
		args = append(args, cv.val)
		argIndex++
	}

	// ON clause
	var onParts []string
	for _, cc := range conflictCols {
		onParts = append(onParts, fmt.Sprintf("target.%s = %s.%s", q.dialect.Quote(cc), alias, q.dialect.Quote(cc)))
	}

	// WHEN MATCHED THEN UPDATE SET
	effectiveUpdateCols := updateCols
	if len(effectiveUpdateCols) == 0 {
		written := make([]string, len(allCols))
		for i, cv := range allCols {
			written[i] = cv.col
		}
		effectiveUpdateCols = q.mergeInferredUpdateCols(written, conflictSet)
	}
	var updateParts []string
	for _, uc := range effectiveUpdateCols {
		updateParts = append(updateParts, fmt.Sprintf("target.%s = %s.%s", q.dialect.Quote(uc), alias, q.dialect.Quote(uc)))
	}

	// WHEN NOT MATCHED THEN INSERT
	// Neither MSSQL nor Oracle allows multi-part identifiers (e.g. target.col)
	// in the MERGE INSERT column list — use bare column names only.
	// Both MSSQL (IDENTITY(1,1)) and Oracle (GENERATED ALWAYS AS IDENTITY) forbid
	// explicit inserts into auto-increment PK columns in the MERGE INSERT branch.
	// Skip integer single-PKs from the INSERT column list for these dialects.
	skipIdentityPK := (q.dialect.Name() == "mssql" || q.dialect.Name() == "oracle") && !q.meta.HasCompositePK
	if skipIdentityPK {
		switch q.pk.Kind {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			// keep true — integer PK is an auto-increment column
		default:
			skipIdentityPK = false // string/other PKs are user-supplied, include them
		}
	}

	var insCols []string
	var insSrc []string
	for _, cv := range allCols {
		if skipIdentityPK && cv.col == q.pk.Column {
			continue
		}
		insCols = append(insCols, q.dialect.Quote(cv.col))
		insSrc = append(insSrc, fmt.Sprintf("%s.%s", alias, q.dialect.Quote(cv.col)))
	}

	var sqlBuf strings.Builder
	if q.dialect.Name() == "oracle" {
		// Oracle does not allow the AS keyword in MERGE table/subquery aliases.
		sqlBuf.WriteString(fmt.Sprintf("MERGE INTO %s target\n", table))
		sqlBuf.WriteString(fmt.Sprintf("USING (SELECT %s) %s\n", strings.Join(srcCols, ", "), alias))
	} else {
		sqlBuf.WriteString(fmt.Sprintf("MERGE INTO %s AS target\n", table))
		sqlBuf.WriteString(fmt.Sprintf("USING (SELECT %s) AS %s\n", strings.Join(srcCols, ", "), alias))
	}
	sqlBuf.WriteString(fmt.Sprintf("ON (%s)\n", strings.Join(onParts, " AND ")))
	if len(updateParts) > 0 {
		sqlBuf.WriteString(q.mergeMatchedClause(strings.Join(updateParts, ", "), argIndex, &args))
	}
	sqlBuf.WriteString(fmt.Sprintf("WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		strings.Join(insCols, ", "), strings.Join(insSrc, ", ")))

	if q.dialect.Name() == "mssql" {
		sqlBuf.WriteString(";")
	} else {
		// Oracle
		sqlBuf.WriteString(";")
	}

	return sqlBuf.String(), args, len(updateParts) > 0, nil
}

// mergeInferredUpdateCols is the update set of a MERGE that was given no
// updateCols (SQL Server, Oracle): the columns the insert writes, except the
// conflict columns, the primary key and created_at. It used to be every
// written column but the conflict ones (QK-62). The key is the row's
// identity, which Update never writes either, and an integer key is an
// IDENTITY column the engine refuses to update (Msg 8102, ORA-32796) — the
// whole statement was refused, even for a row it would have inserted.
// created_at is stamped on create only: Update leaves it, and the stamp the
// convention put on the entity overwrote the row's. updated_at stays in the
// set, refreshed as by Update.
func (q *BaseQuery) mergeInferredUpdateCols(written []string, conflictSet map[string]bool) []string {
	keyCols := make(map[string]bool, 1)
	if q.meta != nil && q.meta.HasCompositePK {
		for _, cpk := range q.meta.CompositePK {
			keyCols[cpk.Column] = true
		}
	} else if q.pk.Column != "" {
		keyCols[q.pk.Column] = true
	}
	var out []string
	for _, col := range written {
		if conflictSet[col] || keyCols[col] || strings.EqualFold(col, "created_at") {
			continue
		}
		out = append(out, col)
	}
	return out
}

// mergeMatchedClause renders a MERGE's WHEN MATCHED branch for the SET list
// sets. Under RowLevelSecurityClient it only fires for a row of the resolved
// tenant (QK-43): SQL Server takes the condition on the WHEN, Oracle as a
// WHERE on the UPDATE. The tenant is bound at argIndex and appended to args.
func (q *BaseQuery) mergeMatchedClause(sets string, argIndex int, args *[]any) string {
	if !q.tenantGuarded() {
		return fmt.Sprintf("WHEN MATCHED THEN UPDATE SET %s\n", sets)
	}
	*args = append(*args, q.tenantID)
	cond := fmt.Sprintf("target.%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(argIndex))
	if q.dialect.Name() == "oracle" {
		return fmt.Sprintf("WHEN MATCHED THEN UPDATE SET %s WHERE %s\n", sets, cond)
	}
	return fmt.Sprintf("WHEN MATCHED AND %s THEN UPDATE SET %s\n", cond, sets)
}

// CreateBatch inserts multiple records in a single SQL statement using bulk VALUES.
// Each entity gets its PK populated when the dialect supports RETURNING; otherwise
// PKs are left at their zero value (callers can re-query if needed).
//
// Example:
//
//	users := []*User{{Name: "Alice"}, {Name: "Bob"}}
//	err := quark.For[User](ctx, client).CreateBatch(users)
func (q *Query[T]) CreateBatch(entities []*T) error {
	if q.err != nil {
		return q.err
	}
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if len(entities) == 0 {
		return nil
	}

	// Validate all entities first
	for _, e := range entities {
		if err := q.client.Validate(q.ctx, e); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	// Run BeforeCreate on every entity before binding, so hooks that set
	// timestamps / defaults / derived fields land in the INSERT — the same
	// contract as single Create (validate, then BeforeCreate). Batch ops used to
	// skip hooks entirely, silently dropping those mutations: a model whose
	// BeforeCreate sets CreatedAt would otherwise write a zero time that MySQL
	// strict mode rejects, and any derived column would be lost (Finding H).
	// After* hooks are intentionally NOT fired for batch ops — their commit-phase
	// queue semantics (queueOrRunAfterHook) don't map onto a multi-row write; run
	// a loop of single Create inside client.Tx if you need them.
	for _, e := range entities {
		if hook, ok := any(e).(BeforeCreateHook); ok {
			if err := hook.BeforeCreate(q.ctx); err != nil {
				return err
			}
		}

		// created_at/updated_at column convention (DX-20).
		if q.meta != nil {
			stampTimestamps(e, q.meta, true)
		}
	}

	// Build column list from the first entity
	first := reflect.ValueOf(entities[0])
	if first.Kind() == reflect.Ptr {
		first = first.Elem()
	}
	q.ensureTenantID(first)

	t := first.Type()
	var columns []string
	var colIndexes []int
	var colTags []string // raw db tags, parallel to colIndexes, for per-column tz resolution
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		dbTag := columnFromDBTag(field.Tag.Get("db"))
		if dbTag == "" || dbTag == "-" {
			continue
		}
		if !q.meta.HasCompositePK && i == q.pk.Index && isZeroPKValue(first.Field(i)) {
			continue
		}
		if err := q.guard.ValidateIdentifier(dbTag); err != nil {
			return err
		}
		columns = append(columns, q.dialect.Quote(dbTag))
		colIndexes = append(colIndexes, i)
		colTags = append(colTags, dbTag)
	}

	// Oracle's INSERT ALL statement is incompatible with GENERATED ALWAYS AS IDENTITY
	// columns — Oracle generates only one sequence value for the whole statement,
	// causing ORA-00001 on the second row. Use individual single-row INSERTs instead.
	if q.dialect.Name() == "oracle" {
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()
		tableName := q.fullTableName()
		colList := strings.Join(columns, ", ")
		phs := make([]string, len(colIndexes))
		for j := range colIndexes {
			phs[j] = q.dialect.Placeholder(j + 1)
		}
		insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", tableName, colList, strings.Join(phs, ", "))

		// Backfill the generated PK per row when it's a single auto-generated key.
		// Oracle's RETURNING is an OUT bind inside a PL/SQL block (mirrors single
		// Create above); the multi-row VALUES path that other dialects use can't
		// carry it, so without this the per-row loop leaves entity.ID == 0 on
		// Oracle while the rows insert fine — a silent divergence from every other
		// engine (Finding C). The condition matches the column loop's PK skip
		// above: a single, non-composite PK that was zero on the first entity.
		returnPK := q.pk.Column != "" && !q.meta.HasCompositePK && isZeroPKValue(first.Field(q.pk.Index))
		execSQL := insertSQL
		if returnPK {
			execSQL = "BEGIN " + insertSQL + " " + q.dialect.Returning(q.pk.Column) + " INTO :ret_id; END;"
		}

		pks := make([]any, 0, len(entities))
		// executeExec drops the table tag per row; the fresh row tags are
		// dropped once for the whole batch (one call) so a cached read by PK
		// can't go stale — parity with single Create. On the way out, so the
		// rows before a failing one, which stay inserted, are dropped too
		// (QK-66). No-op without a cache store.
		if returnPK {
			defer func() { q.invalidateBatchInsert(ctx, pks) }()
		}
		for _, entity := range entities {
			v := reflect.ValueOf(entity)
			if v.Kind() == reflect.Ptr {
				v = v.Elem()
			}
			q.ensureTenantID(v)
			rowArgs := make([]any, len(colIndexes))
			for j, ci := range colIndexes {
				rowArgs[j] = q.bindColumnArg(colTags[j], v.Field(ci).Interface())
			}
			if returnPK {
				var id int64
				if _, err := q.executeExec(ctx, execSQL, append(rowArgs, sql.Named("ret_id", sql.Out{Dest: &id}))); err != nil {
					return err
				}
				setPKValue(v, q.pk, id)
				pks = append(pks, id)
			} else if _, err := q.executeExec(ctx, execSQL, rowArgs); err != nil {
				return err
			}
		}
		return nil
	}

	// MySQL and SQL Server can't read generated PKs back from a multi-row INSERT
	// (neither supports RETURNING). When the PK is auto-generated, insert per row
	// and back-fill each entity with the same mechanism single Create uses
	// (LastInsertId on MySQL, SCOPE_IDENTITY on SQL Server). Without this,
	// CreateBatch silently leaves every entity.ID == 0 — the MySQL/MSSQL sibling
	// of the Oracle Finding C (Finding G). Provided or composite PKs fall through
	// to the faster chunked multi-row INSERT below.
	//
	// Since A12 Q3 the per-row form is the fallback, not the rule: where the
	// engine can PROVE which key each row got, a chunk goes in one round trip
	// with the same rows and the same keys (createBatchBackfill, QK-36).
	if !q.dialect.SupportsReturning() &&
		q.pk.Column != "" && !q.meta.HasCompositePK && isZeroPKValue(first.Field(q.pk.Index)) {
		return q.createBatchBackfill(entities, columns, colIndexes, colTags)
	}

	// Chunk the multi-row INSERT so each statement stays within THIS dialect's
	// bind-parameter ceiling (batchBindParamCeiling). Without this, a
	// CreateBatch of a few hundred wide rows overruns SQL Server's
	// ~2100-parameter limit, and a few thousand overruns SQLite — the
	// statement simply fails. Chunks loop on the bound executor (q.exec), so
	// an explicit tx or a native-RLS executor still routes correctly; like
	// DeleteBatch, chunks are not wrapped in an implicit transaction, so
	// callers needing all-or-nothing across chunks should run CreateBatch
	// inside client.Tx.
	rowsPerChunk := batchBindParamCeiling(q.dialect.Name()) / len(columns)
	if rowsPerChunk < 1 {
		// Only a model with more insertable columns than the whole budget
		// lands here — vanishingly rare. It degrades to one row per
		// statement (safe, just slower); the guard keeps rowsPerChunk ≥ 1.
		rowsPerChunk = 1
	}

	// One timeout context for the whole batch (matches DeleteBatch and the
	// Oracle path above): QueryTimeout bounds the operation, not each chunk.
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()
	for start := 0; start < len(entities); start += rowsPerChunk {
		end := min(start+rowsPerChunk, len(entities))
		if err := q.createBatchStmt(ctx, entities[start:end], columns, colIndexes, colTags); err != nil {
			return err
		}
	}
	return nil
}

// createBatchBackfillPerRow inserts each entity with its own single-row INSERT
// and back-fills the generated PK. It is the non-RETURNING path for MySQL and
// SQL Server: a multi-row INSERT can't read generated keys back there, so the
// only way to populate entity.ID is per row, with the same mechanism single
// Create uses — LastInsertId on MySQL, SCOPE_IDENTITY (via LastInsertIDQuery)
// on SQL Server. Slower than the chunked multi-row form, but only taken when
// the PK is auto-generated and the caller therefore needs it back; provided or
// composite PKs keep the multi-row path. Both executors pin to q.exec (primary
// / tx), never a replica — this is a write.
func (q *Query[T]) createBatchBackfillPerRow(entities []*T, columns []string, colIndexes []int, colTags []string) error {
	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()
	return q.backfillPerRow(ctx, entities, columns, colIndexes, colTags)
}

// backfillPerRow is createBatchBackfillPerRow on a context the caller owns,
// so the one-round-trip paths can hand it a chunk under the batch's timeout.
func (q *Query[T]) backfillPerRow(ctx context.Context, entities []*T, columns []string, colIndexes []int, colTags []string) error {
	colList := strings.Join(columns, ", ")
	phs := make([]string, len(colIndexes))
	for j := range colIndexes {
		phs[j] = q.dialect.Placeholder(j + 1)
	}
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", q.fullTableName(), colList, strings.Join(phs, ", "))
	isMSSQL := q.dialect.Name() == "mssql"

	pks := make([]any, 0, len(entities))
	// MySQL's executeExec drops the table tag per row; the MSSQL query-row
	// path drops nothing. Drop the table tag + the fresh row tags once for
	// the whole batch — parity with the Oracle path and single Create — on
	// the way out: the rows before a failing one stay inserted, and a batch
	// that failed on SQL Server used to leave them out of every cached read
	// of the table (QK-66). No-op without a cache store.
	defer func() { q.invalidateBatchInsert(ctx, pks) }()
	for _, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		q.ensureTenantID(v)
		rowArgs := make([]any, len(colIndexes))
		for j, ci := range colIndexes {
			rowArgs[j] = q.bindColumnArg(colTags[j], v.Field(ci).Interface())
		}

		var id int64
		if isMSSQL {
			// SCOPE_IDENTITY() in the same batch returns this row's identity.
			//
			// Scanned into a NullInt64 for the reason single Create does: when
			// the INSERT is rejected the server still answers the SELECT, with
			// NULL, and the driver delivers the INSERT's error only after the
			// scan succeeds. Scanning that NULL into an int64 failed first, and
			// database/sql returned "converting NULL to int64 is unsupported"
			// in place of the engine's error — a duplicate key that
			// IsUniqueViolation could not see (QK-53).
			var lastID sql.NullInt64
			row := q.executeQueryRow(ctx, insertSQL+"; "+q.dialect.LastInsertIDQuery(q.meta.Table, q.pk.Column), rowArgs)
			if err := row.Scan(&lastID); err != nil {
				return wrapDBError(err)
			}
			if !lastID.Valid {
				// No error and no identity: an INSTEAD OF trigger took the
				// row, or the key is not an IDENTITY. Handing back a zero key
				// the caller would use as real is worse than failing.
				return fmt.Errorf("insert into %s reported success but SCOPE_IDENTITY() returned NULL", q.meta.Table)
			}
			id = lastID.Int64
		} else { // mysql
			res, err := q.executeExec(ctx, insertSQL, rowArgs)
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
		}
		setPKValue(v, q.pk, id)
		pks = append(pks, id)
	}
	return nil
}

// createBatchStmt emits a single multi-row INSERT for one chunk of entities.
// CreateBatch splits the full slice into chunks small enough to stay under the
// dialect's bind-parameter ceiling and calls this for each. columns/colIndexes/
// colTags are computed once by the caller and shared across chunks. For
// dialects that support RETURNING, generated primary keys are scanned back into
// the chunk (which aliases the caller's slice, so PKs reach the caller).
func (q *Query[T]) createBatchStmt(ctx context.Context, entities []*T, columns []string, colIndexes []int, colTags []string) error {
	// RETURNING for dialects that support it
	returning := q.dialect.SupportsReturning() && q.pk.Column != ""
	suffix := ""
	if returning {
		suffix = " " + q.dialect.Returning(q.pk.Column)
	}
	sqlStr, args := q.buildBatchInsert(entities, columns, colIndexes, colTags, suffix)

	if returning {
		// INSERT ... RETURNING is a write: pin to the primary, never a replica
		// (F6-5, ADR-0015), even though it reads rows back.
		rows, err := q.executeQueryPrimary(ctx, sqlStr, args)
		if err != nil {
			return err
		}
		defer rows.Close()
		pks := make([]any, 0, len(entities))
		// executeQueryPrimary (the RETURNING scan path) invalidates nothing,
		// unlike executeExec, so drop the table tag + the fresh row tags here
		// or a cached table-level read goes stale after the batch insert (the
		// batch sibling of BB-15). On the way out, so a key that fails to
		// scan after the statement inserted the chunk drops the table tag too
		// (QK-66).
		defer func() { q.invalidateBatchInsert(ctx, pks) }()
		for i := 0; rows.Next(); i++ {
			if i >= len(entities) {
				break
			}
			v := reflect.ValueOf(entities[i])
			if v.Kind() == reflect.Ptr {
				v = v.Elem()
			}
			pkField := v.Field(q.pk.Index)
			if pkField.CanAddr() {
				if err := rows.Scan(pkField.Addr().Interface()); err != nil {
					return wrapDBError(err)
				}
				pks = append(pks, pkField.Interface())
			}
		}
		if err := rows.Err(); err != nil {
			return wrapDBError(err)
		}
		return nil
	}

	_, err := q.executeExec(ctx, sqlStr, args)
	return err
}

// buildBatchInsert writes the multi-row INSERT of one chunk — the columns
// computed once by CreateBatch, one parenthesised row of placeholders per
// entity — followed by suffix, and returns it with its arguments.
func (q *Query[T]) buildBatchInsert(entities []*T, columns []string, colIndexes []int, colTags []string, suffix string) (string, []any) {
	var sqlBuf strings.Builder
	// About seven bytes per placeholder ("$1234, ") and four per row's
	// parentheses and separator, so the builder grows once, not a dozen times.
	sqlBuf.Grow(128 + len(suffix) + len(entities)*(7*len(colIndexes)+4))
	sqlBuf.WriteString("INSERT INTO ")
	sqlBuf.WriteString(q.fullTableName())
	sqlBuf.WriteString(" (")
	sqlBuf.WriteString(strings.Join(columns, ", "))
	sqlBuf.WriteString(") VALUES ")

	// The arguments are sized up front, and each row's placeholders are
	// written straight into the statement: a slice and a strings.Join per
	// row, and an argument slice grown by appending, were two allocations
	// per row and a dozen copies of the whole argument list (QK-36).
	args := make([]any, 0, len(entities)*len(colIndexes))
	argIndex := 1
	for rowIdx, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		q.ensureTenantID(v)

		if rowIdx > 0 {
			sqlBuf.WriteString(", ")
		}
		sqlBuf.WriteByte('(')
		for j, ci := range colIndexes {
			if j > 0 {
				sqlBuf.WriteString(", ")
			}
			writePlaceholder(&sqlBuf, q.dialect, argIndex)
			args = append(args, q.bindColumnArg(colTags[j], v.Field(ci).Interface()))
			argIndex++
		}
		sqlBuf.WriteByte(')')
	}
	sqlBuf.WriteString(suffix)
	return sqlBuf.String(), args
}

// DeleteBatch deletes multiple records by their primary key values using
// DELETE … WHERE pk IN (…) statements, chunked to batchChunkSize to stay within
// every supported dialect's placeholder limit (Oracle: 1000, MSSQL: ~2100, others: larger).
//
// The query's conditions are ANDed with the ids, as in Delete (QK-40): an id
// whose row does not satisfy them is not deleted.
//
// Example:
//
//	affected, err := quark.For[User](ctx, client).DeleteBatch([]any{1, 2, 3})
//
// DeleteBatchOf is the typed-slice form of [Query.DeleteBatch] (AQ-07): it
// accepts the ids as they usually arrive — []int64 from a previous query,
// []string for natural keys — and performs the []any conversion once,
// internally. Package-level function because Go methods cannot introduce a
// second type parameter.
//
//	ids := []int64{1, 2, 3}
//	n, err := quark.DeleteBatchOf(quark.For[User](ctx, client), ids)
func DeleteBatchOf[T any, V any](q *Query[T], ids []V) (int64, error) {
	return q.DeleteBatch(anySlice(ids))
}

func (q *Query[T]) DeleteBatch(ids []any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if q.pk.Column == "" {
		return 0, fmt.Errorf("%w: no primary key field found", ErrInvalidModel)
	}

	table := q.fullTableName()
	pkCol := q.dialect.Quote(q.pk.Column)

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	var totalAffected int64
	for start := 0; start < len(ids); start += batchChunkSize {
		end := start + batchChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]

		phs := make([]string, len(chunk))
		for j := range chunk {
			phs[j] = q.dialect.Placeholder(j + 1)
		}

		sqlStr := fmt.Sprintf("DELETE FROM %s WHERE %s IN (%s)",
			table, pkCol, strings.Join(phs, ", "))
		args := chunk

		// The query's conditions narrow the ids, as they do for Delete
		// (QK-40): an id that does not satisfy them is not deleted.
		whereSQL, whereArgs, err := q.whereForWrite(len(chunk) + 1)
		if err != nil {
			return totalAffected, err
		}
		if whereSQL != "" {
			sqlStr += " AND " + whereSQL
			// The capped slice makes append copy instead of writing into
			// the caller's ids beyond this chunk.
			args = append(chunk[:len(chunk):len(chunk)], whereArgs...)
		}

		// The ids are the keys of the rows the statement deletes: their row
		// tags go with the table tag, as for Delete (QK-66).
		rowTags := make([]string, 0, len(chunk))
		for _, id := range chunk {
			rowTags = append(rowTags, q.rowTag(id))
		}
		result, err := q.executeExec(ctx, sqlStr, args, rowTags...)
		if err != nil {
			return totalAffected, fmt.Errorf("delete batch failed: %w", err)
		}
		if result != nil {
			n, _ := result.RowsAffected()
			totalAffected += n
		}
	}
	return totalAffected, nil
}

// UpsertBatch inserts or updates multiple records in a single batch operation.
// conflictCols defines uniqueness (e.g. primary key or unique index columns).
// updateCols defines which columns to update on conflict. An empty updateCols
// leaves a conflicting row as it was on PostgreSQL, SQLite, MySQL and
// MariaDB, and updates every non-conflict column but the primary key and
// created_at on SQL Server and Oracle, as for [Query.Upsert].
//
// On SQL Server and Oracle each entity whose key is one integer column gets
// the key of the row it inserted or updated, as with [Query.Upsert] (QK-64).
// The multi-row INSERT of PostgreSQL, SQLite, MySQL and MariaDB reports no
// key per row, and the entities keep the keys they carried; under
// RowLevelSecurityClient MySQL and MariaDB upsert one row at a time and
// write each key as Upsert does.
//
// Dialect strategies:
//   - Postgres / SQLite / MySQL / MariaDB: multi-row INSERT … ON CONFLICT / ON DUPLICATE KEY
//   - MSSQL: single MERGE … USING (VALUES …) AS src(…)
//   - Oracle: N individual MERGE statements (Oracle IDENTITY restriction prevents bulk MERGE)
//
// Example:
//
//	err := quark.For[User](ctx, client).UpsertBatch(users, []string{"email"}, []string{"name"})
func (q *Query[T]) UpsertBatch(entities []*T, conflictCols []string, updateCols []string) error {
	if q.err != nil {
		return q.err
	}
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	// Same contract as Upsert: an empty conflict target is an error, not an
	// engine-dependent surprise (see Upsert).
	if len(conflictCols) == 0 {
		return fmt.Errorf("%w: UpsertBatch requires at least one conflict column", ErrInvalidQuery)
	}
	if err := q.checkTenantUpsert(); err != nil {
		return err
	}
	if len(entities) == 0 {
		return nil
	}
	for _, e := range entities {
		if err := q.client.Validate(q.ctx, e); err != nil {
			return fmt.Errorf("validation failed: %w", err)
		}
	}

	// Run BeforeCreate per entity before binding — Upsert prepares each row as an
	// insert (Finding I), same contract as single Upsert and CreateBatch.
	// BeforeUpdate is not run for the conflict path (outcome unknown at call time).
	for _, e := range entities {
		if hook, ok := any(e).(BeforeCreateHook); ok {
			if err := hook.BeforeCreate(q.ctx); err != nil {
				return err
			}
		}

		// created_at/updated_at column convention (DX-20).
		if q.meta != nil {
			stampTimestamps(e, q.meta, true)
		}
	}

	first := reflect.ValueOf(entities[0])
	if first.Kind() == reflect.Ptr {
		first = first.Elem()
	}
	q.ensureTenantID(first)

	t := first.Type()
	// Skip an auto-increment single PK when the first entity has a zero value,
	// so the database assigns it (mirrors the same guard in CreateBatch).
	skipAutoIncrPK := !q.meta.HasCompositePK && isZeroPKValue(first.Field(q.pk.Index))
	var cols []batchColDef
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		dbTag := columnFromDBTag(field.Tag.Get("db"))
		if dbTag == "" || dbTag == "-" {
			continue
		}
		if skipAutoIncrPK && i == q.pk.Index {
			continue
		}
		if err := q.guard.ValidateIdentifier(dbTag); err != nil {
			return err
		}
		cols = append(cols, batchColDef{
			quoted: q.dialect.Quote(dbTag),
			dbTag:  dbTag,
			index:  i,
		})
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	run := func(bq *Query[T]) error {
		// Oracle upserts row-at-a-time (identity-sequence limitation, see
		// below) and needs no chunking.
		if bq.dialect.Name() == "oracle" {
			return bq.upsertBatchOracle(ctx, entities, conflictCols, updateCols)
		}
		// Under the tenant guard MySQL and MariaDB upsert row at a time: a
		// multi-row statement reports one rows-affected for the whole batch,
		// and a row of another tenant kept by the guard is not told apart
		// from a row of this one that already held its values (QK-43).
		if bq.tenantGuarded() && bq.upsertFamily() == "duplicate_key" {
			return bq.upsertBatchGuardedRows(ctx, entities, conflictCols, updateCols)
		}

		// Chunk to the dialect's bind-parameter ceiling, exactly like
		// CreateBatch (QK-P1-4): a large UpsertBatch used to build one giant
		// statement and blow SQL Server's ~2100-parameter cap (and the other
		// engines' higher ones). Same non-transactional chunk contract as
		// CreateBatch — wrap in client.Tx for all-or-nothing across chunks.
		// The tenant guard binds one more argument per statement on
		// PostgreSQL, SQLite and SQL Server; one column's worth of headroom
		// covers it.
		width := len(cols)
		if bq.tenantGuarded() {
			width++
		}
		rowsPerChunk := batchBindParamCeiling(bq.dialect.Name()) / width
		if rowsPerChunk < 1 {
			rowsPerChunk = 1
		}
		for start := 0; start < len(entities); start += rowsPerChunk {
			end := min(start+rowsPerChunk, len(entities))
			chunk := entities[start:end]
			var err error
			if bq.dialect.Name() == "mssql" {
				err = bq.upsertBatchMSSQLBulk(ctx, chunk, cols, conflictCols, updateCols)
			} else {
				err = bq.upsertBatchStandard(ctx, chunk, cols, conflictCols, updateCols)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}

	// Under RowLevelSecurityClient the batch is all or nothing: a conflict
	// with another tenant's row is found after the statement that met it,
	// and the rows that statement and the earlier chunks wrote must not
	// stay (QK-43). Inside a caller's transaction they are undone by a
	// savepoint, so they do not stay either when the caller goes on and
	// commits (QK-57).
	if q.tenantGuarded() {
		return q.atomically(ctx, func(tx *Tx) error {
			tq := *q
			tq.exec = tx.tx
			tq.tx = tx
			return run(&tq)
		})
	}
	return run(q)
}

// upsertBatchGuardedRows upserts each entity on its own, through the guarded
// single-row path, for MySQL and MariaDB under RowLevelSecurityClient (QK-43).
func (q *Query[T]) upsertBatchGuardedRows(ctx context.Context, entities []*T, conflictCols, updateCols []string) error {
	for _, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		insert, args, err := q.buildInsert(v)
		if err != nil {
			return err
		}
		insertSQL, returning := splitReturning(insert)
		if err := q.upsertGuardedInsertStyle(ctx, v, insertSQL, returning, args, conflictCols, updateCols); err != nil {
			return fmt.Errorf("upsert batch failed: %w", err)
		}
	}
	return nil
}

// upsertBatchStandard handles Postgres, SQLite, MySQL and MariaDB via multi-row
// INSERT … ON CONFLICT / ON DUPLICATE KEY UPDATE.
func (q *Query[T]) upsertBatchStandard(
	ctx context.Context,
	entities []*T,
	cols []batchColDef,
	conflictCols, updateCols []string,
) error {
	table := q.fullTableName()

	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = c.quoted
	}

	var sqlBuf strings.Builder
	var args []any
	argIndex := 1

	sqlBuf.WriteString("INSERT INTO ")
	sqlBuf.WriteString(table)
	sqlBuf.WriteString(" (")
	sqlBuf.WriteString(strings.Join(quotedCols, ", "))
	sqlBuf.WriteString(") VALUES ")

	for rowIdx, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		q.ensureTenantID(v)

		if rowIdx > 0 {
			sqlBuf.WriteString(", ")
		}
		phs := make([]string, len(cols))
		for j, c := range cols {
			phs[j] = q.dialect.Placeholder(argIndex)
			args = append(args, q.bindColumnArg(c.dbTag, v.Field(c.index).Interface()))
			argIndex++
		}
		sqlBuf.WriteString("(")
		sqlBuf.WriteString(strings.Join(phs, ", "))
		sqlBuf.WriteString(")")
	}

	hasUpdate := false
	if q.tenantGuarded() {
		clause, guardArgs, upd, err := q.guardedConflictClause(conflictCols, updateCols, argIndex)
		if err != nil {
			return err
		}
		sqlBuf.WriteString(clause)
		args = append(args, guardArgs...)
		hasUpdate = upd
	} else {
		sqlBuf.WriteString(q.dialect.UpsertSQL(conflictCols, updateCols, argIndex))
	}

	res, err := q.executeExec(ctx, sqlBuf.String(), args)
	if err != nil {
		return fmt.Errorf("upsert batch failed: %w", err)
	}
	// Every row of the statement is either inserted or updates a row of the
	// tenant; one the guard skipped met another tenant's row (QK-43).
	if hasUpdate {
		if n, _ := res.RowsAffected(); n < int64(len(entities)) {
			return q.errUpsertOutsideTenant()
		}
	}
	return nil
}

// upsertBatchMSSQLBulk builds a single MERGE … USING (VALUES …) AS src(…) statement
// for MSSQL, avoiding N round-trips.
func (q *Query[T]) upsertBatchMSSQLBulk(
	ctx context.Context,
	entities []*T,
	cols []batchColDef,
	conflictCols, updateCols []string,
) error {
	table := q.fullTableName()

	conflictSet := make(map[string]bool, len(conflictCols))
	for _, cc := range conflictCols {
		conflictSet[cc] = true
	}

	// With a single integer key the MERGE reads back each row's key next
	// to the row's position in the statement, as CreateBatch does (QK-36):
	// the position is a literal first column of the source, and OUTPUT
	// names it (QK-64).
	readKeys := q.upsertIntegerKey()

	// Build USING (VALUES …) rows
	var valueRows []string
	var args []any
	argIndex := 1
	for ord, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		q.ensureTenantID(v)

		phs := make([]string, 0, len(cols)+1)
		if readKeys {
			phs = append(phs, strconv.Itoa(ord))
		}
		for _, c := range cols {
			phs = append(phs, q.dialect.Placeholder(argIndex))
			args = append(args, q.bindColumnArg(c.dbTag, v.Field(c.index).Interface()))
			argIndex++
		}
		valueRows = append(valueRows, "("+strings.Join(phs, ", ")+")")
	}

	// Source column aliases (quoted) used in the USING table alias header
	srcCols := make([]string, 0, len(cols)+1)
	if readKeys {
		srcCols = append(srcCols, "quark_ord")
	}
	for _, c := range cols {
		srcCols = append(srcCols, c.quoted)
	}
	const srcAlias = "src"

	// ON clause: target.pk = src.pk
	var onParts []string
	for _, cc := range conflictCols {
		onParts = append(onParts, fmt.Sprintf("target.%s = %s.%s",
			q.dialect.Quote(cc), srcAlias, q.dialect.Quote(cc)))
	}

	// WHEN MATCHED THEN UPDATE SET
	effUpdateCols := updateCols
	if len(effUpdateCols) == 0 {
		written := make([]string, len(cols))
		for i, c := range cols {
			written[i] = c.dbTag
		}
		effUpdateCols = q.mergeInferredUpdateCols(written, conflictSet)
	}
	var updateParts []string
	for _, uc := range effUpdateCols {
		updateParts = append(updateParts, fmt.Sprintf("target.%s = %s.%s",
			q.dialect.Quote(uc), srcAlias, q.dialect.Quote(uc)))
	}

	// WHEN NOT MATCHED THEN INSERT — skip identity PK (MSSQL IDENTITY columns
	// must not be supplied explicitly in the MERGE INSERT branch).
	skipIdentityPK := !q.meta.HasCompositePK
	if skipIdentityPK {
		switch q.pk.Kind {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		default:
			skipIdentityPK = false
		}
	}

	var insCols []string
	var insSrc []string
	for _, c := range cols {
		if skipIdentityPK && c.dbTag == q.pk.Column {
			continue
		}
		insCols = append(insCols, q.dialect.Quote(c.dbTag))
		insSrc = append(insSrc, fmt.Sprintf("%s.%s", srcAlias, q.dialect.Quote(c.dbTag)))
	}

	var sqlBuf strings.Builder
	if readKeys {
		sqlBuf.WriteString("SET NOCOUNT ON;\nDECLARE @quark_keys TABLE (quark_ord INT NOT NULL, quark_key BIGINT NOT NULL);\n")
	}
	sqlBuf.WriteString(fmt.Sprintf("MERGE INTO %s AS target\n", table))
	sqlBuf.WriteString(fmt.Sprintf("USING (VALUES %s) AS %s (%s)\n",
		strings.Join(valueRows, ", "), srcAlias, strings.Join(srcCols, ", ")))
	sqlBuf.WriteString(fmt.Sprintf("ON (%s)\n", strings.Join(onParts, " AND ")))
	if len(updateParts) > 0 {
		sqlBuf.WriteString(q.mergeMatchedClause(strings.Join(updateParts, ", "), argIndex, &args))
	}
	sqlBuf.WriteString(fmt.Sprintf("WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		strings.Join(insCols, ", "), strings.Join(insSrc, ", ")))
	if readKeys {
		sqlBuf.WriteString("\nOUTPUT " + srcAlias + ".quark_ord, INSERTED." + q.dialect.Quote(q.pk.Column) +
			" INTO @quark_keys (quark_ord, quark_key);\nSELECT quark_ord, quark_key FROM @quark_keys;")
		return q.upsertBatchMSSQLKeys(ctx, entities, sqlBuf.String(), args, len(updateParts) > 0)
	}
	sqlBuf.WriteString(";")

	res, err := q.executeExec(ctx, sqlBuf.String(), args)
	if err != nil {
		return fmt.Errorf("upsert batch (mssql) failed: %w", err)
	}
	// A source row the guarded WHEN MATCHED skipped met another tenant's
	// row (QK-43).
	if q.tenantGuarded() && len(updateParts) > 0 {
		if n, _ := res.RowsAffected(); n < int64(len(entities)) {
			return q.errUpsertOutsideTenant()
		}
	}
	return nil
}

// upsertBatchMSSQLKeys runs the bulk MERGE of upsertBatchMSSQLBulk that
// reads back each row's key with its position, and writes the keys into
// the entities (QK-64): the key of the row each source row inserted or
// updated, whatever key the entity carried. A source row that wrote no row
// — it met a row and the MERGE has no update branch, or under
// RowLevelSecurityClient met a row of another tenant — gets no key, and
// neither does one whose conflict columns matched several rows.
func (q *Query[T]) upsertBatchMSSQLKeys(ctx context.Context, entities []*T, mergeSQL string, args []any, hasUpdate bool) (err error) {
	// A write that reads rows back: on the primary, never a replica. The
	// seam drops nothing from the cache for it; invalidateBatchInsert does.
	rows, err := q.executeQueryPrimary(ctx, mergeSQL, args)
	if err != nil {
		return fmt.Errorf("upsert batch (mssql) failed: %w", wrapDBError(err))
	}
	// A read-back that fails after the MERGE wrote still leaves the rows
	// written: the table tag goes on the way out (QK-66). A row of another
	// tenant fails the batch too; the caller's savepoint or transaction
	// undoes it, and the extra drop costs nothing.
	defer func() {
		if err != nil {
			q.invalidateBatchInsert(ctx, nil)
		}
	}()
	keys := make([]int64, len(entities))
	written := make([]int, len(entities))
	for rows.Next() {
		var ord, key int64
		if err := rows.Scan(&ord, &key); err != nil {
			_ = rows.Close()
			return fmt.Errorf("upsert batch (mssql) failed: %w", wrapDBError(err))
		}
		if ord < 0 || ord >= int64(len(entities)) {
			_ = rows.Close()
			return fmt.Errorf("%w: UpsertBatch read back key %d for row %d of a statement of %d", ErrInvalidQuery, key, ord, len(entities))
		}
		keys[ord] = key
		written[ord]++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("upsert batch (mssql) failed: %w", wrapDBError(err))
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("upsert batch (mssql) failed: %w", wrapDBError(err))
	}
	// A source row the guarded WHEN MATCHED skipped met another tenant's
	// row (QK-43).
	if q.tenantGuarded() && hasUpdate {
		for _, w := range written {
			if w == 0 {
				return q.errUpsertOutsideTenant()
			}
		}
	}
	pks := make([]any, 0, len(entities))
	for i, entity := range entities {
		if written[i] != 1 {
			continue
		}
		setPKValue(reflect.ValueOf(entity).Elem(), q.pk, keys[i])
		pks = append(pks, keys[i])
	}
	q.invalidateBatchInsert(ctx, pks)
	return nil
}

// upsertBatchOracle falls back to N individual MERGE calls because Oracle's
// GENERATED ALWAYS AS IDENTITY sequence generates a single value for the whole
// multi-row MERGE statement, causing ORA-00001 on the second inserted row.
func (q *Query[T]) upsertBatchOracle(
	ctx context.Context,
	entities []*T,
	conflictCols, updateCols []string,
) error {
	for _, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		mergeSQL, mergeArgs, hasUpdate, err := q.buildMerge(v, conflictCols, updateCols)
		if err != nil {
			return err
		}
		// A single integer key is read back from each MERGE, as Upsert does
		// (QK-64).
		if q.upsertIntegerKey() {
			n, key, err := q.mergeRowKeys(ctx, mergeSQL, mergeArgs)
			if err != nil {
				return fmt.Errorf("upsert batch (oracle) failed: %w", err)
			}
			if n == 0 && q.tenantGuarded() && hasUpdate {
				return q.errUpsertOutsideTenant()
			}
			if n == 1 {
				setPKValue(v, q.pk, key)
			}
			continue
		}
		res, err := q.executeExec(ctx, mergeSQL, mergeArgs)
		if err != nil {
			return fmt.Errorf("upsert batch (oracle) failed: %w", err)
		}
		if q.tenantGuarded() && hasUpdate {
			if n, _ := res.RowsAffected(); n == 0 {
				return q.errUpsertOutsideTenant()
			}
		}
	}
	return nil
}

// UpdateBatch updates multiple records by their primary keys within a single transaction.
// Each entity undergoes a partial update: zero-value fields are skipped (same semantics as Update).
// A transaction is used to guarantee atomicity across all rows.
//
// On a query bound to a transaction ([ForTx]) the batch runs in that
// transaction, inside a savepoint: a failing row undoes the rows before it
// and leaves the caller's transaction usable, and the caller's rollback
// undoes the whole batch. It used to open a transaction of its own on another
// connection even there, which waited for the caller's locks — until the
// query timeout on SQLite — or committed apart from the caller (QK-57).
//
// The query's conditions are ANDed with each entity's key, as in Update
// (QK-40): an entity whose row does not satisfy them is not written, the way
// an entity whose key does not exist is not written. UpdateBatch returns no
// count, so a caller that needs to know which rows were written calls Update
// per entity.
//
// Example:
//
//	err := quark.For[User](ctx, client).UpdateBatch(users)
func (q *Query[T]) UpdateBatch(entities []*T) error {
	if q.err != nil {
		return q.err
	}
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}
	if len(entities) == 0 {
		return nil
	}
	if q.pk.Column == "" && !q.meta.HasCompositePK {
		return fmt.Errorf("%w: no primary key field found", ErrInvalidModel)
	}

	ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
	defer cancel()

	return q.atomically(ctx, func(tx *Tx) error {
		for _, entity := range entities {
			// BeforeUpdate runs before buildUpdate so a hook that touches
			// UpdatedAt / derived columns is reflected in the SET clause — the
			// single-Update contract (Finding H). Inside the tx: a hook error
			// rolls the whole batch back. q.ctx (not the batch-timeout ctx) is
			// passed, mirroring single Create/Update and CreateBatch. After*
			// hooks are not fired for batch ops (see CreateBatch).
			if hook, ok := any(entity).(BeforeUpdateHook); ok {
				if err := hook.BeforeUpdate(q.ctx); err != nil {
					return err
				}
			}

			// created_at/updated_at column convention (DX-20).
			if q.meta != nil {
				stampTimestamps(entity, q.meta, false)
			}
			v := reflect.ValueOf(entity)
			if v.Kind() == reflect.Ptr {
				v = v.Elem()
			}
			// Build a per-row BaseQuery bound to the transaction executor,
			// preserving tenant isolation and query metadata from the parent query.
			bq := BaseQuery{
				ctx:       ctx,
				client:    tx.client,
				dialect:   tx.client.dialect,
				guard:     tx.client.guard,
				table:     q.table,
				pk:        q.pk,
				exec:      tx.tx,
				meta:      q.meta,
				tenantID:  q.tenantID,
				tenantCol: q.tenantCol,
				schema:    q.schema, // SchemaPerTenant: keep writes in the tenant schema (BB-8)
				// The query's conditions narrow each row's key (QK-40); a row
				// that does not satisfy them is not written, like a key that
				// does not exist.
				where: q.where,
				err:   q.err,
			}
			sqlStr, args, err := bq.buildUpdate(v)
			if err != nil {
				return err
			}
			// F4-6: every entity in UpdateBatch carries its own PK in
			// v — pass the row tag, just like UpdateFields. Composite
			// PKs return "" and fall back to the table tag.
			var pkTag string
			if !bq.meta.HasCompositePK {
				pkTag = bq.rowTag(getPKValue(v, q.pk))
			}
			if _, err := bq.executeExec(ctx, sqlStr, args, pkTag); err != nil {
				return fmt.Errorf("update batch failed: %w", err)
			}
		}
		return nil
	})
}

// linkM2M creates a record in the join table if it doesn't exist.
//
// The operation is idempotent for an already-existing link: a unique-key
// violation from any of the supported drivers is interpreted as "already
// linked" and surfaces as nil. Every other driver error (foreign-key
// violation, missing table, broken connection, etc.) is wrapped with
// wrapDBError and returned, so callers see the failure instead of silent
// corruption.
func (q *BaseQuery) linkM2M(rel RelationMeta, parentPK, childPK any) error {
	// Qualify the join table with the tenant schema under SchemaPerTenant, so
	// the link rows land in the tenant's schema like the entity rows (BB-8).
	joinTable := q.qualifiedTable(rel.JoinTable)
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s, %s) VALUES (%s, %s)",
		joinTable,
		q.dialect.Quote(rel.JoinFK),
		q.dialect.Quote(rel.JoinRefFK),
		q.dialect.Placeholder(1),
		q.dialect.Placeholder(2),
	)

	_, err := q.executeExec(q.ctx, sqlStr, []any{parentPK, childPK})
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return nil
	}
	return fmt.Errorf("linkM2M: %w", wrapDBError(err))
}

// stampTimestamps applies the created_at/updated_at column convention
// (DX-20): on Create, both are set to now (UTC) when the entity carries the
// zero value — an explicit value always wins; on Update, updated_at is
// refreshed unconditionally (the row just changed). Models without those
// columns pay one map lookup each. This replaces the 18 hand-written hook
// methods the reference app needed just to stamp timestamps.
func stampTimestamps(entity any, meta *ModelMeta, creating bool) {
	// A model with neither column has nothing to stamp: return before
	// reading the clock, which a 1000-row batch otherwise read 1000 times
	// for nothing.
	if _, ok := meta.FieldByCol["updated_at"]; !ok {
		if _, ok := meta.FieldByCol["created_at"]; !ok || !creating {
			return
		}
	}
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	now := time.Now().UTC()

	setIf := func(col string, always bool) {
		fm, ok := meta.FieldByCol[col]
		if !ok {
			return
		}
		f := v.Field(fm.Index)
		if !f.CanSet() || f.Type() != timeTimeType {
			return
		}
		if always || f.Interface().(time.Time).IsZero() {
			f.Set(reflect.ValueOf(now))
		}
	}
	if creating {
		setIf("created_at", false)
		setIf("updated_at", false)
	} else {
		setIf("updated_at", true)
	}
}

var timeTimeType = reflect.TypeOf(time.Time{})
