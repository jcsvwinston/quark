// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

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
		pKey := val.Field(parentFieldMeta.Index).Interface()
		if reflect.ValueOf(pKey).IsZero() {
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
		placeholders := make([]string, len(chunk))
		for i := range chunk {
			placeholders[i] = q.dialect.Placeholder(i + 1)
		}
		args := make([]any, 0, len(chunk)+1)
		args = append(args, chunk...)

		var whereClauses []string
		whereClauses = append(whereClauses, fmt.Sprintf("%s IN (%s)", q.dialect.Quote(foreignCol), strings.Join(placeholders, ", ")))
		if hasTenantCol {
			whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(len(chunk)+1)))
			args = append(args, q.tenantID)
		}

		query := fmt.Sprintf("SELECT * FROM %s WHERE %s",
			q.dialect.Quote(relModel.Table),
			strings.Join(whereClauses, " AND "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		rows, err := q.executeQuery(ctx, query, args)
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

	var parentKeys []any
	parentKeyMap := make(map[any][]int)
	for i := 0; i < parents.Len(); i++ {
		val := indirect(parents.Index(i))
		pKey := val.Field(parentFieldMeta.Index).Interface()
		if reflect.ValueOf(pKey).IsZero() {
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
		joinPlaceholders := make([]string, len(chunk))
		for i := range chunk {
			joinPlaceholders[i] = q.dialect.Placeholder(i + 1)
		}
		joinQuery := fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s IN (%s)",
			q.dialect.Quote(relMeta.JoinFK),
			q.dialect.Quote(relMeta.JoinRefFK),
			q.dialect.Quote(relMeta.JoinTable),
			q.dialect.Quote(relMeta.JoinFK),
			strings.Join(joinPlaceholders, ", "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		joinRows, err := q.executeQuery(ctx, joinQuery, chunk)
		if err != nil {
			return fmt.Errorf("failed to load join table for relation %s: %w", relName, err)
		}
		defer joinRows.Close()
		for joinRows.Next() {
			var parentID, relatedID any
			if err := joinRows.Scan(&parentID, &relatedID); err != nil {
				return err
			}
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

	pkFieldMeta, ok := relModel.FieldByCol[strings.ToLower(relModel.PK.Column)]
	if !ok {
		return fmt.Errorf("could not find PK column %s in related model", relModel.PK.Column)
	}

	return chunkParentKeys(relatedKeys, func(chunk []any) error {
		relPlaceholders := make([]string, len(chunk))
		for i := range chunk {
			relPlaceholders[i] = q.dialect.Placeholder(i + 1)
		}
		args := make([]any, 0, len(chunk)+1)
		args = append(args, chunk...)

		var whereClauses []string
		whereClauses = append(whereClauses, fmt.Sprintf("%s IN (%s)", q.dialect.Quote(relModel.PK.Column), strings.Join(relPlaceholders, ", ")))
		if hasTenantCol {
			whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(len(chunk)+1)))
			args = append(args, q.tenantID)
		}

		relQuery := fmt.Sprintf("SELECT * FROM %s WHERE %s",
			q.dialect.Quote(relModel.Table),
			strings.Join(whereClauses, " AND "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		rows, err := q.executeQuery(ctx, relQuery, args)
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
				if fm, ok := relModel.FieldByCol[col]; ok {
					scanDest[i] = makeScanDest(relVal.Field(fm.Index), q.preloadColumnTZ(relModel, fm))
				} else {
					var discard any
					scanDest[i] = &discard
				}
			}
			if err := rows.Scan(scanDest...); err != nil {
				return err
			}
			relatedID := relVal.Field(pkFieldMeta.Index).Interface()
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
	parentFieldMeta, ok := ownerMeta.FieldByCol[parentCol]
	if !ok {
		return fmt.Errorf("could not find parent PK column %s for polymorphic relation %s", parentCol, relName)
	}

	var parentKeys []any
	parentKeyMap := make(map[any][]int)
	for i := 0; i < parents.Len(); i++ {
		val := indirect(parents.Index(i))
		pKey := val.Field(parentFieldMeta.Index).Interface()
		if reflect.ValueOf(pKey).IsZero() {
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
		placeholders := make([]string, len(chunk))
		for i := range chunk {
			placeholders[i] = q.dialect.Placeholder(i + 2)
		}
		var whereClauses []string
		whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(relMeta.PolyTypeColumn), q.dialect.Placeholder(1)))
		whereClauses = append(whereClauses, fmt.Sprintf("%s IN (%s)", q.dialect.Quote(relMeta.PolyIDColumn), strings.Join(placeholders, ", ")))
		args := append([]any{relMeta.PolyType}, chunk...)
		if hasTenantCol {
			whereClauses = append(whereClauses, fmt.Sprintf("%s = %s", q.dialect.Quote(q.tenantCol), q.dialect.Placeholder(len(args)+1)))
			args = append(args, q.tenantID)
		}
		polyQuery := fmt.Sprintf("SELECT * FROM %s WHERE %s",
			q.dialect.Quote(relModel.Table),
			strings.Join(whereClauses, " AND "),
		)
		ctx, cancel := context.WithTimeout(q.ctx, q.client.limits.QueryTimeout)
		defer cancel()

		rows, err := q.executeQuery(ctx, polyQuery, args)
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
	cols, _ := rows.Columns()
	foreignFieldMeta, ok := relModel.FieldByCol[strings.ToLower(foreignCol)]
	if !ok {
		return fmt.Errorf("could not find foreign column %s in related model", foreignCol)
	}
	for rows.Next() {
		relPtr := reflect.New(relMeta.RefType)
		relVal := relPtr.Elem()
		scanDest := make([]any, len(cols))
		for i, col := range cols {
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
		fKey := relVal.Field(foreignFieldMeta.Index).Interface()
		if parentIndexes, ok := keyMap[fKey]; ok {
			for _, pIdx := range parentIndexes {
				parentVal := indirect(parents.Index(pIdx))
				relField := parentVal.FieldByName(relName)
				if relMeta.IsSlice {
					relField.Set(reflect.Append(relField, relVal))
				} else if relField.Kind() == reflect.Ptr {
					relField.Set(relPtr)
				} else {
					relField.Set(relVal)
				}
			}
		}
	}
	return rows.Err()
}

// scanAndMapPolymorphic does the polymorphic equivalent.
func (q *BaseQuery) scanAndMapPolymorphic(rows *sql.Rows, parents reflect.Value, relName string, relMeta *RelationMeta, relModel *ModelMeta, parentKeyMap map[any][]int) error {
	cols, _ := rows.Columns()
	polyIDFieldMeta, ok := relModel.FieldByCol[strings.ToLower(relMeta.PolyIDColumn)]
	if !ok {
		return fmt.Errorf("could not find polymorphic ID column %s in related model", relMeta.PolyIDColumn)
	}
	for rows.Next() {
		relPtr := reflect.New(relMeta.RefType)
		relVal := relPtr.Elem()
		scanDest := make([]any, len(cols))
		for i, col := range cols {
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
		parentID := relVal.Field(polyIDFieldMeta.Index).Interface()
		if parentIndexes, ok := parentKeyMap[parentID]; ok {
			for _, pIdx := range parentIndexes {
				parentVal := indirect(parents.Index(pIdx))
				relField := parentVal.FieldByName(relName)
				if relMeta.IsSlice {
					relField.Set(reflect.Append(relField, relVal))
				} else if relField.Kind() == reflect.Ptr {
					relField.Set(relPtr)
				} else {
					relField.Set(relVal)
				}
			}
		}
	}
	return rows.Err()
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
