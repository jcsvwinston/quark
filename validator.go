// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"

	"github.com/go-playground/validator/v10"
)

var defaultValidator = validator.New()

// Validator is implemented by a model that checks itself before Quark writes
// it:
//
//	Validate(ctx context.Context) error
//
// [Client.Validate] calls it first, with the context of the write, and then
// applies the `validate:"…"` struct tags. [Query.Create], [Query.CreateBatch],
// [Query.Upsert] and [Query.UpsertBatch] call [Client.Validate] on each entity
// before any SQL runs; a non-nil error aborts the write, wrapped as
// "validation failed: %w", so errors.Is and errors.As still find it. Update
// does not validate. Implement it on the pointer receiver, since Quark hands
// it the *T it is about to write.
//
// Quark asserts against this type, so a model can check that it satisfies the
// convention at compile time:
//
//	var _ quark.Validator = (*Member)(nil)
type Validator interface {
	Validate(ctx context.Context) error
}

// Validate runs the model's own [Validator], when it implements one, and then
// checks its fields against the standard validation tags (e.g.
// validate:"required"). Create, CreateBatch, Upsert and UpsertBatch call it on
// each entity before they write; Update does not.
func (c *Client) Validate(ctx context.Context, model any) error {
	// A model that implements Validator runs its own rules first.
	if v, ok := model.(Validator); ok {
		if err := v.Validate(ctx); err != nil {
			return err
		}
	}

	// Use go-playground/validator as fallback
	return defaultValidator.Struct(model)
}
