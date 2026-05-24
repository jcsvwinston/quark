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

// AuditConfig configures the optional audit log (F5-7). Pass it to
// [Client.EnableAuditLog]. The zero value audits every table with
// empty user/tenant attribution; populate the callbacks and filters
// to taste.
type AuditConfig struct {
	// UserFromContext, when set, extracts the acting user identifier
	// from the request context for the `user_id` column. Returns ""
	// when unknown. nil leaves user_id empty.
	UserFromContext func(context.Context) string

	// TenantFromContext, when set, extracts the tenant identifier for
	// the `tenant_id` column. Returns "" when unknown. nil leaves
	// tenant_id empty. (When using a TenantRouter the value is
	// already on the context the resolver reads — reuse the same
	// extractor here.)
	TenantFromContext func(context.Context) string

	// IncludeTables, when non-empty, restricts auditing to exactly
	// these table names. Empty means "all tables" (subject to
	// ExcludeTables). The audit table itself (`quark_audit`) is
	// always excluded regardless of this list — auditing the audit
	// log would recurse.
	IncludeTables []string

	// ExcludeTables names tables that are never audited. Takes
	// precedence over IncludeTables.
	ExcludeTables []string
}

// auditTableName is the fixed name of the audit log table. Excluded
// from auditing unconditionally so audit writes never recurse.
const auditTableName = "quark_audit"

// quarkAuditRow is the audit log model. EnableAuditLog migrates it on
// every configured Client so the DDL is portable across all six
// dialects (the migrator picks each engine's native type — the
// PG-specific BIGSERIAL/JSONB shape in the original sketch would not
// port to MySQL/MSSQL/Oracle/SQLite). The diff is stored via
// [JSON][map[string]any], which serialises to the engine's JSON or
// text column.
type quarkAuditRow struct {
	ID          int64                `db:"id" pk:"true"`
	Timestamp   time.Time            `db:"ts"`
	TenantID    string               `db:"tenant_id"`
	UserID      string               `db:"user_id"`
	TargetTable string               `db:"table_name"`
	Operation   string               `db:"operation"`
	PK          string               `db:"pk"`
	Diff        JSON[map[string]any] `db:"diff"`
}

// TableName pins the table to `quark_audit` (the default deriver
// would produce something else).
func (quarkAuditRow) TableName() string { return auditTableName }

// auditState holds the live audit configuration on the Client.
type auditState struct {
	cfg     AuditConfig
	include map[string]struct{} // nil when IncludeTables empty
	exclude map[string]struct{}
}

// shouldAudit reports whether a table should be audited under the
// configured include/exclude filters. The audit table is always
// excluded.
func (a *auditState) shouldAudit(table string) bool {
	if table == auditTableName {
		return false
	}
	if _, no := a.exclude[table]; no {
		return false
	}
	if a.include != nil {
		_, yes := a.include[table]
		return yes
	}
	return true
}

// EnableAuditLog turns on the optional audit log for the Client. It
// migrates the `quark_audit` table (idempotent) and installs the
// configuration so every subsequent Create / Update / Delete writes
// an audit row.
//
// The audit row is written **inline on the same connection /
// transaction as the CRUD operation**, not post-commit. When the
// operation runs inside [Client.Tx], the audit row joins that
// transaction and commits (or rolls back) atomically with the data —
// you never end up with committed data lacking its audit trail, nor
// an audit row for rolled-back work. This is the "junto al commit, no
// después" guarantee from ADR-0013; it is intentionally stronger than
// the post-commit `OnCommit` emission used by the EventBus (F5-6),
// where losing an event on a crash is acceptable but losing an audit
// record is not. For non-transactional CRUD the audit INSERT is a
// separate statement immediately after the write — there is a small
// crash window there, closed by wrapping your writes in [Client.Tx].
//
// The diff payload depends on the operation:
//   - Create: the full inserted row, `{column: value}`.
//   - Delete: the full row being deleted, `{column: value}`.
//   - Update via [Query.Update] / [Query.UpdateFields]: the new
//     values, `{column: value}` (no prior value — there is no
//     snapshot).
//   - Update via [Tracked.Save]: a per-column delta,
//     `{column: {"old": …, "new": …}}`, because the tracked snapshot
//     carries the prior values.
//
// Bulk paths (CreateBatch / UpdateBatch / DeleteBatch / DeleteBy /
// UpdateMap) are not audited — same scope boundary as hooks and
// events.
//
// EnableAuditLog is intended to be called once at setup. Calling it
// again replaces the configuration.
func (c *Client) EnableAuditLog(ctx context.Context, cfg AuditConfig) error {
	if err := c.Migrate(ctx, &quarkAuditRow{}); err != nil {
		return fmt.Errorf("enable audit log: migrate %s: %w", auditTableName, err)
	}
	st := &auditState{cfg: cfg}
	if len(cfg.IncludeTables) > 0 {
		st.include = make(map[string]struct{}, len(cfg.IncludeTables))
		for _, t := range cfg.IncludeTables {
			st.include[t] = struct{}{}
		}
	}
	if len(cfg.ExcludeTables) > 0 {
		st.exclude = make(map[string]struct{}, len(cfg.ExcludeTables))
		for _, t := range cfg.ExcludeTables {
			st.exclude[t] = struct{}{}
		}
	}
	c.audit = st
	return nil
}

// DisableAuditLog turns audit logging back off. Subsequent CRUD stops
// writing quark_audit rows; the table and its existing rows are left
// intact. Safe to call when auditing was never enabled.
func (c *Client) DisableAuditLog() {
	c.audit = nil
}

// recordAudit writes an audit row for the given operation when audit
// logging is enabled and the table is in scope. The full-row diff
// (used for Create/Delete and the new-values side of plain Update) is
// built here, after the enabled/in-scope gate, so no map is allocated
// on the common path where no audit sink is configured. Tracked.Save
// builds its own old/new delta and calls writeAuditRow directly. The
// row is written through q.exec, so it joins the active transaction
// when the query is bound to one.
func (q *BaseQuery) recordAudit(ctx context.Context, operation string, entity any) error {
	st := q.client.audit
	if st == nil || !st.shouldAudit(q.table) {
		return nil
	}
	return q.client.writeAuditRow(ctx, q.exec, st, q.table, operation, pkStringFromMeta(entity, q.meta), rowToMap(entity, q.meta))
}

// pkStringFromMeta renders the entity's primary key as a string for
// the audit `pk` column. Composite PKs (meta.CompositePK) are joined
// with ":"; single PKs use meta.PK. Returns "" when the PK can't be
// resolved (best-effort — the audit row is still useful). Shared by
// the BaseQuery CRUD path and Tracked.Save so both honour composite
// PKs identically.
func pkStringFromMeta(entity any, meta *ModelMeta) string {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || meta == nil {
		return ""
	}
	if len(meta.CompositePK) > 0 {
		parts := make([]string, 0, len(meta.CompositePK))
		for _, pk := range meta.CompositePK {
			parts = append(parts, fmt.Sprint(v.Field(pk.Index).Interface()))
		}
		return strings.Join(parts, ":")
	}
	if meta.HasPK {
		return fmt.Sprint(v.Field(meta.PK.Index).Interface())
	}
	return ""
}

// writeAuditRow inserts a single audit record via exec. It builds the
// INSERT by hand (not through Query[T].Create) so it never recurses
// into the audit / event pipeline and never needs a "skip" flag.
func (c *Client) writeAuditRow(ctx context.Context, exec Executor, st *auditState, table, operation, pk string, diff map[string]any) error {
	var userID, tenantID string
	if st.cfg.UserFromContext != nil {
		userID = st.cfg.UserFromContext(ctx)
	}
	if st.cfg.TenantFromContext != nil {
		tenantID = st.cfg.TenantFromContext(ctx)
	}

	cols := []string{"ts", "tenant_id", "user_id", "table_name", "operation", "pk", "diff"}
	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = c.dialect.Placeholder(i + 1)
	}
	sqlStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		c.dialect.Quote(auditTableName),
		strings.Join(quoteAll(c.dialect, cols), ", "),
		strings.Join(placeholders, ", "),
	)

	diffVal := JSON[map[string]any]{V: diff}
	args := []any{time.Now().UTC(), tenantID, userID, table, operation, pk, diffVal}

	if _, err := exec.ExecContext(ctx, sqlStr, args...); err != nil {
		return fmt.Errorf("audit write for %s/%s: %w", table, operation, err)
	}
	return nil
}

func quoteAll(d Dialect, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = d.Quote(n)
	}
	return out
}

// rowToMap builds a {column: value} map of the model's persisted
// fields, used as the audit diff for Create/Delete and the new-value
// side of plain Update. Reflection here runs only when audit logging
// is enabled (opt-in via EnableAuditLog) and reuses the cached
// ModelMeta, so it stays off the default hot path (ADR-0002 / CLAUDE
// rule 5).
func rowToMap(entity any, meta *ModelMeta) map[string]any {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || meta == nil {
		return nil
	}
	out := make(map[string]any, len(meta.Fields))
	for i := range meta.Fields {
		fm := &meta.Fields[i]
		if fm.Column == "" {
			continue
		}
		out[fm.Column] = v.Field(fm.Index).Interface()
	}
	return out
}
