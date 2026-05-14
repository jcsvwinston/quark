// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql"
	"strings"
	"time"
)

// Per-column timezone support (ADR-0010).
//
// Two knobs drive the feature:
//   - quark.WithDefaultTZ(loc)   — a Client-wide fallback location.
//   - quark:"tz=Europe/Madrid"   — a per-column override tag.
//
// The wire contract is UTC-always: when a column resolves to a non-nil
// location, time.Time values are converted to UTC on the way to the
// driver (so every dialect stores the same instant) and to loc in memory
// on the way back. loc therefore only affects how the struct field reads
// in Go, never what is persisted. A column with neither a tag nor a
// client default passes through to the driver untouched — the feature is
// fully opt-in and adds nothing to the v0.6 behaviour for callers that
// don't use it.
//
// The helpers here are deliberately reflection-free: the bind/scan hot
// paths gate on BaseQuery.tzActive (an O(1) flag read) and only then do a
// FieldByCol map lookup plus a type switch. ADR-0002 (no extra reflect in
// hot paths) is respected.

// bindTimeValue normalises a field value destined for a SQL bind
// parameter. When loc is non-nil, the three time-shaped values Quark
// binds — time.Time, *time.Time and Nullable[time.Time] (an alias of
// sql.Null[time.Time]) — are converted to UTC. Every other value, and
// every value when loc is nil, passes through unchanged.
func bindTimeValue(val any, loc *time.Location) any {
	if loc == nil {
		return val
	}
	switch t := val.(type) {
	case time.Time:
		return t.UTC()
	case *time.Time:
		if t == nil {
			return t
		}
		u := t.UTC()
		return &u
	case sql.Null[time.Time]:
		if t.Valid {
			t.V = t.V.UTC()
		}
		return t
	default:
		return val
	}
}

// resolveFieldTZ returns the location governing a column's time.Time
// bind/scan conversion, following the precedence
//
//	column tag (FieldMeta.TZ) → client default → nil (driver pass-through).
func resolveFieldTZ(fm *FieldMeta, clientDefault *time.Location) *time.Location {
	if fm != nil && fm.TZ != nil {
		return fm.TZ
	}
	return clientDefault
}

// tzActive reports whether the per-column timezone feature is in play for
// this query at all — either the model declares at least one valid tz tag
// or the client carries a default. Models and clients that use no
// timezones short-circuit here and never reach a FieldByCol lookup.
func (q *BaseQuery) tzActive() bool {
	if q.meta != nil && q.meta.HasTZ {
		return true
	}
	return q.client != nil && q.client.defaultTZ != nil
}

// columnTZ resolves the timezone for a single column on this query, or
// nil when the column passes through untouched. Callers should gate on
// tzActive first so the common no-timezone path skips the map lookup.
func (q *BaseQuery) columnTZ(dbTag string) *time.Location {
	var fm *FieldMeta
	if q.meta != nil {
		fm = q.meta.FieldByCol[strings.ToLower(dbTag)]
	}
	var clientDefault *time.Location
	if q.client != nil {
		clientDefault = q.client.defaultTZ
	}
	return resolveFieldTZ(fm, clientDefault)
}

// bindColumnArg is the bind-path convenience: it resolves the column's
// timezone and applies the wire conversion in one call. When the feature
// is inactive for this query it returns val untouched with no lookup.
func (q *BaseQuery) bindColumnArg(dbTag string, val any) any {
	if !q.tzActive() {
		return val
	}
	return bindTimeValue(val, q.columnTZ(dbTag))
}

// preloadColumnTZ resolves the timezone for a preloaded relation's column,
// or nil when neither the related model nor the client uses timezones. The
// preload loaders scan related-model rows with their own ModelMeta (not the
// root query's), so per-column tz tags on the related model are honoured the
// same way as on a directly-loaded model.
func (q *BaseQuery) preloadColumnTZ(relModel *ModelMeta, fm *FieldMeta) *time.Location {
	var clientDefault *time.Location
	if q.client != nil {
		clientDefault = q.client.defaultTZ
	}
	if (relModel == nil || !relModel.HasTZ) && clientDefault == nil {
		return nil
	}
	return resolveFieldTZ(fm, clientDefault)
}
