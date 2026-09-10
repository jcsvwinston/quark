// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"
	"testing"
)

// TestWrapDBError_UniqueViolationIsLocaleIndependent pins that
// ErrConstraintViolation is reached by the driver's error CODE and not by the
// wording of its message.
//
// The message below is what a PostgreSQL server configured with a Spanish
// lc_messages actually returns for SQLSTATE 23505. It contains none of the
// English substrings wrapDBError matches on, so before the code check was
// added this error fell through unclassified: the same duplicate insert was
// a constraint violation on an English server and an unrecognised error on a
// Spanish one.
func TestWrapDBError_UniqueViolationIsLocaleIndependent(t *testing.T) {
	translated := &libpqError{
		Code: "23505",
		Msg:  "llave duplicada viola restricción de unicidad «users_email_key»",
	}

	got := wrapDBError(translated)
	if !errors.Is(got, ErrConstraintViolation) {
		t.Fatalf("wrapDBError(23505 with translated message) did not yield ErrConstraintViolation: %v", got)
	}
	// The original error must stay reachable for callers that need detail.
	if !errors.Is(got, error(translated)) {
		t.Error("wrapDBError dropped the underlying driver error")
	}
	// And the precise predicate must agree with the coarse sentinel.
	if !IsUniqueViolation(got) {
		t.Error("IsUniqueViolation disagrees with ErrConstraintViolation on the same error")
	}
}
