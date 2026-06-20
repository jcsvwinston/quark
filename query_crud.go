// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sort"
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

// maxBatchBindParams is the per-statement bind-parameter budget for bulk
// multi-row INSERT (CreateBatch). SQL Server caps a single statement at ~2100
// parameters — the tightest of the bulk-capable dialects (Oracle takes the
// single-row INSERT loop and never builds a multi-row statement). Staying
// under it lets CreateBatch chunk a large slice into statements that are valid
// on every engine, instead of overrunning the ceiling on the first call.
const maxBatchBindParams = 2000

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

// executeExec runs an ExecContext through the middleware chain.
// This is used for INSERT, UPDATE, DELETE operations.
//
// extraTags are additional invalidation tags emitted alongside q.table
// when the mutation succeeds. Callers that know the affected primary
// key (Update / UpdateFields / Tracked.Save / Delete by PK) pass
// `table:pk` so queries cached under that tag invalidate without
// blowing away every listing on the table (F4-6, per docs/playbooks/cache.md).
// Mutations that don't know the affected rows up-front (DeleteBatch
// WHERE-complex, raw Exec) pass nothing and fall back to the
// table-only invalidation that has been the historical default.
func (q *BaseQuery) executeExec(ctx context.Context, sqlStr string, args []any, extraTags ...string) (sql.Result, error) {
	if q.err != nil {
		return nil, q.err
	}
	// Base handler: direct execution
	handler := ExecFunc(func(ctx context.Context, exec Executor, s string, a []any) (sql.Result, error) {
		start := time.Now()
		res, execErr := exec.ExecContext(ctx, s, a...)
		duration := time.Since(start)
		err := wrapDBError(execErr)

		// Automatic Cache Invalidation (Maintain data freshness).
		// One InvalidateTags call carries the table tag plus any
		// caller-supplied row tag, so backing stores see a single
		// invalidation batch per mutation.
		if err == nil && q.client.cacheStore != nil && q.table != "" {
			tags := make([]string, 0, 1+len(extraTags))
			tags = append(tags, q.table)
			for _, t := range extraTags {
				if t != "" {
					tags = append(tags, t)
				}
			}
			_ = q.client.cacheStore.InvalidateTags(ctx, tags...)
		}

		// Notify observers
		rowsAffected := int64(0)
		if err == nil {
			rowsAffected, _ = res.RowsAffected()
		}
		q.notifyObservers(QueryEvent{
			SQL:       s,
			Args:      a,
			Duration:  duration,
			Error:     err,
			Table:     q.table,
			Operation: "EXEC",
			Rows:      rowsAffected,
		})

		return res, err
	})

	// Wrap with middleware in reverse order
	for i := len(q.client.middleware) - 1; i >= 0; i-- {
		handler = q.client.middleware[i].WrapExec(handler)
	}

	return handler(ctx, q.exec, sqlStr, args)
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

// ensureTenantID populates the tenant field if RLS is active and the field is zero.
func (q *BaseQuery) ensureTenantID(v reflect.Value) {
	if q.tenantID == "" || q.tenantCol == "" {
		return
	}

	if q.meta != nil {
		if fm, ok := q.meta.FieldByCol[q.tenantCol]; ok {
			field := v.Field(fm.Index)
			if field.Kind() == reflect.String && isZeroValue(field) {
				field.SetString(q.tenantID)
			}
		}
	}
}

// saveAny persists an arbitrary struct to the database using its metadata.
// It handles recursive saving of associations if they are present.
func (q *BaseQuery) saveAny(ctx context.Context, exec Executor, entity any, isUpdate bool) (int64, error) {
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

	// 1. Save BelongsTo associations FIRST (so we have their PKs)
	for _, rel := range meta.Relations {
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
				}

				if _, err := sq.saveAny(ctx, exec, relatedVal.Interface(), actualUpdate); err != nil {
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

	// 2. Save the main entity using a dynamic query
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
	}

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
					var lastID int64
					err = dq.executeQueryRow(ctx, sqlBatch, args).Scan(&lastID)
					if err != nil {
						return 0, err
					}
					setPKValue(elem, meta.PK, lastID)
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

	// 4. Save HasOne/HasMany associations AFTER
	if err := dq.saveAssociations(elem, actualUpdate); err != nil {
		return rowsAffected, err
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
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if err := q.client.Validate(q.ctx, entity); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	if hook, ok := any(entity).(BeforeCreateHook); ok {
		if err := hook.BeforeCreate(q.ctx); err != nil {
			return err
		}
	}

	if _, err := q.saveAny(q.ctx, q.exec, entity, false); err != nil {
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
// CAUTION — zero-value trap (P0-4 — pending dirty tracking in Phase 1):
// because zero values are skipped, calling Update cannot write false to a
// bool, 0 to an integer, "" to a string, or nil to a pointer/slice/map.
// To write a zero value explicitly, use UpdateFields or UpdateMap.
// When Update skips a scalar zero (false / 0 / ""), it logs a WARN so callers
// notice the silent skip; skipped nil pointers/slices/maps are the expected
// "absent" case and do not warn.
//
// Any Where() conditions are merged into the WHERE clause alongside the PK.
// Returns the number of rows affected. Recursively saves associations.
func (q *Query[T]) Update(entity *T) (int64, error) {
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if hook, ok := any(entity).(BeforeUpdateHook); ok {
		if err := hook.BeforeUpdate(q.ctx); err != nil {
			return 0, err
		}
	}

	rowsAffected, err := q.saveAny(q.ctx, q.exec, entity, true)
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
// Returns the number of rows affected.
func (q *Query[T]) UpdateFields(entity *T, fields ...string) (int64, error) {
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

	// Merge any additional Where() conditions in the same way Update does.
	for _, cond := range q.where {
		sqlBuf.WriteString(" AND ")
		if err := q.guard.ValidateIdentifier(cond.column); err != nil {
			return 0, err
		}
		if err := q.guard.ValidateOperator(cond.operator); err != nil {
			return 0, err
		}
		sqlBuf.WriteString(q.dialect.Quote(cond.column))
		sqlBuf.WriteString(" ")
		sqlBuf.WriteString(cond.operator)
		sqlBuf.WriteString(" ")
		sqlBuf.WriteString(q.dialect.Placeholder(argIndex))
		args = append(args, cond.value)
		argIndex++
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
// Returns the number of rows affected.
func (q *Query[T]) UpdateMap(data map[string]any) (int64, error) {
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

	// Merge any additional Where() conditions
	for _, cond := range q.where {
		sql.WriteString(" AND ")

		if err := q.guard.ValidateIdentifier(cond.column); err != nil {
			return "", nil, err
		}
		if err := q.guard.ValidateOperator(cond.operator); err != nil {
			return "", nil, err
		}

		sql.WriteString(q.dialect.Quote(cond.column))
		sql.WriteString(" ")
		sql.WriteString(cond.operator)
		sql.WriteString(" ")
		sql.WriteString(q.dialect.Placeholder(argIndex))
		args = append(args, cond.value)
		argIndex++
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

		setClauses = append(setClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(col), q.dialect.Placeholder(argIndex)))
		args = append(args, q.bindColumnArg(col, val))
		argIndex++
	}

	var sql strings.Builder
	sql.WriteString("UPDATE ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" SET ")
	sql.WriteString(strings.Join(setClauses, ", "))

	// WHERE clause from query conditions
	if len(q.where) > 0 {
		sql.WriteString(" WHERE ")
		for i, cond := range q.where {
			if i > 0 {
				sql.WriteString(" AND ")
			}

			if err := q.guard.ValidateIdentifier(cond.column); err != nil {
				return "", nil, err
			}
			if err := q.guard.ValidateOperator(cond.operator); err != nil {
				return "", nil, err
			}

			sql.WriteString(q.dialect.Quote(cond.column))
			sql.WriteString(" ")
			sql.WriteString(cond.operator)
			sql.WriteString(" ")
			sql.WriteString(q.dialect.Placeholder(argIndex))
			args = append(args, cond.value)
			argIndex++
		}
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
// Returns the number of rows affected.
func (q *Query[T]) Delete(entity *T) (int64, error) {
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

	var pkValue any
	if q.meta != nil && q.meta.HasCompositePK {
		vals := make([]any, len(q.meta.CompositePK))
		for j, cpk := range q.meta.CompositePK {
			vals[j] = v.Field(cpk.Index).Interface()
		}
		pkValue = vals
	} else {
		pkValue = getPKValue(v, q.pk)
	}

	var rows int64
	var err error
	if hasDeletedAt {
		rows, err = q.softDelete(pkValue)
	} else {
		rows, err = q.hardDeleteByPK(pkValue)
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
	if q.client == nil {
		return 0, fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
	}

	if len(q.where) == 0 {
		return 0, fmt.Errorf("%w: DeleteBy requires Where clause to prevent accidental full table delete", ErrInvalidQuery)
	}

	return q.hardDeleteWhere()
}

// HardDelete permanently deletes the entity by its primary key.
func (q *Query[T]) HardDelete(entity *T) (int64, error) {
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
	var pkValue any
	if q.meta != nil && q.meta.HasCompositePK {
		vals := make([]any, len(q.meta.CompositePK))
		for j, cpk := range q.meta.CompositePK {
			vals[j] = v.Field(cpk.Index).Interface()
		}
		pkValue = vals
	} else {
		pkValue = getPKValue(v, q.pk)
	}

	rows, err := q.hardDeleteByPK(pkValue)
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

// softDelete performs a soft delete (sets deleted_at = NOW()).
func (q *Query[T]) softDelete(pkValue any) (int64, error) {
	var sql strings.Builder
	var args []any

	sql.WriteString("UPDATE ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" SET ")
	sql.WriteString(q.dialect.Quote("deleted_at"))
	sql.WriteString(" = ")
	sql.WriteString(q.dialect.CurrentTimestamp())
	sql.WriteString(" WHERE ")

	if q.meta != nil && q.meta.HasCompositePK {
		pkVals, _ := pkValue.([]any)
		for j, cpk := range q.meta.CompositePK {
			if j > 0 {
				sql.WriteString(" AND ")
			}
			sql.WriteString(q.dialect.Quote(cpk.Column))
			sql.WriteString(" = ")
			sql.WriteString(q.dialect.Placeholder(j + 1))
			args = append(args, pkVals[j])
		}
	} else {
		sql.WriteString(q.dialect.Quote(q.pk.Column))
		sql.WriteString(" = ")
		sql.WriteString(q.dialect.Placeholder(1))
		args = append(args, pkValue)
	}

	// Add deleted_at IS NULL to ensure we don't update already deleted rows
	sql.WriteString(" AND ")
	sql.WriteString(q.dialect.Quote("deleted_at"))
	sql.WriteString(" IS NULL")

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

// hardDeleteByPK performs a hard delete by primary key (single or composite).
// For single-PK models pass the pk value; for composite PKs pass a []any of values
// in the same order as ModelMeta.CompositePK.
func (q *Query[T]) hardDeleteByPK(pkValue any) (int64, error) {
	var sql strings.Builder
	var args []any

	sql.WriteString("DELETE FROM ")
	sql.WriteString(q.fullTableName())
	sql.WriteString(" WHERE ")

	if q.meta != nil && q.meta.HasCompositePK {
		pkVals, _ := pkValue.([]any)
		for j, cpk := range q.meta.CompositePK {
			if j > 0 {
				sql.WriteString(" AND ")
			}
			sql.WriteString(q.dialect.Quote(cpk.Column))
			sql.WriteString(" = ")
			sql.WriteString(q.dialect.Placeholder(j + 1))
			args = append(args, pkVals[j])
		}
	} else {
		sql.WriteString(q.dialect.Quote(q.pk.Column))
		sql.WriteString(" = ")
		sql.WriteString(q.dialect.Placeholder(1))
		args = append(args, pkValue)
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
	var args []any
	argIndex := 1

	sql.WriteString("DELETE FROM ")
	sql.WriteString(q.fullTableName())

	// WHERE clause
	if len(q.where) > 0 {
		sql.WriteString(" WHERE ")
		for i, cond := range q.where {
			if i > 0 {
				sql.WriteString(" AND ")
			}

			if err := q.guard.ValidateIdentifier(cond.column); err != nil {
				return 0, err
			}
			if err := q.guard.ValidateOperator(cond.operator); err != nil {
				return 0, err
			}

			sql.WriteString(q.dialect.Quote(cond.column))
			sql.WriteString(" ")
			sql.WriteString(cond.operator)
			sql.WriteString(" ")
			sql.WriteString(q.dialect.Placeholder(argIndex))
			args = append(args, cond.value)
			argIndex++
		}
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

			if _, err := q.saveAny(q.ctx, q.exec, relatedVal.Interface(), isUpdate); err != nil {
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

				if _, err := q.saveAny(q.ctx, q.exec, itemPtr.Interface(), isUpdate); err != nil {
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
					if _, err := q.saveAny(q.ctx, q.exec, itemPtr.Interface(), isUpdate); err != nil {
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
// updateCols specifies which columns to update on conflict; if empty, all non-conflict columns are updated.
//
// Example:
//
//	quark.For[User](ctx, client).Upsert(&user, []string{"email"}, []string{"name", "updated_at"})
func (q *Query[T]) Upsert(entity *T, conflictCols []string, updateCols []string) error {
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
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
		mergeSQL, mergeArgs, mergeErr := q.buildMerge(v, conflictCols, updateCols)
		if mergeErr != nil {
			return mergeErr
		}
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()
		_, execErr := q.executeExec(ctx, mergeSQL, mergeArgs)
		return execErr
	default:
		upsertFragment := q.dialect.UpsertSQL(conflictCols, updateCols, argOffset)
		fullSQL := insertSQL + upsertFragment + returningClause

		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		if q.dialect.SupportsReturning() && q.pk.Column != "" {
			row := q.executeQueryRow(ctx, fullSQL, args)
			return q.scanReturning(row, v)
		}
		_, execErr := q.executeExec(ctx, fullSQL, args)
		if execErr != nil {
			return execErr
		}
		if q.dialect.SupportsLastInsertID() && isZeroPKValue(v.Field(q.pk.Index)) {
			idRow := q.executeQueryRow(ctx, q.dialect.LastInsertIDQuery(q.table, q.pk.Column), nil)
			var id int64
			if scanErr := idRow.Scan(&id); scanErr == nil {
				setPKValue(v, q.pk, id)
			}
		}
		return nil
	}
}

// buildMerge constructs a MERGE (UPSERT) statement for MSSQL and Oracle.
func (q *BaseQuery) buildMerge(v reflect.Value, conflictCols []string, updateCols []string) (string, []any, error) {
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
		for _, cv := range allCols {
			if !conflictSet[cv.col] {
				effectiveUpdateCols = append(effectiveUpdateCols, cv.col)
			}
		}
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
		sqlBuf.WriteString(fmt.Sprintf("WHEN MATCHED THEN UPDATE SET %s\n", strings.Join(updateParts, ", ")))
	}
	sqlBuf.WriteString(fmt.Sprintf("WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s)",
		strings.Join(insCols, ", "), strings.Join(insSrc, ", ")))

	if q.dialect.Name() == "mssql" {
		sqlBuf.WriteString(";")
	} else {
		// Oracle
		sqlBuf.WriteString(";")
	}

	return sqlBuf.String(), args, nil
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
		// executeExec already dropped the table tag per row; also drop the fresh
		// row tags (one call for the whole batch) so a cached read by PK can't go
		// stale — parity with single Create. No-op without a cache store.
		if returnPK {
			q.invalidateBatchInsert(ctx, pks)
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
	if !q.dialect.SupportsReturning() &&
		q.pk.Column != "" && !q.meta.HasCompositePK && isZeroPKValue(first.Field(q.pk.Index)) {
		return q.createBatchBackfillPerRow(entities, columns, colIndexes, colTags)
	}

	// Chunk the multi-row INSERT so each statement stays within the dialect's
	// bind-parameter ceiling (maxBatchBindParams). Without this, a CreateBatch
	// of a few hundred wide rows overruns SQL Server's ~2100-parameter limit,
	// and a few thousand overruns SQLite/Postgres/MySQL — the statement simply
	// fails. Chunks loop on the bound executor (q.exec), so an explicit tx or a
	// native-RLS executor still routes correctly; like DeleteBatch, chunks are
	// not wrapped in an implicit transaction, so callers needing all-or-nothing
	// across chunks should run CreateBatch inside client.Tx.
	rowsPerChunk := maxBatchBindParams / len(columns)
	if rowsPerChunk < 1 {
		// Only a model with more than maxBatchBindParams (2000) insertable
		// columns lands here — vanishingly rare. It degrades to one row per
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

	colList := strings.Join(columns, ", ")
	phs := make([]string, len(colIndexes))
	for j := range colIndexes {
		phs[j] = q.dialect.Placeholder(j + 1)
	}
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", q.fullTableName(), colList, strings.Join(phs, ", "))
	isMSSQL := q.dialect.Name() == "mssql"

	pks := make([]any, 0, len(entities))
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
			row := q.executeQueryRow(ctx, insertSQL+"; "+q.dialect.LastInsertIDQuery(q.meta.Table, q.pk.Column), rowArgs)
			if err := row.Scan(&id); err != nil {
				return wrapDBError(err)
			}
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
	// MySQL's executeExec already dropped the table tag per row; the MSSQL
	// query-row path invalidates nothing. Drop the table tag + the fresh row
	// tags once for the whole batch — parity with the Oracle path and single
	// Create. No-op without a cache store.
	q.invalidateBatchInsert(ctx, pks)
	return nil
}

// createBatchStmt emits a single multi-row INSERT for one chunk of entities.
// CreateBatch splits the full slice into chunks small enough to stay under the
// dialect's bind-parameter ceiling and calls this for each. columns/colIndexes/
// colTags are computed once by the caller and shared across chunks. For
// dialects that support RETURNING, generated primary keys are scanned back into
// the chunk (which aliases the caller's slice, so PKs reach the caller).
func (q *Query[T]) createBatchStmt(ctx context.Context, entities []*T, columns []string, colIndexes []int, colTags []string) error {
	var sqlBuf strings.Builder
	sqlBuf.WriteString("INSERT INTO ")
	sqlBuf.WriteString(q.fullTableName())
	sqlBuf.WriteString(" (")
	sqlBuf.WriteString(strings.Join(columns, ", "))
	sqlBuf.WriteString(") VALUES ")

	var args []any
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
		placeholders := make([]string, len(colIndexes))
		for j, ci := range colIndexes {
			placeholders[j] = q.dialect.Placeholder(argIndex)
			args = append(args, q.bindColumnArg(colTags[j], v.Field(ci).Interface()))
			argIndex++
		}
		sqlBuf.WriteString("(")
		sqlBuf.WriteString(strings.Join(placeholders, ", "))
		sqlBuf.WriteString(")")
	}

	// RETURNING for dialects that support it
	if q.dialect.SupportsReturning() && q.pk.Column != "" {
		sqlBuf.WriteString(" ")
		sqlBuf.WriteString(q.dialect.Returning(q.pk.Column))
	}

	if q.dialect.SupportsReturning() && q.pk.Column != "" {
		// INSERT ... RETURNING is a write: pin to the primary, never a replica
		// (F6-5, ADR-0015), even though it reads rows back.
		rows, err := q.executeQueryPrimary(ctx, sqlBuf.String(), args)
		if err != nil {
			return err
		}
		defer rows.Close()
		pks := make([]any, 0, len(entities))
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
		// executeQueryPrimary (the RETURNING scan path) invalidates nothing,
		// unlike executeExec, so drop the table tag + the fresh row tags here or
		// a cached table-level read goes stale after the batch insert (the batch
		// sibling of BB-15).
		q.invalidateBatchInsert(ctx, pks)
		return nil
	}

	_, err := q.executeExec(ctx, sqlBuf.String(), args)
	return err
}

// DeleteBatch deletes multiple records by their primary key values using
// DELETE … WHERE pk IN (…) statements, chunked to batchChunkSize to stay within
// every supported dialect's placeholder limit (Oracle: 1000, MSSQL: ~2100, others: larger).
//
// Example:
//
//	affected, err := quark.For[User](ctx, client).DeleteBatch([]any{1, 2, 3})
func (q *Query[T]) DeleteBatch(ids []any) (int64, error) {
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

		result, err := q.executeExec(ctx, sqlStr, chunk)
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
// updateCols defines which columns to update on conflict; empty = all non-conflict columns.
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
	if q.client == nil {
		return fmt.Errorf("%w: client not initialized", ErrInvalidQuery)
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

	switch q.dialect.Name() {
	case "oracle":
		return q.upsertBatchOracle(ctx, entities, conflictCols, updateCols)
	case "mssql":
		return q.upsertBatchMSSQLBulk(ctx, entities, cols, conflictCols, updateCols)
	default:
		return q.upsertBatchStandard(ctx, entities, cols, conflictCols, updateCols)
	}
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

	sqlBuf.WriteString(q.dialect.UpsertSQL(conflictCols, updateCols, argIndex))

	if _, err := q.executeExec(ctx, sqlBuf.String(), args); err != nil {
		return fmt.Errorf("upsert batch failed: %w", err)
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

	// Build USING (VALUES …) rows
	var valueRows []string
	var args []any
	argIndex := 1
	for _, entity := range entities {
		v := reflect.ValueOf(entity)
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		q.ensureTenantID(v)

		phs := make([]string, len(cols))
		for j, c := range cols {
			phs[j] = q.dialect.Placeholder(argIndex)
			args = append(args, q.bindColumnArg(c.dbTag, v.Field(c.index).Interface()))
			argIndex++
		}
		valueRows = append(valueRows, "("+strings.Join(phs, ", ")+")")
	}

	// Source column aliases (quoted) used in the USING table alias header
	srcCols := make([]string, len(cols))
	for i, c := range cols {
		srcCols[i] = c.quoted
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
		for _, c := range cols {
			if !conflictSet[c.dbTag] {
				effUpdateCols = append(effUpdateCols, c.dbTag)
			}
		}
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
	sqlBuf.WriteString(fmt.Sprintf("MERGE INTO %s AS target\n", table))
	sqlBuf.WriteString(fmt.Sprintf("USING (VALUES %s) AS %s (%s)\n",
		strings.Join(valueRows, ", "), srcAlias, strings.Join(srcCols, ", ")))
	sqlBuf.WriteString(fmt.Sprintf("ON (%s)\n", strings.Join(onParts, " AND ")))
	if len(updateParts) > 0 {
		sqlBuf.WriteString(fmt.Sprintf("WHEN MATCHED THEN UPDATE SET %s\n", strings.Join(updateParts, ", ")))
	}
	sqlBuf.WriteString(fmt.Sprintf("WHEN NOT MATCHED THEN INSERT (%s) VALUES (%s);",
		strings.Join(insCols, ", "), strings.Join(insSrc, ", ")))

	if _, err := q.executeExec(ctx, sqlBuf.String(), args); err != nil {
		return fmt.Errorf("upsert batch (mssql) failed: %w", err)
	}
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
		mergeSQL, mergeArgs, err := q.buildMerge(v, conflictCols, updateCols)
		if err != nil {
			return err
		}
		if _, err := q.executeExec(ctx, mergeSQL, mergeArgs); err != nil {
			return fmt.Errorf("upsert batch (oracle) failed: %w", err)
		}
	}
	return nil
}

// UpdateBatch updates multiple records by their primary keys within a single transaction.
// Each entity undergoes a partial update: zero-value fields are skipped (same semantics as Update).
// A transaction is used to guarantee atomicity across all rows.
//
// Example:
//
//	err := quark.For[User](ctx, client).UpdateBatch(users)
func (q *Query[T]) UpdateBatch(entities []*T) error {
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

	return q.client.Tx(ctx, func(tx *Tx) error {
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
	joinTable := q.dialect.Quote(rel.JoinTable)
	if q.schema != "" {
		joinTable = q.dialect.Quote(q.schema) + "." + joinTable
	}
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
