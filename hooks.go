// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "context"

// BeforeCreateHook is executed before an entity is created.
type BeforeCreateHook interface {
	BeforeCreate(ctx context.Context) error
}

// AfterCreateHook is executed after an entity is created.
type AfterCreateHook interface {
	AfterCreate(ctx context.Context) error
}

// BeforeUpdateHook is executed before an entity is updated.
type BeforeUpdateHook interface {
	BeforeUpdate(ctx context.Context) error
}

// AfterUpdateHook is executed after an entity is updated.
type AfterUpdateHook interface {
	AfterUpdate(ctx context.Context) error
}

// BeforeDeleteHook is executed before an entity is deleted.
type BeforeDeleteHook interface {
	BeforeDelete(ctx context.Context) error
}

// AfterDeleteHook is executed after an entity is deleted.
type AfterDeleteHook interface {
	AfterDelete(ctx context.Context) error
}
