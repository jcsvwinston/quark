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

// TableNamer is implemented by a model that names its own table:
//
//	TableName() string
//
// Without it, a model's table is its type name in snake case, pluralised
// (User → users). Quark asks once per model type, on a zero *T, and caches
// the answer in the model's metadata, which is where Migrate and the query
// builder read the table from: the method may have a value or a pointer
// receiver, and must not depend on the value's fields.
//
// The type is the one Quark asserts against, not a copy of it, so a model can
// check that it satisfies the convention at compile time:
//
//	var _ quark.TableNamer = User{}
type TableNamer = schema.TableNamer

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

// columnFromDBTag returns the column-name portion of a db tag, stripping
// sizing options like ",size=512". Used wherever code reads the raw db tag
// off a reflect.StructField and needs a clean identifier for the SQL guard.
func columnFromDBTag(tag string) string {
	return schema.ColumnFromDBTag(tag)
}
