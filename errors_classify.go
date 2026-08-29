// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Error classification.
//
// A handler that writes to the database needs to tell one failure apart from
// another: a duplicate e-mail is a 409 the caller can fix, a deadlock is worth
// retrying, and everything else is a 500. Until these predicates existed the
// only exported signal was the ErrConstraintViolation sentinel, which lumps
// unique, foreign-key, not-null and check violations together — enough to know
// something was rejected, not enough to say what to do about it.
//
// The alternative callers reached for was importing their driver and matching
// its error type by hand. That works until the application runs on a second
// engine, and it re-derives detail that is easy to get wrong: Oracle wraps its
// error inside a session error, SQL Server's type has a value receiver, and
// the cgo and non-cgo SQLite drivers are different types entirely. These
// predicates carry that knowledge for all six supported engines and both
// PostgreSQL drivers, and they cost the caller no driver import.
//
// Both predicates match on driver error codes through errors.As, never on
// message text, so they are unaffected by the server's locale and by wording
// changes between driver releases. Both walk the Unwrap chain, so an error
// that quark or the caller has wrapped still classifies.

// IsUniqueViolation reports whether err was caused by a unique or primary-key
// constraint. It is the signal to turn a failed insert into "that value is
// already taken" rather than an internal error:
//
//	if err := users.Create(ctx, u); err != nil {
//	    if quark.IsUniqueViolation(err) {
//	        http.Error(w, "email already registered", http.StatusConflict)
//	        return
//	    }
//	    return err
//	}
//
// It does NOT report foreign-key, not-null or check violations; those still
// arrive as ErrConstraintViolation. Deliberately narrow: a caller acting on
// "unique" wants to point at one field, and widening the predicate later
// would silently change what that branch catches.
func IsUniqueViolation(err error) bool { return isUniqueViolation(err) }

// IsDeadlock reports whether err is a deadlock that the database resolved by
// choosing this transaction as the victim. Such a transaction is fully rolled
// back and is safe to re-run: retrying is the correct response, not an error
// to surface.
//
// Client.Tx already retries deadlocks when the client is built with
// WithDeadlockRetry; this predicate is for callers driving transactions
// themselves, or deciding whether a failed unit of work is worth requeuing.
//
// SQLite never reports a deadlock — it is single-writer, and its SQLITE_BUSY
// is lock contention, which retrying in a tight loop makes worse. It is
// intentionally not classified here.
func IsDeadlock(err error) bool { return isDeadlock(err) }
