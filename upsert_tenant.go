// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"
)

// QK-43. Under RowLevelSecurityClient the update branch of Upsert and
// UpsertBatch was not confined to the tenant: ON CONFLICT … DO UPDATE, ON
// DUPLICATE KEY UPDATE and MERGE … WHEN MATCHED applied to whichever row held
// the conflicting key, in any tenant. Tenant A upserting a code that tenant B
// already had rewrote B's row — and on SQL Server and Oracle, where an empty
// updateCols means "every column but the conflict ones", moved it into A by
// writing A's id into the tenant column.
//
// The update branch now carries the tenant predicate on every engine
// (rendered by conflictClause and mergeMatchedClause, upsert_conflict.go):
//
//	PostgreSQL, SQLite  ON CONFLICT (…) DO UPDATE SET … WHERE <table>.<tenant> = ?
//	MySQL, MariaDB      ON DUPLICATE KEY UPDATE c = IF(<conflict match> AND <tenant> = ?, VALUES(c), c), …
//	SQL Server          MERGE … WHEN MATCHED AND target.<tenant> = @p THEN UPDATE …
//	Oracle              MERGE … WHEN MATCHED THEN UPDATE SET … WHERE target.<tenant> = :n
//
// so a row of another tenant is never written. A conflict with such a row
// cannot be an insert either — the key is taken — so the call returns
// ErrConstraintViolation, the error a Create of the same key returns, and
// writes nothing: UpsertBatch runs in a transaction under this strategy and
// rolls the batch back. A dialect quark does not know how to guard refuses
// the upsert with ErrUnsupportedFeature rather than run it unguarded.
// Without updateCols there is no update branch on any engine, and a
// conflict with another tenant's row is left alone and not reported, as a
// conflict with the tenant's own row is (QK-59).

// tenantGuarded reports whether the query's writes are confined by
// RowLevelSecurityClient: a resolved tenant and the column that holds it.
func (q *BaseQuery) tenantGuarded() bool {
	return q.tenantID != "" && q.tenantCol != ""
}

// errUpsertOutsideTenant is what an upsert returns when its conflict key is
// held by a row of another tenant (QK-43).
func (q *BaseQuery) errUpsertOutsideTenant() error {
	return fmt.Errorf("%w: upsert into %s under RowLevelSecurityClient: a row with the same conflict key belongs to another tenant; it was not updated and no row was inserted",
		ErrConstraintViolation, q.table)
}

// warnForeignTenantValue logs a value for the tenant column that was replaced
// by the resolved tenant (QK-42, QK-44).
func (q *BaseQuery) warnForeignTenantValue() {
	if q.client == nil || q.client.logger == nil {
		return
	}
	q.client.logger.Warn("the write carried another tenant's id in the tenant column; RowLevelSecurityClient writes the resolved tenant instead",
		"event", "quark.tenant.foreign_value_replaced",
		"table", q.table,
		"column", q.tenantCol,
	)
}

// upsertFamily names the conflict clause an INSERT-based upsert uses on the
// query's dialect: "on_conflict" (PostgreSQL, SQLite), "duplicate_key"
// (MySQL, MariaDB), "merge" (SQL Server, Oracle), or "" for a dialect quark
// does not know how to confine to a tenant.
func (q *BaseQuery) upsertFamily() string {
	switch q.dialect.Name() {
	case "postgres", "sqlite":
		return "on_conflict"
	case "mysql", "mariadb":
		return "duplicate_key"
	case "mssql", "oracle":
		return "merge"
	default:
		return ""
	}
}

// checkTenantUpsert refuses an upsert under RowLevelSecurityClient that
// cannot be confined: a dialect without a known guarded form, or a model
// without the tenant column (the guard would name a column that is not
// there). Nil when the query is not tenant-guarded.
func (q *BaseQuery) checkTenantUpsert() error {
	if !q.tenantGuarded() {
		return nil
	}
	if q.upsertFamily() == "" {
		return fmt.Errorf("%w: Upsert under RowLevelSecurityClient needs an update branch confined to the tenant, and quark has no such form for dialect %q",
			ErrUnsupportedFeature, q.dialect.Name())
	}
	if q.meta == nil {
		return fmt.Errorf("%w: Upsert under RowLevelSecurityClient needs model metadata", ErrInvalidModel)
	}
	if _, ok := q.meta.FieldByCol[strings.ToLower(q.tenantCol)]; !ok {
		return fmt.Errorf("%w: Upsert under RowLevelSecurityClient needs the tenant column %q on %s",
			ErrInvalidModel, q.tenantCol, q.table)
	}
	return q.guard.ValidateIdentifier(q.tenantCol)
}

// confineTenantColumn returns data with every key that names the tenant
// column set to the resolved tenant, under RowLevelSecurityClient (QK-44).
// UpdateMap wrote its map verbatim, so {"tenant_id": "tb"} moved a row of
// tenant ta into tb — the move Create and the updates by entity no longer
// allow since QK-42. A different value is replaced and logged, as there. The
// caller's map is not modified.
func (q *BaseQuery) confineTenantColumn(data map[string]any) map[string]any {
	if !q.tenantGuarded() {
		return data
	}
	var out map[string]any
	for k, val := range data {
		if !strings.EqualFold(k, q.tenantCol) {
			continue
		}
		if s, ok := val.(string); ok && s == q.tenantID {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(data))
			for k2, v2 := range data {
				out[k2] = v2
			}
		}
		out[k] = q.tenantID
	}
	if out == nil {
		return data
	}
	q.warnForeignTenantValue()
	return out
}

// splitReturning separates an INSERT built by buildInsert from its RETURNING
// clause, which an upsert appends after its conflict clause.
func splitReturning(insert string) (string, string) {
	if i := strings.Index(insert, " RETURNING "); i != -1 {
		return insert[:i], insert[i:]
	}
	return insert, ""
}
