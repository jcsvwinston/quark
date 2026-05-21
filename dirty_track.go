// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Tracked wraps a loaded entity with a snapshot of its column values, so a
// later Save can emit an UPDATE limited to the fields that actually changed.
//
// Active Record + dirty tracking ligero (Phase 1): the snapshot lives on the
// wrapper, not in the Client, so there is no shared map to grow or evict.
// Each Tracked carries the metadata it needs (client, table, dialect, pk,
// meta) to run Save without the caller threading state.
//
// Tracked is the permanent fix for P0-4: a bool / int / string / pointer can
// be set to its zero value and Save will write it because the diff is taken
// against the snapshot, not against "is this field non-zero?".
type Tracked[T any] struct {
	// Entity is the loaded value. Mutate fields on it directly; Save will
	// detect the changes and write only those columns.
	Entity *T

	snap    map[string]any
	client  *Client
	dialect Dialect
	guard   *SQLGuard
	exec    Executor
	meta    *ModelMeta
	pk      pkMeta
	table   string
	schema  string
	// tx is the *Tx the loading query was bound to (via ForTx[T]),
	// or nil when loaded against a plain Client. Save queues the
	// AfterUpdate hook on it so the hook fires post-commit (F5-4),
	// matching the contract of every other CRUD path.
	tx *Tx
	// tenantID and tenantCol are propagated from the loading query so Save
	// preserves the RowLevelSecurityClient predicate (parallel to query_exec.go's
	// cloneForGroup invariant — see playbooks/tenant.md § Historial P0-1).
	tenantID  string
	tenantCol string
}

// Changed reports the names (db tags) of columns whose value differs between
// Entity now and the snapshot taken at load time. Useful for tests and
// observability; Save uses the same comparison internally.
func (t *Tracked[T]) Changed() []string {
	if t == nil || t.Entity == nil {
		return nil
	}
	v := reflect.ValueOf(t.Entity).Elem()
	var changed []string
	for col, snapVal := range t.snap {
		fm, ok := t.meta.FieldByCol[strings.ToLower(col)]
		if !ok {
			continue
		}
		cur := v.Field(fm.Index).Interface()
		if !valueEquals(cur, snapVal) {
			changed = append(changed, col)
		}
	}
	return changed
}

// Save updates the row identified by Entity's primary key, writing only the
// columns whose value differs from the load-time snapshot. If nothing
// changed, Save returns (0, nil) without touching the database.
//
// Returns the number of rows affected. Refreshes the internal snapshot on
// success so subsequent Save calls diff against the new state.
func (t *Tracked[T]) Save(ctx context.Context) (int64, error) {
	if t == nil || t.Entity == nil {
		return 0, fmt.Errorf("%w: nil tracked entity", ErrInvalidQuery)
	}
	if t.client == nil {
		return 0, fmt.Errorf("%w: tracked entity has no client (was Track() called?)", ErrInvalidQuery)
	}

	if hook, ok := any(t.Entity).(BeforeUpdateHook); ok {
		if err := hook.BeforeUpdate(ctx); err != nil {
			return 0, err
		}
	}

	v := reflect.ValueOf(t.Entity).Elem()
	pkCols := map[string]struct{}{}
	if t.meta.HasCompositePK {
		for _, cpk := range t.meta.CompositePK {
			pkCols[cpk.Column] = struct{}{}
		}
	} else if t.pk.Column != "" {
		pkCols[t.pk.Column] = struct{}{}
	}

	var setClauses []string
	var args []any
	var changedCols []string
	argIndex := 1

	// F5-7: when audit logging is enabled, accumulate a per-column
	// {old,new} delta as we discover changed columns. The snapshot
	// still holds the OLD values at this point (it is refreshed only
	// after the UPDATE succeeds), so this is the one place the prior
	// value is available for the audit diff.
	var auditDiff map[string]any
	if t.client != nil && t.client.audit != nil {
		auditDiff = make(map[string]any)
	}

	// Iterate the meta's field order — deterministic, and matches the order
	// the dialect would emit columns elsewhere.
	for _, fm := range t.meta.Fields {
		col := fm.Column
		if col == "" {
			continue
		}
		if _, isPK := pkCols[col]; isPK {
			continue
		}
		if t.tenantCol != "" && strings.EqualFold(col, t.tenantCol) {
			// Never let the tenant column be rewritten via Save — it's the
			// isolation boundary, not user-mutable state.
			continue
		}
		if fm.IsVersion {
			// The version column gets a dedicated "version = version + 1"
			// clause appended below — never write the entity's snapshotted
			// value back, even if the caller mutated it.
			continue
		}
		snapVal, hasSnap := t.snap[col]
		if !hasSnap {
			continue
		}
		cur := v.Field(fm.Index).Interface()
		if valueEquals(cur, snapVal) {
			continue
		}
		if err := t.guard.ValidateIdentifier(col); err != nil {
			return 0, err
		}
		// Safe Sprintf: col validated by guard above, Placeholder emits literals only.
		setClauses = append(setClauses, fmt.Sprintf("%s = %s", t.dialect.Quote(col), t.dialect.Placeholder(argIndex)))
		args = append(args, cur)
		changedCols = append(changedCols, col)
		if auditDiff != nil {
			auditDiff[col] = map[string]any{"old": snapVal, "new": cur}
		}
		argIndex++
	}

	// If nothing changed, skip the UPDATE entirely — including the version
	// bump. Save with a snapshot equal to current state is a no-op by
	// contract; otherwise a callers's idempotent retry would silently
	// inflate the version each time.
	if len(setClauses) == 0 {
		return 0, nil
	}

	// Optimistic locking: when there are real column changes, bundle a
	// version bump into the same UPDATE so the row's version moves in
	// lock-step with the data write. The predicate added below to WHERE
	// is what makes the lock effective.
	hasVersion := versionFieldOf(t.meta) != nil
	if hasVersion {
		vfm := versionFieldOf(t.meta)
		if err := t.guard.ValidateIdentifier(vfm.Column); err != nil {
			return 0, err
		}
		quoted := t.dialect.Quote(vfm.Column)
		setClauses = append(setClauses, fmt.Sprintf("%s = %s + 1", quoted, quoted))
	}

	var sqlBuf strings.Builder
	sqlBuf.WriteString("UPDATE ")
	if t.schema != "" {
		sqlBuf.WriteString(t.dialect.Quote(t.schema))
		sqlBuf.WriteString(".")
	}
	sqlBuf.WriteString(t.dialect.Quote(t.table))
	sqlBuf.WriteString(" SET ")
	sqlBuf.WriteString(strings.Join(setClauses, ", "))
	sqlBuf.WriteString(" WHERE ")

	if t.meta.HasCompositePK {
		for j, cpk := range t.meta.CompositePK {
			if j > 0 {
				sqlBuf.WriteString(" AND ")
			}
			sqlBuf.WriteString(t.dialect.Quote(cpk.Column))
			sqlBuf.WriteString(" = ")
			sqlBuf.WriteString(t.dialect.Placeholder(argIndex))
			args = append(args, v.Field(cpk.Index).Interface())
			argIndex++
		}
	} else {
		if t.pk.Column == "" {
			return 0, fmt.Errorf("%w: dirty tracking requires a primary key", ErrInvalidModel)
		}
		sqlBuf.WriteString(t.dialect.Quote(t.pk.Column))
		sqlBuf.WriteString(" = ")
		sqlBuf.WriteString(t.dialect.Placeholder(argIndex))
		args = append(args, getPKValue(v, t.pk))
		argIndex++
	}

	if t.tenantID != "" && t.tenantCol != "" {
		sqlBuf.WriteString(" AND ")
		sqlBuf.WriteString(t.dialect.Quote(t.tenantCol))
		sqlBuf.WriteString(" = ")
		sqlBuf.WriteString(t.dialect.Placeholder(argIndex))
		args = append(args, t.tenantID)
		argIndex++
	}

	// Optimistic-locking predicate: AND version = <snapshot_version>. We
	// use the snapshot rather than the entity's current value so the user
	// can't unintentionally overwrite the predicate by mutating the
	// version field directly.
	if hasVersion {
		vfm := versionFieldOf(t.meta)
		snapVer, _ := t.snap[vfm.Column]
		sqlBuf.WriteString(" AND ")
		sqlBuf.WriteString(t.dialect.Quote(vfm.Column))
		sqlBuf.WriteString(" = ")
		sqlBuf.WriteString(t.dialect.Placeholder(argIndex))
		args = append(args, snapVer)
		argIndex++
	}

	ctx, cancel := context.WithTimeout(ctx, t.client.limits.QueryTimeout)
	defer cancel()

	// Borrow the existing middleware-chained executeExec by attaching to a
	// minimal BaseQuery — this gets observer + cache-invalidation behaviour
	// for free instead of reimplementing the chain here. Cache/limits/where
	// fields are deliberately not propagated: executeExec only reads
	// `client`, `cacheStore` (from client), `table`, and observers; the
	// query-shape state of the original Query[T] is irrelevant to a Save
	// that re-derives its own SET/WHERE.
	bq := &BaseQuery{
		client:    t.client,
		ctx:       ctx,
		table:     t.table,
		schema:    t.schema,
		dialect:   t.dialect,
		guard:     t.guard,
		pk:        t.pk,
		exec:      t.exec,
		meta:      t.meta,
		tenantID:  t.tenantID,
		tenantCol: t.tenantCol,
	}
	// F4-6: row tag for single-PK saves (composite returns "" — gap).
	var pkTag string
	if !t.meta.HasCompositePK {
		pkTag = bq.rowTag(getPKValue(v, t.pk))
	}
	result, err := bq.executeExec(ctx, sqlBuf.String(), args, pkTag)
	if err != nil {
		return 0, fmt.Errorf("Tracked.Save failed: %w", err)
	}
	rowsAffected := int64(0)
	if result != nil {
		rowsAffected, _ = result.RowsAffected()
	}

	// Optimistic locking: zero rows-affected with a version column means
	// another writer bumped the version since we loaded. Surface as
	// ErrStaleEntity. Otherwise bump the in-memory version + the snapshot
	// so a subsequent Save against the same Tracked stays consistent.
	if hasVersion {
		vfm := versionFieldOf(t.meta)
		if rowsAffected == 0 {
			return 0, fmt.Errorf("%w: table %s pk=%v", ErrStaleEntity, t.table, getPKValue(v, t.pk))
		}
		bumpVersion(v, vfm)
		t.snap[vfm.Column] = v.Field(vfm.Index).Interface()
	}

	// Refresh the snapshot for the columns we just wrote, so a subsequent
	// Save on the same Tracked diffs against the new state and skips them
	// if untouched.
	for _, col := range changedCols {
		if fm, ok := t.meta.FieldByCol[strings.ToLower(col)]; ok {
			t.snap[col] = v.Field(fm.Index).Interface()
		}
	}

	// F5-7: write the audit row with the {old,new} delta captured
	// above, inline on t.exec so it joins the active transaction when
	// the Tracked was loaded via ForTx. Skipped when audit is off or
	// the table is filtered out.
	if st := t.client.audit; st != nil && st.shouldAudit(t.table) {
		// pkStringFromMeta honours composite PKs (via t.meta.CompositePK);
		// getPKValue(v, t.pk) alone would mis-record composite-PK rows
		// because t.pk carries only the single-PK slot.
		pk := pkStringFromMeta(t.Entity, t.meta)
		if err := t.client.writeAuditRow(ctx, t.exec, st, t.table, "updated", pk, auditDiff); err != nil {
			return rowsAffected, err
		}
	}

	if hook, ok := any(t.Entity).(AfterUpdateHook); ok {
		// F5-4: when the Tracked was loaded inside an explicit
		// transaction (Track() called on a ForTx-bound query), the
		// AfterUpdate fires post-commit through the tx queue —
		// same contract as every other CRUD path. Outside a tx, it
		// runs inline as before.
		fire := func() error { return hook.AfterUpdate(ctx) }
		if t.tx != nil {
			t.tx.queueAfterHook(fire)
		} else if err := fire(); err != nil {
			return rowsAffected, err
		}
	}
	return rowsAffected, nil
}

// TrackedQuery is the lightweight wrapper Query[T].Track() returns. It
// re-issues Find/First/List on the underlying query and wraps each loaded
// entity with a snapshot for later dirty-tracked Save.
type TrackedQuery[T any] struct {
	inner *Query[T]
}

// Track returns a TrackedQuery whose Find/First/List yield *Tracked[T]
// values carrying a column-value snapshot. Call Save on the result to emit
// an UPDATE that touches only the columns whose values changed since load
// — the permanent fix for the zero-value trap (P0-4).
//
// Track is opt-in. Existing Find/First/List remain unchanged.
func (q *Query[T]) Track() *TrackedQuery[T] {
	return &TrackedQuery[T]{inner: q}
}

// Find loads a single entity by primary key and wraps it in a Tracked.
func (tq *TrackedQuery[T]) Find(id any) (*Tracked[T], error) {
	entity, err := tq.inner.Find(id)
	if err != nil {
		return nil, err
	}
	return tq.wrap(&entity), nil
}

// First returns the first matching entity wrapped in a Tracked.
func (tq *TrackedQuery[T]) First() (*Tracked[T], error) {
	entity, err := tq.inner.First()
	if err != nil {
		return nil, err
	}
	return tq.wrap(&entity), nil
}

// List loads all matching entities wrapped in Tracked values.
func (tq *TrackedQuery[T]) List() ([]*Tracked[T], error) {
	entities, err := tq.inner.List()
	if err != nil {
		return nil, err
	}
	out := make([]*Tracked[T], len(entities))
	for i := range entities {
		out[i] = tq.wrap(&entities[i])
	}
	return out, nil
}

// wrap captures the snapshot for a freshly-loaded entity.
func (tq *TrackedQuery[T]) wrap(entity *T) *Tracked[T] {
	q := tq.inner
	v := reflect.ValueOf(entity).Elem()
	snap := make(map[string]any, len(q.meta.Fields))
	for _, fm := range q.meta.Fields {
		if fm.Column == "" {
			continue
		}
		snap[fm.Column] = v.Field(fm.Index).Interface()
	}
	return &Tracked[T]{
		Entity:    entity,
		snap:      snap,
		client:    q.client,
		dialect:   q.dialect,
		guard:     q.guard,
		exec:      q.exec,
		meta:      q.meta,
		pk:        q.pk,
		table:     q.table,
		schema:    q.schema,
		tenantID:  q.tenantID,
		tenantCol: q.tenantCol,
		tx:        q.tx, // F5-4: propagate so Save queues AfterUpdate post-commit.
	}
}

// valueEquals compares two interface values for equality using a small
// hierarchy:
//
//   - nil + nil  → equal.
//   - time.Time and *time.Time use t.Equal(other) so two values for the same
//     wall-time but different monotonic clock readings (e.g. one loaded from
//     the DB without monotonic, one captured via time.Now() with monotonic)
//     compare equal. Without this, the snapshot would emit spurious UPDATEs
//     every time the user touched a *time.Time field.
//   - other comparable types fall back to reflect.Value.Equal which is
//     equivalent to == on the underlying type.
//   - non-comparable types fall back to reflect.DeepEqual.
//
// The comparison must be cheap on hot paths but correct for the types Quark
// maps today (primitives, time.Time, *time.Time, string, []byte, structs).
func valueEquals(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// time.Time and *time.Time get the wall-clock-only comparison. Both must
	// be the same shape (value-vs-value or pointer-vs-pointer) — mixing them
	// is unusual but we handle it conservatively by comparing dereferenced
	// pointers when both sides are non-nil.
	switch ta := a.(type) {
	case time.Time:
		if tb, ok := b.(time.Time); ok {
			return ta.Equal(tb)
		}
	case *time.Time:
		if tb, ok := b.(*time.Time); ok {
			if ta == nil || tb == nil {
				return ta == tb
			}
			return ta.Equal(*tb)
		}
	}
	// Fast path: comparable types via reflect.Value.Equal (Go 1.20+).
	va := reflect.ValueOf(a)
	vb := reflect.ValueOf(b)
	if va.Type() == vb.Type() && va.Comparable() {
		return va.Equal(vb)
	}
	return reflect.DeepEqual(a, b)
}
