// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
)

// RegisterModel records one or more model values on the Client so
// follow-on calls like [Client.MigrateRegistered] and
// [Client.PlanMigrationRegistered] don't need the caller to re-list
// them. The typical setup pattern:
//
//	client, _ := quark.New(...)
//	client.RegisterModel(&User{}, &Order{}, &Invoice{})
//	if err := client.MigrateRegistered(ctx); err != nil { ... }
//
// Each model goes through the same reflection validation as
// [Client.Migrate] (must be a struct value or pointer to struct;
// nil is rejected) so registration fails fast on a bad model
// rather than at migration time.
//
// Calling RegisterModel multiple times APPENDS to the registry —
// it does NOT deduplicate. If you call `RegisterModel(&User{})`
// twice, [Client.MigrateRegistered] will see User twice. That's
// intentional: Quark doesn't try to be clever about identity of
// reflect.Type-equal values; the caller controls the list.
//
// Safe for concurrent use — the registry is mutex-protected. In
// practice you'll call this once at startup, not from request
// handlers.
//
// The per-Client registry is intentionally additive — it does
// NOT replace the global type-meta cache in `internal/schema`,
// which is correct as global state (deterministic per
// `reflect.Type`). F3-7 only adds per-Client state for "which
// models does this Client manage", not for "what's the cached
// meta of type X".
func (c *Client) RegisterModel(models ...any) error {
	if len(models) == 0 {
		return nil
	}
	// Validate every model up-front before mutating the registry,
	// so a partial failure doesn't leave half the registration
	// applied.
	for _, m := range models {
		if m == nil {
			return fmt.Errorf("RegisterModel: model must not be nil")
		}
		t := reflect.TypeOf(m)
		if t == nil {
			return fmt.Errorf("RegisterModel: model must not be untyped nil")
		}
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return fmt.Errorf("RegisterModel: model must be a struct or *struct, got %s", t.Kind())
		}
		// Fail fast on an invalid quark:"tz=..." tag: surface it now,
		// at registration, rather than on the first query that binds or
		// scans the column. computeModelMeta records the parse failure
		// on the cached meta; we wrap it in the public sentinel here.
		if meta := GetModelMetaByType(t); meta != nil && meta.TZError != nil {
			return fmt.Errorf("%w: %v", ErrInvalidTimezone, meta.TZError)
		}
	}
	c.registeredModelsMu.Lock()
	defer c.registeredModelsMu.Unlock()
	c.registeredModels = append(c.registeredModels, models...)
	return nil
}

// RegisteredModels returns a snapshot of the models registered on
// this Client via [Client.RegisterModel]. The returned slice is a
// COPY — mutations to the returned value don't affect the
// internal registry. Order matches the registration order.
//
// Useful for introspection / debugging and for the user CLI
// wrappers (e.g. `quarkmigrate.Run` could accept a Client with
// pre-registered models instead of taking the variadic models
// argument — though that's not yet wired).
func (c *Client) RegisteredModels() []any {
	c.registeredModelsMu.Lock()
	defer c.registeredModelsMu.Unlock()
	out := make([]any, len(c.registeredModels))
	copy(out, c.registeredModels)
	return out
}

// MigrateRegistered is a convenience for `Migrate(ctx,
// c.RegisteredModels()...)`. Returns nil immediately if no models
// have been registered (no-op rather than error — letting the
// caller initialise the Client in stages without worrying about
// the registration phase).
func (c *Client) MigrateRegistered(ctx context.Context) error {
	models := c.RegisteredModels()
	if len(models) == 0 {
		return nil
	}
	return c.Migrate(ctx, models...)
}

// PlanMigrationRegistered is a convenience for `PlanMigration(ctx,
// c.RegisteredModels()...)`. Returns an empty Plan when no models
// are registered (consistent with the IsEmpty() semantics used
// elsewhere — no models means nothing to plan against).
func (c *Client) PlanMigrationRegistered(ctx context.Context) (Plan, error) {
	models := c.RegisteredModels()
	if len(models) == 0 {
		return Plan{}, nil
	}
	return c.PlanMigration(ctx, models...)
}
