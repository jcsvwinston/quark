// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"reflect"

	"github.com/jcsvwinston/quark/internal/schema"
)

// Re-export internal types so the public API remains unchanged.

// ModelMeta is the cached metadata for a model struct.
type ModelMeta = schema.ModelMeta

// FieldMeta is the metadata for a single struct field.
type FieldMeta = schema.FieldMeta

// RelationMeta is the metadata for a model relation.
type RelationMeta = schema.RelationMeta

// pkMeta holds primary key metadata (kept lowercase for internal use).
type pkMeta = schema.PKMeta

// GetModelMeta returns the cached metadata for model type T.
func GetModelMeta[T any]() *ModelMeta {
	return schema.GetModelMeta[T]()
}

// GetModelMetaByType returns the cached metadata for a reflect.Type.
func GetModelMetaByType(t reflect.Type) *ModelMeta {
	return schema.GetModelMetaByType(t)
}

// toSnakeCase converts CamelCase to snake_case (delegates to internal/schema).
func toSnakeCase(s string) string {
	return schema.ToSnakeCase(s)
}

// pluralize applies basic English pluralization (delegates to internal/schema).
func pluralize(s string) string {
	return schema.Pluralize(s)
}

// findPK finds the primary key field in a struct value (delegates to internal/schema).
func findPK(v reflect.Value) (pkMeta, bool) {
	return schema.FindPK(v)
}
