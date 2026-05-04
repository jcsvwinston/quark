// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"

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

	// Use go-playground/validator as fallback
	return defaultValidator.Struct(model)
}
