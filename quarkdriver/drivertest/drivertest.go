// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package drivertest is a conformance kit for Quark driver modules.
//
// Classifiers are consulted in turn, which makes one property sharp enough to
// deserve a suite of its own: a classifier that answers true for an error it
// did not produce answers for ANOTHER engine, and the bug shows up as a wrong
// HTTP status on a deployment its author never runs.
//
// A driver module runs it from its own test:
//
//	drivertest.Verify(t, drivertest.Case{
//	    Engine:     "mysql",
//	    Classifier: quarkdriver.Classifier{...},
//	    Unique:     &gomysql.MySQLError{Number: 1062},
//	    Deadlock:   &gomysql.MySQLError{Number: 1213},
//	    Neither:    []error{&gomysql.MySQLError{Number: 1452}},
//	})
package drivertest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// Case describes one driver's classifier and the errors it must and must not
// claim.
type Case struct {
	// Engine is the dialect name the module registers under.
	Engine string

	// Classifier is what the module passes to quarkdriver.Register.
	Classifier quarkdriver.Classifier

	// Unique is an error the driver produces for a unique or primary-key
	// constraint. Prefer one obtained from the real driver.
	Unique error

	// Deadlock is an error the engine produces when it picks this
	// transaction as the deadlock victim. Leave nil for an engine that
	// cannot deadlock — SQLite is single-writer and never does.
	Deadlock error

	// Neither lists errors from the SAME driver that are neither: another
	// constraint kind, a syntax error. These are what fail when a predicate
	// is widened past what it promises.
	Neither []error
}

// sqlStater mimics the shape every PostgreSQL driver exposes. Quark reads a
// SQLSTATE through this method BEFORE consulting the registry, so a
// classifier that claimed such an error would shadow an engine it knows
// nothing about.
type sqlStater struct{ code string }

func (e *sqlStater) Error() string    { return "SQLSTATE " + e.code }
func (e *sqlStater) SQLState() string { return e.code }

// Verify runs the conformance checks against one driver's classifier.
func Verify(t *testing.T, c Case) {
	t.Helper()

	if c.Engine == "" || c.Unique == nil {
		t.Fatal("drivertest: Engine and Unique are required")
	}
	for name, fn := range map[string]func(error) bool{
		"UniqueViolation": c.Classifier.UniqueViolation,
		"Deadlock":        c.Classifier.Deadlock,
		"TransientConn":   c.Classifier.TransientConn,
	} {
		if fn == nil {
			t.Fatalf("drivertest: %s is nil; a nil predicate does not fail, it answers false", name)
		}
	}

	t.Run("recognises its own unique violation", func(t *testing.T) {
		if !c.Classifier.UniqueViolation(c.Unique) {
			t.Errorf("not recognised: %v", c.Unique)
		}
		// Callers wrap. A predicate that type-asserts instead of using
		// errors.As passes the check above and fails this one.
		if !c.Classifier.UniqueViolation(fmt.Errorf("insert user: %w", c.Unique)) {
			t.Errorf("not recognised through a wrap: %v — use errors.As, not a type assertion", c.Unique)
		}
	})

	t.Run("recognises its own deadlock", func(t *testing.T) {
		if c.Deadlock == nil {
			t.Skip("this engine does not deadlock")
		}
		if !c.Classifier.Deadlock(c.Deadlock) {
			t.Errorf("not recognised: %v — Quark retries deadlocks, so a false here turns recoverable contention into a failed request", c.Deadlock)
		}
		if !c.Classifier.Deadlock(fmt.Errorf("tx: %w", c.Deadlock)) {
			t.Errorf("not recognised through a wrap: %v", c.Deadlock)
		}
	})

	t.Run("keeps unique and deadlock apart", func(t *testing.T) {
		if c.Deadlock == nil {
			t.Skip("this engine does not deadlock")
		}
		if c.Classifier.UniqueViolation(c.Deadlock) {
			t.Error("a deadlock classified as a unique violation: the caller would answer 409 instead of retrying")
		}
		if c.Classifier.Deadlock(c.Unique) {
			t.Error("a unique violation classified as a deadlock: the transaction would be retried forever against a constraint that will reject it every time")
		}
	})

	t.Run("rejects what it does not promise", func(t *testing.T) {
		for _, err := range c.Neither {
			if c.Classifier.UniqueViolation(err) {
				t.Errorf("claimed %v as unique; a caller acting on \"unique\" points at the wrong field", err)
			}
			if c.Classifier.Deadlock(err) {
				t.Errorf("claimed %v as a deadlock; it would be retried and fail identically", err)
			}
		}
	})

	t.Run("claims nothing that is not its own", func(t *testing.T) {
		for _, err := range []error{
			nil,
			errors.New("connection refused"),
			&sqlStater{code: "23505"},
			fmt.Errorf("wrapped: %w", &sqlStater{code: "40P01"}),
		} {
			for name, fn := range map[string]func(error) bool{
				"UniqueViolation": c.Classifier.UniqueViolation,
				"Deadlock":        c.Classifier.Deadlock,
				"TransientConn":   c.Classifier.TransientConn,
			} {
				if fn(err) {
					t.Errorf("%s claimed a foreign error (%v); a predicate must answer only for its own driver's error type", name, err)
				}
			}
		}
	})

	t.Run("is registered under its engine", func(t *testing.T) {
		// A correct classifier is worth nothing if init() never registered
		// it: the failure mode then is silence.
		if !quarkdriver.HasEngine(c.Engine) {
			t.Errorf("engine %q has no registered classifier; registered: %v", c.Engine, quarkdriver.RegisteredEngines())
		}
	})
}
