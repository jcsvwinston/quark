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

// BeforeFindHook is executed before a read query is dispatched —
// once per call to [Query.List], [Query.First], [Query.Find], or
// the streaming variants ([Query.Iter] / [Query.Cursor]). The hook
// receives the request context the query was constructed with and
// fires synchronously: returning a non-nil error aborts the query
// before any SQL is emitted, and the error propagates to the
// caller.
//
// Implement on a *T where T is the model type. Quark resolves the
// hook by checking whether `&T{}` satisfies the interface — no
// registration needed.
//
// Typical uses: injecting an additional tenant predicate, enabling
// soft-delete semantics by default, or logging read intent.
type BeforeFindHook interface {
	BeforeFind(ctx context.Context) error
}

// AfterFindHook is executed once the rows of a read query have been
// scanned into the result slice (or single value). The hook
// receives the request context and fires synchronously on the
// result-bearing return path; returning a non-nil error replaces
// the would-be-returned value with that error.
//
// Implement on a *T where T is the model type. The hook fires
// exactly once per query — not once per row.
//
// Typical uses: post-scan enrichment, lazy join hydration not
// covered by [Query.Preload], or read auditing.
type AfterFindHook interface {
	AfterFind(ctx context.Context) error
}
