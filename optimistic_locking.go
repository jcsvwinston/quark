// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"reflect"
)

// versionFieldOf returns the FieldMeta for the optimistic-locking version
// column on meta, or nil if the model doesn't carry quark:"version".
func versionFieldOf(meta *ModelMeta) *FieldMeta {
	if meta == nil || meta.VersionFieldIndex < 0 {
		return nil
	}
	return &meta.Fields[meta.VersionFieldIndex]
}

// readVersion returns the current value of the version field on entityVal
// (a struct value), normalised to int64. Returns 0 when the field is not
// an integer kind — defensive; the schema layer rejects non-integer version
// fields, but the helper survives reflect surprises gracefully.
func readVersion(entityVal reflect.Value, fm *FieldMeta) int64 {
	if fm == nil {
		return 0
	}
	f := entityVal.Field(fm.Index)
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return f.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(f.Uint())
	}
	return 0
}

// bumpVersion increments the version field on entityVal in place. Used after
// a successful UPDATE that included the optimistic-locking predicate so the
// entity reflects the freshly-written value without a re-read.
func bumpVersion(entityVal reflect.Value, fm *FieldMeta) {
	if fm == nil {
		return
	}
	f := entityVal.Field(fm.Index)
	switch f.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if f.CanSet() {
			f.SetInt(f.Int() + 1)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if f.CanSet() {
			f.SetUint(f.Uint() + 1)
		}
	}
}
