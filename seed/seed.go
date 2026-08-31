// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package seed is the registration point for database seeders — the seeding
// counterpart to package migrate. A seeder file registers itself from an
// init() so that blank-importing the seeders package into your runner (the
// same way migrations are wired) makes `quark seed run` see it:
//
//	package seeders
//
//	func init() { seed.Register("demo_users", SeedDemoUsers) }
//
//	func SeedDemoUsers(ctx context.Context, client *quark.Client) error { ... }
//
// This mirrors migrate.Register: the standalone `quark` binary cannot import
// your project's seeders package, so seeders reach the CLI through the runner
// that blank-imports them. Registration order is preserved and is the order
// `seed run` executes in.
package seed

import (
	"context"

	"github.com/jcsvwinston/quark"
)

// Func is the signature of a seeder.
type Func func(ctx context.Context, client *quark.Client) error

// registry holds the seeders registered via Register; order preserves
// registration order, which is the order Run promises. A map alone iterates
// randomly — every all-seeders run would use a different order.
var (
	registry = map[string]Func{}
	order    []string
)

// Register records a seeder under name. Registering the same name twice
// replaces the function but keeps its original position.
func Register(name string, fn Func) {
	if _, exists := registry[name]; !exists {
		order = append(order, name)
	}
	registry[name] = fn
}

// Names returns the registered seeder names in registration order.
func Names() []string {
	out := make([]string, len(order))
	copy(out, order)
	return out
}

// Get returns the seeder registered under name, if any.
func Get(name string) (Func, bool) {
	fn, ok := registry[name]
	return fn, ok
}

// Count reports how many seeders are registered in this binary. The CLI uses
// it to refuse a no-op `seed run`: a standalone binary that never imported the
// project's seeders package has an empty registry, and pretending success
// there would be a lie (symmetric to migrate.RegisteredCount).
func Count() int {
	return len(registry)
}

// Reset clears the registry. Intended for use in tests only.
func Reset() {
	registry = map[string]Func{}
	order = nil
}
