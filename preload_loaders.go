// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// preloadQuery runs one eager-loading SELECT and feeds it into the observer
// pipeline as a QueryEvent with Operation "PRELOAD" (AQ-04). The loaders
// used to call executeQuery directly, so the batched `WHERE fk IN (...)`
// child SELECTs — precisely the heavy queries an operator wants to see —
// were invisible to every QueryObserver and to the slow-query log (which
// piggybacks on the same notifyObservers pipeline). The row count is not
// known at emit time (the rows are scanned by the caller), so Rows stays 0,
// matching the "SELECT (stream)" event contract. A failed statement is
// reported by the seam, under the same Operation.
func (q *BaseQuery) preloadQuery(ctx context.Context, table, sqlStr string, args []any) (*sql.Rows, error) {
	start := time.Now()
	rows, err := q.executeQuery(ctx, "PRELOAD", table, sqlStr, args)
	if err != nil {
		return nil, err
	}
	q.notifyObservers(QueryEvent{
		SQL:       sqlStr,
		Args:      args,
		Duration:  time.Since(start),
		Table:     table,
		Operation: "PRELOAD",
		Kind:      StatementQuery,
	})
	return rows, nil
}

// preloadKeyFilter renders the condition that selects the related rows of
// one batch of keys, with first as the index of its first placeholder, and
// returns the arguments it binds: "col IN ($1, …, $n)", one placeholder per
// key, or on PostgreSQL "col = ANY($1)", one array for all of them.
//
// On PostgreSQL the IN list is the slow form. Its text changes with the
// number of keys and every execution carries n parameters to plan with; one
// array parameter is one statement for any number of keys. On the engine
// bench the hand-written preload of 100 parents took 1.5 to 1.7 times as long
// with the IN list as with = ANY($1) (QK-35).
//
// The array is bound as its text form — {1,2,3} or {"a","b"} — in a string,
// which every PostgreSQL driver Quark accepts binds unchanged (pgx, lib/pq and
// pq alike) and the server reads as an array of the column's type, exactly as
// it read each IN parameter before. A batch whose keys pgArrayKeys cannot
// write that way keeps the IN list, and so does every other engine.
func (q *BaseQuery) preloadKeyFilter(col string, keys []any, first int) (string, []any) {
	quoted := q.dialect.Quote(col)
	if q.dialect.Name() == "postgres" {
		if lit, ok := pgArrayKeys(keys); ok {
			return quoted + " = ANY(" + q.dialect.Placeholder(first) + ")", []any{lit}
		}
	}
	var b strings.Builder
	b.Grow(len(quoted) + 6 + len(keys)*6)
	b.WriteString(quoted)
	b.WriteString(" IN (")
	for i := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		writePlaceholder(&b, q.dialect, first+i)
	}
	b.WriteByte(')')
	return b.String(), keys
}

// pgArrayKeys writes keys as the text of a PostgreSQL array, and reports
// whether it could. It can when every key is an integer of any width, or
// every key is a string — what a primary or foreign key field holds, named
// types included. It cannot, and the caller keeps the IN list, for a key
// that implements driver.Valuer (the driver binds what Value returns, not
// the Go value: a UUID type, a custom ID), for any other kind, and for a
// mix of the two kinds.
func pgArrayKeys(keys []any) (string, bool) {
	if len(keys) == 0 {
		return "", false
	}
	b := make([]byte, 0, 2+len(keys)*8)
	b = append(b, '{')
	var strs bool
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		var isStr bool
		switch v := k.(type) {
		case int64:
			b = strconv.AppendInt(b, v, 10)
		case int:
			b = strconv.AppendInt(b, int64(v), 10)
		case int32:
			b = strconv.AppendInt(b, int64(v), 10)
		case string:
			b, isStr = appendPGArrayString(b, v), true
		default:
			if _, ok := k.(driver.Valuer); ok {
				return "", false
			}
			rv := reflect.ValueOf(k)
			switch rv.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				b = strconv.AppendInt(b, rv.Int(), 10)
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				b = strconv.AppendUint(b, rv.Uint(), 10)
			case reflect.String:
				b, isStr = appendPGArrayString(b, rv.String()), true
			default:
				return "", false
			}
		}
		if i == 0 {
			strs = isStr
		} else if isStr != strs {
			return "", false
		}
	}
	b = append(b, '}')
	return string(b), true
}

// appendPGArrayString appends s as a quoted element of an array literal:
// inside the quotes only the backslash and the double quote are special.
func appendPGArrayString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\\' || c == '"' {
			b = append(b, '\\')
		}
		b = append(b, s[i])
	}
	return append(b, '"')
}

// loadStandard handles has_one / has_many / belongs_to relations against a
// reflect.Value of the parent slice. Refactor of the old generic
// loadStandardRelation that worked on []T — the BaseQuery form is needed so
// nested-preload (Phase 2) can recurse into a parent slice whose element
// type is decided at run time.
func (q *BaseQuery) loadStandard(parents reflect.Value, ownerMeta *ModelMeta, relName string, relMeta *RelationMeta, relModel *ModelMeta) error {
	var parentCol string
	if relMeta.Type == "belongs_to" {
		parentCol = relMeta.JoinCol
	} else {
		parentCol = ownerMeta.PK.Column
	}

	parentFieldMeta, ok := ownerMeta.FieldByCol[strings.ToLower(parentCol)]
	if !ok {
		for _, fm := range ownerMeta.Fields {
			if strings.EqualFold(fm.Type.Name(), parentCol) {
				parentFieldMeta = &fm
				break
			}
		}
		if parentFieldMeta == nil {
			return fmt.Errorf("could not find parent column %s for relation %s", parentCol, relName)
		}
	}

	var parentKeys []any
	keyMap := make(map[any][]int)
	for i := 0; i < parents.Len(); i++ {
		val := indirect(parents.Index(i))
		pKey := normalizeKey(val.Field(parentFieldMeta.Index).Interface())
		if pKey == nil || reflect.ValueOf(pKey).IsZero() {
			continue
		}
		parentKeys = append(parentKeys, pKey)
		keyMap[pKey] = append(keyMap[pKey], i)
	}
	if len(parentKeys) == 0 {
		return nil
	}

	var foreignCol string
	if relMeta.Type == "belongs_to" {
		foreignCol = relModel.PK.Column
	} else {
		foreignCol = relMeta.JoinCol
	}

	hasTenantCol := false
	if q.tenantID != "" && q.tenantCol != "" {
		if _, ok := relModel.FieldByCol[strings.ToLower(q.tenantCol)]; ok {
			hasTenantCol = true
		}
	}

	return chunkParentKeys(parentKeys, func(chunk []any) error {
		keyFilter, keyArgs := q.preloadKeyFilter(foreignCol, chunk, 1)
		args := make([]any, 0, len(keyArgs)+1)
		args = append(args, keyArgs...)

		var whereClauses []string
		whereClauses = append(whereClauses, keyFilter)
		if hasTenantCol {
			whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(len(args)+1)))
			args = append(args, q.tenantID)
		}
		// Per-relation filters from PreloadWhere. They narrow which children
		// load; they never drop a parent that ends up with none. They are
		// rendered by buildWhereClause, the renderer of every other caller
		// condition (QK-39), over a query scoped to the RELATED model — its
		// table and metadata, no joins — so IN, BETWEEN and the column check
		// read the relation's columns, not the parent's.
		if conds := q.preloadConds[relName]; len(conds) > 0 {
			rq := &BaseQuery{
				client:  q.client,
				ctx:     q.ctx,
				dialect: q.dialect,
				guard:   q.guard,
				table:   relModel.Table,
				meta:    relModel,
			}
			// This loader always upper-cased the operator, so `is null`
			// keeps meaning IS NULL here.
			norm := make([]condition, len(conds))
			for i, c := range conds {
				c.operator = strings.ToUpper(strings.TrimSpace(c.operator))
				norm[i] = c
			}
			frag, condArgs, err := rq.buildWhereClause(norm, len(args)+1)
			if err != nil {
				return err
			}
			whereClauses = append(whereClauses, "("+frag+")")
			args = append(args, condArgs...)
		}

		query := fmt.Sprintf("SELECT * FROM %s WHERE %s",
			q.qualifiedTable(relModel.Table),
			strings.Join(whereClauses, " AND "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		rows, err := q.preloadQuery(ctx, relModel.Table, query, args)
		if err != nil {
			return fmt.Errorf("failed to load relation %s: %w", relName, err)
		}
		defer rows.Close()

		return q.scanAndMapStandard(rows, parents, relName, relMeta, relModel, foreignCol, keyMap)
	})
}

// loadM2M is the BaseQuery / reflect-based equivalent of the old
// loadM2MRelation method.
func (q *BaseQuery) loadM2M(parents reflect.Value, ownerMeta *ModelMeta, relName string, relMeta *RelationMeta, relModel *ModelMeta) error {
	parentCol := ownerMeta.PK.Column
	parentFieldMeta, ok := ownerMeta.FieldByCol[strings.ToLower(parentCol)]
	if !ok {
		return fmt.Errorf("could not find parent PK column %s for m2m relation %s", parentCol, relName)
	}
	pkFieldMeta, ok := relModel.FieldByCol[strings.ToLower(relModel.PK.Column)]
	if !ok {
		return fmt.Errorf("could not find PK column %s in related model", relModel.PK.Column)
	}

	var parentKeys []any
	parentKeyMap := make(map[any][]int)
	for i := 0; i < parents.Len(); i++ {
		val := indirect(parents.Index(i))
		pKey := normalizeKey(val.Field(parentFieldMeta.Index).Interface())
		if pKey == nil || reflect.ValueOf(pKey).IsZero() {
			continue
		}
		parentKeys = append(parentKeys, pKey)
		parentKeyMap[pKey] = append(parentKeyMap[pKey], i)
	}
	if len(parentKeys) == 0 {
		return nil
	}

	relatedToParent := make(map[any][]any)
	var relatedKeys []any
	seenRelated := make(map[any]bool)

	if err := chunkParentKeys(parentKeys, func(chunk []any) error {
		keyFilter, keyArgs := q.preloadKeyFilter(relMeta.JoinFK, chunk, 1)
		joinQuery := fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s",
			q.dialect.Quote(relMeta.JoinFK),
			q.dialect.Quote(relMeta.JoinRefFK),
			q.qualifiedTable(relMeta.JoinTable),
			keyFilter,
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		joinRows, err := q.preloadQuery(ctx, relMeta.JoinTable, joinQuery, keyArgs)
		if err != nil {
			return fmt.Errorf("failed to load join table for relation %s: %w", relName, err)
		}
		defer joinRows.Close()
		for joinRows.Next() {
			// Scan the join-table FK columns into destinations typed to the
			// owner and related PK fields. Scanning into interface{} lets the
			// driver choose a dynamic type (e.g. go-ora returns NUMBER as a type
			// that is not == the struct's int64 PK), so the key match below
			// fails and the relation silently loads empty (BB-7). Typed scan
			// targets yield the same Go type as the struct PKs on every driver.
			parentPtr := reflect.New(parentFieldMeta.Type)
			relatedPtr := reflect.New(pkFieldMeta.Type)
			if err := joinRows.Scan(
				makeScanDest(parentPtr.Elem(), nil),
				makeScanDest(relatedPtr.Elem(), nil),
			); err != nil {
				return err
			}
			parentID := normalizeKey(parentPtr.Elem().Interface())
			relatedID := normalizeKey(relatedPtr.Elem().Interface())
			relatedToParent[relatedID] = append(relatedToParent[relatedID], parentID)
			if !seenRelated[relatedID] {
				relatedKeys = append(relatedKeys, relatedID)
				seenRelated[relatedID] = true
			}
		}
		return joinRows.Err()
	}); err != nil {
		return err
	}

	if len(relatedKeys) == 0 {
		return nil
	}

	hasTenantCol := false
	if q.tenantID != "" && q.tenantCol != "" {
		if _, ok := relModel.FieldByCol[strings.ToLower(q.tenantCol)]; ok {
			hasTenantCol = true
		}
	}

	return chunkParentKeys(relatedKeys, func(chunk []any) error {
		keyFilter, keyArgs := q.preloadKeyFilter(relModel.PK.Column, chunk, 1)
		args := make([]any, 0, len(keyArgs)+1)
		args = append(args, keyArgs...)

		var whereClauses []string
		whereClauses = append(whereClauses, keyFilter)
		if hasTenantCol {
			whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(len(args)+1)))
			args = append(args, q.tenantID)
		}

		relQuery := fmt.Sprintf("SELECT * FROM %s WHERE %s",
			q.qualifiedTable(relModel.Table),
			strings.Join(whereClauses, " AND "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		rows, err := q.preloadQuery(ctx, relModel.Table, relQuery, args)
		if err != nil {
			return fmt.Errorf("failed to load m2m relation %s: %w", relName, err)
		}
		defer rows.Close()

		cols, _ := rows.Columns()
		for rows.Next() {
			relPtr := reflect.New(relMeta.RefType)
			relVal := relPtr.Elem()
			scanDest := make([]any, len(cols))
			for i, col := range cols {
				// Lower-case the driver-reported column name before the field
				// lookup: Oracle returns identifiers upper-cased, and FieldByCol
				// is keyed by the lower-case db tag. Without this, no column maps
				// on Oracle, the related row scans all-zero, and the m2m match
				// below finds nothing (BB-7). Mirrors scanAndMapStandard.
				if fm, ok := relModel.FieldByCol[strings.ToLower(col)]; ok {
					scanDest[i] = makeScanDest(relVal.Field(fm.Index), q.preloadColumnTZ(relModel, fm))
				} else {
					var discard any
					scanDest[i] = &discard
				}
			}
			if err := rows.Scan(scanDest...); err != nil {
				return err
			}
			relatedID := normalizeKey(relVal.Field(pkFieldMeta.Index).Interface())
			if parentIDs, ok := relatedToParent[relatedID]; ok {
				for _, parentID := range parentIDs {
					if parentIndexes, ok := parentKeyMap[parentID]; ok {
						for _, pIdx := range parentIndexes {
							parentVal := indirect(parents.Index(pIdx))
							relField := parentVal.FieldByName(relName)
							relField.Set(reflect.Append(relField, relVal))
						}
					}
				}
			}
		}
		return rows.Err()
	})
}

// loadPolymorphic is the BaseQuery / reflect-based equivalent of the old
// loadPolymorphicRelation method.
func (q *BaseQuery) loadPolymorphic(parents reflect.Value, ownerMeta *ModelMeta, relName string, relMeta *RelationMeta, relModel *ModelMeta) error {
	parentCol := ownerMeta.PK.Column
	// Lower-case the PK column before the lookup, matching loadStandard/loadM2M.
	// FieldByCol is keyed by the lower-case db tag; an upper-cased tag (or a
	// driver that reports identifiers upper-cased) would otherwise miss here.
	parentFieldMeta, ok := ownerMeta.FieldByCol[strings.ToLower(parentCol)]
	if !ok {
		return fmt.Errorf("could not find parent PK column %s for polymorphic relation %s", parentCol, relName)
	}

	var parentKeys []any
	parentKeyMap := make(map[any][]int)
	for i := 0; i < parents.Len(); i++ {
		val := indirect(parents.Index(i))
		pKey := normalizeKey(val.Field(parentFieldMeta.Index).Interface())
		if pKey == nil || reflect.ValueOf(pKey).IsZero() {
			continue
		}
		parentKeys = append(parentKeys, pKey)
		parentKeyMap[pKey] = append(parentKeyMap[pKey], i)
	}
	if len(parentKeys) == 0 {
		return nil
	}

	hasTenantCol := false
	if q.tenantID != "" && q.tenantCol != "" {
		if _, ok := relModel.FieldByCol[strings.ToLower(q.tenantCol)]; ok {
			hasTenantCol = true
		}
	}

	return chunkParentKeys(parentKeys, func(chunk []any) error {
		keyFilter, keyArgs := q.preloadKeyFilter(relMeta.PolyIDColumn, chunk, 2)
		var whereClauses []string
		whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(relMeta.PolyTypeColumn), q.dialect.Placeholder(1)))
		whereClauses = append(whereClauses, keyFilter)
		args := append([]any{relMeta.PolyType}, keyArgs...)
		if hasTenantCol {
			whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(len(args)+1)))
			args = append(args, q.tenantID)
		}
		polyQuery := fmt.Sprintf("SELECT * FROM %s WHERE %s",
			q.qualifiedTable(relModel.Table),
			strings.Join(whereClauses, " AND "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		rows, err := q.preloadQuery(ctx, relModel.Table, polyQuery, args)
		if err != nil {
			return fmt.Errorf("failed to load polymorphic relation %s: %w", relName, err)
		}
		defer rows.Close()

		return q.scanAndMapPolymorphic(rows, parents, relName, relMeta, relModel, parentKeyMap)
	})
}

// scanAndMapStandard scans rows from a has_one / has_many / belongs_to
// load and assigns them to the parent slice via reflection.
func (q *BaseQuery) scanAndMapStandard(rows *sql.Rows, parents reflect.Value, relName string, relMeta *RelationMeta, relModel *ModelMeta, foreignCol string, keyMap map[any][]int) error {
	foreignFieldMeta, ok := relModel.FieldByCol[strings.ToLower(foreignCol)]
	if !ok {
		return fmt.Errorf("could not find foreign column %s in related model", foreignCol)
	}
	return q.scanAndMap(rows, parents, relName, relMeta, relModel, foreignFieldMeta, true, keyMap)
}

// scanAndMapPolymorphic does the polymorphic equivalent.
func (q *BaseQuery) scanAndMapPolymorphic(rows *sql.Rows, parents reflect.Value, relName string, relMeta *RelationMeta, relModel *ModelMeta, parentKeyMap map[any][]int) error {
	polyIDFieldMeta, ok := relModel.FieldByCol[strings.ToLower(relMeta.PolyIDColumn)]
	if !ok {
		return fmt.Errorf("could not find polymorphic ID column %s in related model", relMeta.PolyIDColumn)
	}
	return q.scanAndMap(rows, parents, relName, relMeta, relModel, polyIDFieldMeta, false, parentKeyMap)
}

// scanAndMap scans the related rows of one preload query and assigns each to
// the parents keyMap names for the value of its keyField. skipNilKey drops a
// row whose key is NULL before the lookup (keyMap never holds a nil key, so
// the lookup would miss it anyway).
//
// The column → field plan is resolved once per query, not per row. A slice
// relation (has_many) is assembled in two passes: every row is scanned into
// ONE reused value, whose scan targets are built once, and copied into a
// slice of all the rows; then each parent's relation slice is grown once, to
// the number of rows it receives, and filled in row order. The single-pass
// form looked the relation field up by name and called reflect.Append for
// every row and parent, growing each parent's slice one element at a time,
// and built the scan targets of every row again: together about half the
// CPU of a 100-parent, 500-child preload (QK-35).
func (q *BaseQuery) scanAndMap(rows *sql.Rows, parents reflect.Value, relName string, relMeta *RelationMeta, relModel *ModelMeta, keyField *FieldMeta, skipNilKey bool, keyMap map[any][]int) error {
	cols, _ := rows.Columns()
	fields := make([]*FieldMeta, len(cols))
	locs := make([]*time.Location, len(cols))
	natives := make([]bool, len(cols))
	for i, col := range cols {
		if fm, ok := relModel.FieldByCol[strings.ToLower(col)]; ok {
			fields[i] = fm
			locs[i] = q.preloadColumnTZ(relModel, fm)
			natives[i] = hasNativeScanDest(relMeta.RefType.Field(fm.Index).Type)
		}
	}
	relField := relationField(parents, relName)

	if !relMeta.IsSlice {
		// has_one / belongs_to: every row is its own value, because a
		// pointer field receives the address of the scanned row.
		scanDest := make([]any, len(cols))
		for rows.Next() {
			relPtr := reflect.New(relMeta.RefType)
			relVal := relPtr.Elem()
			for i, fm := range fields {
				switch {
				case fm == nil:
					var discard any
					scanDest[i] = &discard
				case natives[i]:
					scanDest[i] = makeScanDest(relVal.Field(fm.Index), locs[i])
				default:
					scanDest[i] = scanDestForPtr(relVal.Field(fm.Index).Addr().Interface(), locs[i])
				}
			}
			if err := rows.Scan(scanDest...); err != nil {
				return err
			}
			key := normalizeKey(relVal.Field(keyField.Index).Interface())
			if skipNilKey && key == nil {
				continue
			}
			for _, pIdx := range keyMap[key] {
				f := relField(pIdx)
				if f.Kind() == reflect.Ptr {
					f.Set(relPtr)
				} else {
					f.Set(relVal)
				}
			}
		}
		return rows.Err()
	}

	// has_many, first pass: scan every row into row, whose scan targets
	// point into it and are built once; zero it before each row so nothing
	// of the previous row survives into a column the driver leaves alone,
	// as a fresh value per row guaranteed before.
	row := reflect.New(relMeta.RefType).Elem()
	scanDest := make([]any, len(cols))
	var discard any
	for i, fm := range fields {
		if fm != nil {
			scanDest[i] = makeScanDest(row.Field(fm.Index), locs[i])
		} else {
			scanDest[i] = &discard
		}
	}
	all := reflect.New(reflect.SliceOf(relMeta.RefType)).Elem()
	var owners [][]int
	counts := make([]int, parents.Len())
	for rows.Next() {
		row.SetZero()
		if err := rows.Scan(scanDest...); err != nil {
			return err
		}
		key := normalizeKey(row.Field(keyField.Index).Interface())
		if skipNilKey && key == nil {
			continue
		}
		idx, ok := keyMap[key]
		if !ok {
			continue
		}
		n := all.Len()
		if n == all.Cap() {
			all.Grow(1)
		}
		all.SetLen(n + 1)
		all.Index(n).Set(row)
		owners = append(owners, idx)
		for _, pIdx := range idx {
			counts[pIdx]++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Second pass: one growth per parent, then the rows in the order they
	// came, appended after whatever the relation already held.
	for pIdx, n := range counts {
		if n > 0 {
			relField(pIdx).Grow(n)
		}
	}
	for c, idx := range owners {
		child := all.Index(c)
		for _, pIdx := range idx {
			f := relField(pIdx)
			l := f.Len()
			f.SetLen(l + 1)
			f.Index(l).Set(child)
		}
	}
	return nil
}

// relationField returns a function giving the relation field relName of the
// parent at index i of parents ([]T or []*T), with the field's index path
// resolved once instead of by name on every call.
func relationField(parents reflect.Value, relName string) func(i int) reflect.Value {
	t := parents.Type().Elem()
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	sf, ok := t.FieldByName(relName)
	if !ok || len(sf.Index) != 1 {
		// Not found, or promoted from an embedded struct: keep the lookup by
		// name, which resolves (or fails) exactly as it always did.
		return func(i int) reflect.Value {
			return indirect(parents.Index(i)).FieldByName(relName)
		}
	}
	fi := sf.Index[0]
	return func(i int) reflect.Value {
		return indirect(parents.Index(i)).Field(fi)
	}
}

// gatherLoadedChildren walks parents, extracts each parent's named relation
// field (a slice or single pointer/value), and concatenates everything into
// a single flat reflect.Value of []*RefType.
//
// We deliberately collect POINTERS, not value copies: when the recursive
// loadPreloadTree later mutates these elements (assigning to their
// nested relation fields), the writes alias back into the original
// parent's relation slice. With value-copy semantics those mutations
// would land on copies and the user would see empty nested fields.
//
// Parents must be an addressable reflect.Value (loadRelations passes
// `reflect.ValueOf(&results).Elem()` for that reason).
func gatherLoadedChildren(parents reflect.Value, relName string, relMeta *RelationMeta) reflect.Value {
	ptrSliceType := reflect.SliceOf(reflect.PtrTo(relMeta.RefType))
	out := reflect.MakeSlice(ptrSliceType, 0, parents.Len())
	for i := 0; i < parents.Len(); i++ {
		val := indirect(parents.Index(i))
		f := val.FieldByName(relName)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.Slice:
			for j := 0; j < f.Len(); j++ {
				elem := f.Index(j)
				if elem.CanAddr() {
					out = reflect.Append(out, elem.Addr())
				}
			}
		case reflect.Ptr:
			if !f.IsNil() {
				out = reflect.Append(out, f)
			}
		case reflect.Struct:
			if !f.IsZero() && f.CanAddr() {
				out = reflect.Append(out, f.Addr())
			}
		}
	}
	return out
}

// indirect dereferences a pointer reflect.Value, returning the pointed-at
// struct. For a non-pointer value it returns the value unchanged. Used by
// the loaders so they handle both []T and []*T parent slices uniformly.
func indirect(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Ptr {
		return v.Elem()
	}
	return v
}

// normalizeKey unwraps a pointer join key to its pointee so a nullable FK
// (e.g. a `*int64` on a self-referential or optional relation) compares equal
// to the non-pointer key on the other side of the join. Without this, a
// belongs_to whose FK column maps to a `*int64` field produces a `*int64`
// map key while the related row's PK scans to an `int64`, so the keys never
// match and the relation silently loads as nil/empty. A nil pointer yields
// nil — a NULL FK matches no parent, so the caller skips it.
func normalizeKey(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		return rv.Elem().Interface()
	}
	return v
}
