// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"errors"
	"fmt"
	"testing"
)

// TestIsDeadlock_Detection pins the per-driver mapping documented in
// F4-7. We fabricate the canonical error type each driver returns on
// deadlock and assert that isDeadlock recognises it. This avoids
// needing a live DB just to exercise the classifier.
func TestIsDeadlock_Detection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a deadlock", nil, false},
		{"plain error is not a deadlock", fmt.Errorf("connection refused"), false},

		// PostgreSQL: SQLSTATE 40P01 = deadlock_detected. Delivered here
		// through the lib/pq-shaped stand-in below rather than through pgx:
		// the classifier reads the SQLSTATE off the `SQLState() string`
		// method, so the two are the same input to it, and the library
		// module requires no driver (ADR-0024). The real driver types are
		// exercised against these same predicates by the engine-suite
		// module, which links all five.
		{"pg 40P01 deadlock", &libpqError{Code: "40P01"}, true},
		{"pg 23505 unique violation is NOT a deadlock", &libpqError{Code: "23505"}, false},

		// Wrapped — errors.As walks the Unwrap chain, so a wrapped
		// driver error still classifies correctly.
		{"wrapped pg deadlock", fmt.Errorf("transaction failed: %w", &libpqError{Code: "40P01"}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeadlock(tc.err); got != tc.want {
				t.Errorf("isDeadlock(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsDeadlock_DoesNotCollideWithUniqueViolation: the two
// classifiers must return mutually-exclusive results — a unique
// violation is not a deadlock and a deadlock is not a unique
// violation. This is the contract every retry caller relies on.
func TestIsDeadlock_DoesNotCollideWithUniqueViolation(t *testing.T) {
	pgUnique := &libpqError{Code: "23505"}
	pgDeadlock := &libpqError{Code: "40P01"}

	if isDeadlock(pgUnique) {
		t.Error("PG 23505 (unique) wrongly classified as deadlock")
	}
	if isUniqueViolation(pgDeadlock) {
		t.Error("PG 40P01 (deadlock) wrongly classified as unique violation")
	}
}

// fakeDeadlock returns an error that isDeadlock recognises — useful
// for exercising the retry loop without a live multi-writer DB.
func fakeDeadlock() error {
	return &libpqError{Code: "40P01"}
}

// TestIsDeadlock_FakeWorks sanity-checks the helper above: the
// retry-path tests in tx_test.go rely on `fakeDeadlock` being
// classified as a deadlock.
func TestIsDeadlock_FakeWorks(t *testing.T) {
	if !isDeadlock(fakeDeadlock()) {
		t.Fatal("fakeDeadlock() must satisfy isDeadlock — retry tests depend on it")
	}
	if !errors.Is(fakeDeadlock(), fakeDeadlock()) {
		// Sanity check only — pg errors aren't comparable via Is by
		// default. Skip if the assertion is meaningless.
		t.Log("note: PostgreSQL error values aren't comparable; tests use isDeadlock directly")
	}
}

// --- lib/pq shape ------------------------------------------------------------

// pqCode mirrors lib/pq's `pqerror.Code` — a named string type holding the
// five-character SQLSTATE. Modelled on the real type so the fixture is not
// friendlier than the driver it stands in for.
type pqCode string

// libpqError reproduces the shape of `*github.com/lib/pq.Error`: a pointer
// type carrying the SQLSTATE in a named-string field and exposing it through
// a `SQLState() string` method on a POINTER receiver. lib/pq is the driver
// quark's own installation guide prescribes for PostgreSQL, and `dialect.go`
// accepts its driver names ("postgres", "pq"), so its errors are a
// first-class input to the classifiers — but it is deliberately NOT a
// dependency of this module, hence the stand-in.
//
// The contract under test is "any error in the chain exposing SQLState()",
// which is exactly what `isPGLockTimeout` already relies on and what both
// lib/pq and pgx/v5 satisfy.
type libpqError struct {
	Code pqCode
	// Msg is the server's own message. It carries the locale: the same
	// rejection is worded in whatever language lc_messages selects, which is
	// why the classifiers must read Code and never this.
	Msg string
}

func (e *libpqError) Error() string {
	if e.Msg != "" {
		return "pq: " + e.Msg
	}
	return "pq: " + string(e.Code)
}
func (e *libpqError) SQLState() string { return string(e.Code) }

// TestClassifiers_LibPQShape pins that the three driver-error classifiers
// recognise PostgreSQL errors delivered by lib/pq, not just by pgx.
//
// Each row below is a feature that silently degrades when the classifier
// misses: unique violations break m2m link idempotency, deadlocks make
// WithDeadlockRetry give up without retrying, and transient connection
// errors stop a read from failing over off a downed replica. None of the
// three reports an error when it misfires — they just stop working.
func TestClassifiers_LibPQShape(t *testing.T) {
	cases := []struct {
		name     string
		sqlstat  string
		classify func(error) bool
		want     bool
	}{
		{"unique violation 23505", "23505", isUniqueViolation, true},
		{"deadlock 40P01", "40P01", isDeadlock, true},
		{"connection exception 08006", "08006", isTransientConnErr, true},
		{"admin shutdown 57P01", "57P01", isTransientConnErr, true},

		// Negatives: the classifiers must stay mutually exclusive on the
		// lib/pq shape exactly as they are on the pgx one.
		{"deadlock is not a unique violation", "40P01", isUniqueViolation, false},
		{"unique violation is not a deadlock", "23505", isDeadlock, false},
		{"unique violation is not transient", "23505", isTransientConnErr, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &libpqError{Code: pqCode(tc.sqlstat)}
			if got := tc.classify(err); got != tc.want {
				t.Errorf("classifier on lib/pq %s = %v, want %v", tc.sqlstat, got, tc.want)
			}
			// Wrapped: errors.As walks the Unwrap chain, so a driver error
			// wrapped by quark's own call sites must classify identically.
			wrapped := fmt.Errorf("exec failed: %w", err)
			if got := tc.classify(wrapped); got != tc.want {
				t.Errorf("classifier on WRAPPED lib/pq %s = %v, want %v", tc.sqlstat, got, tc.want)
			}
		})
	}
}

// TestPGSQLState_CapturesTheLibPQShape pins the positive half of the rule
// that makes the ordering inside the classifiers safe: pgSQLState runs FIRST,
// and it must capture any error exposing `SQLState() string`.
//
// The negative half — that no OTHER driver's error type exposes that method,
// which would make the PostgreSQL branch shadow its classifier — is asserted
// by each driver module against its own error type (TestErrorDoesNotExposeSQLState
// under drivers/), because that is where the driver, and the upgrade that
// could change it, live. The library module requires none of them (ADR-0024).
func TestPGSQLState_CapturesTheLibPQShape(t *testing.T) {
	if state, ok := pgSQLState(&libpqError{Code: "23505"}); !ok || state != "23505" {
		t.Errorf("pgSQLState(lib/pq shape) = (%q, %v), want (\"23505\", true)", state, ok)
	}
	// An error with no SQLSTATE at all must fall through to the registered
	// classifier rather than be answered here.
	if state, ok := pgSQLState(errors.New("connection refused")); ok {
		t.Errorf("pgSQLState captured a plain error (state %q)", state)
	}
}
