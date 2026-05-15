// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "context"

// callBeforeFind invokes [BeforeFindHook] on a zero *T if the model
// type implements the interface. Returning an error aborts the
// query before any SQL is built — the caller surfaces the error to
// its own caller unchanged. The hook receives the query context;
// the typical implementation does ambient work (audit, telemetry)
// or rejects the call entirely. F5-4 does NOT pass the *Query[T]
// to the hook so the contract stays consistent with the existing
// BeforeCreate / BeforeUpdate / BeforeDelete shape (only ctx,
// error).
//
// Implemented as a method on *Query[T] rather than BaseQuery so it
// can reach the zero T via Go generics — BaseQuery is type-erased.
func (q *Query[T]) callBeforeFind(ctx context.Context) error {
	var zero T
	if hook, ok := any(&zero).(BeforeFindHook); ok {
		return hook.BeforeFind(ctx)
	}
	return nil
}

// callAfterFind invokes [AfterFindHook] on a zero *T if the model
// type implements the interface. The hook fires exactly once per
// read call, after the rows have been scanned. Returning an error
// replaces the would-be-returned result with that error.
//
// AfterFind does NOT receive the result slice/value — keeping the
// signature minimal mirrors the rest of the hook API (ctx-only).
// Implementations that need access to the scanned data should
// either hang state off ctx or implement enrichment as a Scope
// helper instead of a hook.
func (q *Query[T]) callAfterFind(ctx context.Context) error {
	var zero T
	if hook, ok := any(&zero).(AfterFindHook); ok {
		return hook.AfterFind(ctx)
	}
	return nil
}
