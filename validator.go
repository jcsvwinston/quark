// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
)

var defaultValidator = validator.New()

// Validate checks a model's fields using standard validation tags (e.g. validate:"required").
// It is automatically called before Create and Update operations if the model is a struct.
func (c *Client) Validate(ctx context.Context, model any) error {
	// Let user override validation logic if they implement a Validatable interface
	if v, ok := model.(interface{ Validate(context.Context) error }); ok {
		if err := v.Validate(ctx); err != nil {
			return err
		}
	}

	// A struct whose type declares no validation rule anywhere the validator
	// could reach passes it by construction, so it is not walked: the walk
	// cost 6.5 % of a 1000-row CreateBatch's CPU on a model with no tags
	// (QK-36). Anything else — a rule somewhere, an interface field that could
	// hold a value with rules, a value that is not a struct — goes to the
	// validator as before.
	if !mayHaveTagRules(model) {
		return nil
	}

	// Use go-playground/validator as fallback
	return defaultValidator.Struct(model)
}

// validateTag is the struct tag go-playground/validator reads by default; the
// package's validator is never configured with another.
const validateTag = "validate"

// tagRulesByType caches, per struct type, whether the validator could find a
// rule in it. A type's answer never changes, so it is computed once.
var tagRulesByType sync.Map // reflect.Type → bool

// mayHaveTagRules reports whether defaultValidator.Struct(model) could return
// anything but nil. It answers false only for a non-nil struct, or pointer to
// one, whose type carries no validate tag on any field, at any depth, and no
// interface-typed field; true for everything else, including every value the
// validator rejects as invalid (nil, a non-struct, time.Time), so that the
// validator still reports those.
//
// The package's validator registers no struct-level or custom-type function,
// so a type without tags has nothing the validator could check.
func mayHaveTagRules(model any) bool {
	t := reflect.TypeOf(model)
	if t == nil {
		return true
	}
	if t.Kind() == reflect.Ptr {
		if reflect.ValueOf(model).IsNil() {
			return true
		}
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || t.ConvertibleTo(timeType) {
		return true
	}
	if v, ok := tagRulesByType.Load(t); ok {
		return v.(bool)
	}
	has := typeMayHaveTagRules(t, map[reflect.Type]bool{})
	tagRulesByType.Store(t, has)
	return has
}

var timeType = reflect.TypeOf(time.Time{})

// typeMayHaveTagRules walks t conservatively: every field of every struct it
// can reach through fields, pointers, slices, arrays and maps — further than
// the validator itself goes without a dive tag, which only makes the answer
// "true" more often. seen breaks cycles: a type already on the path adds
// nothing new.
func typeMayHaveTagRules(t reflect.Type, seen map[reflect.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Interface:
		// The validator descends into the dynamic value, which can be a
		// struct with rules: no static answer.
		return true
	case reflect.Ptr, reflect.Slice, reflect.Array:
		return typeMayHaveTagRules(t.Elem(), seen)
	case reflect.Map:
		return typeMayHaveTagRules(t.Key(), seen) || typeMayHaveTagRules(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Tag.Get(validateTag) != "" {
				return true
			}
			if typeMayHaveTagRules(f.Type, seen) {
				return true
			}
		}
	}
	return false
}
