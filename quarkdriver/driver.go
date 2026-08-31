// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package quarkdriver is the contract a database driver module implements to plug
// into Quark. It is a LEAF package: it imports nothing but the standard
// library, so a driver module can implement it without compiling the ORM.
//
// Quark has always let an application bring its own driver — it opens a
// *sql.DB the caller hands it, and never registered one itself. What it DID
// carry was the driver ERROR TYPES, because classifying a failure means
// recognising it: a MySQL deadlock is `*mysql.MySQLError` with number 1213,
// and naming that type imports MySQL. Five drivers' worth of error types
// reached every Quark binary, whichever engine it talked to.
//
// A driver module now supplies that knowledge:
//
//	import _ "github.com/jcsvwinston/quark/drivers/mysql"
//
// The package is named quarkdriver, not driver, because
// database/sql/driver is imported by exactly the code that would import this
// one — naming it "driver" would make every such file alias one of the two.
//
// What makes the omission expensive is that none of these predicates FAIL
// when they do not recognise an error — they answer false. A false from
// Deadlock does not surface as an error; it surfaces as a transaction that
// silently stops being retried, under load, months later.
package quarkdriver

import (
	"fmt"
	"sort"
	"sync"
)

// Classifier is what a driver module knows about its own driver's errors.
//
// All three predicates are required. They are one struct rather than three
// registrations precisely so that supplying two of them is not expressible:
// each answers a question the ORM acts on, and a missing one is a wrong
// answer rather than a missing feature.
//
// Every predicate must match on the CODE the driver reports, through
// errors.As, never on substrings of the message — PostgreSQL, MySQL, Oracle
// and SQL Server all translate their messages when the server runs in another
// language, so a substring check silently returns false on exactly the
// deployments where it matters. And every predicate must return false for an
// error that did not come from its own driver: classifiers are consulted in
// turn, so one that claims a foreign error answers for another engine.
type Classifier struct {
	// UniqueViolation reports a unique or primary-key constraint failure —
	// the signal to answer "that value is already taken" instead of treating
	// a rejected insert as an internal error.
	//
	// It must NOT report foreign-key, not-null or check violations: a caller
	// acting on "unique" points at one field.
	UniqueViolation func(error) bool

	// Deadlock reports that the engine chose this transaction as the victim
	// of a deadlock. Quark retries those, so a driver that does not report
	// its own deadlocks turns a recoverable contention into a failed request.
	Deadlock func(error) bool

	// TransientConn reports a connection that failed in a way a retry or a
	// failover can fix — the server went away, the pooled connection is
	// stale, the host is unreachable. It drives read-replica failover, so a
	// false negative keeps sending reads to a replica that is down.
	//
	// It must NOT report a query or logic error, and must not report the
	// caller's own cancellation: Quark filters context deadlines before
	// consulting classifiers, and a driver that claimed them would fail a
	// healthy replica over on a timeout the caller asked for.
	TransientConn func(error) bool
}

var (
	mu          sync.RWMutex
	classifiers = map[string]Classifier{}
)

// Register records what engine's driver module knows. engine is the dialect
// name Quark uses: "postgres", "mysql", "sqlite", "sqlserver", "oracle".
func Register(engine string, c Classifier) error {
	if engine == "" {
		return fmt.Errorf("quarkdriver: engine name is required")
	}
	for name, fn := range map[string]func(error) bool{
		"UniqueViolation": c.UniqueViolation,
		"Deadlock":        c.Deadlock,
		"TransientConn":   c.TransientConn,
	} {
		if fn == nil {
			return fmt.Errorf("quarkdriver: %s: %s is required — a nil predicate does not fail, it answers false, and Quark acts on that answer", engine, name)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := classifiers[engine]; dup {
		return fmt.Errorf("quarkdriver: %s is already registered", engine)
	}
	classifiers[engine] = c
	return nil
}

// MustRegister is Register for use in an init(), where there is no caller to
// hand an error back to.
func MustRegister(engine string, c Classifier) {
	if err := Register(engine, c); err != nil {
		panic(err)
	}
}

// Classifiers returns the registered classifiers in a stable order, so that
// classification does not depend on map iteration.
func Classifiers() []Classifier {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Classifier, 0, len(classifiers))
	for _, e := range sortedLocked() {
		out = append(out, classifiers[e])
	}
	return out
}

// RegisteredEngines returns the engines with a registered classifier, sorted.
func RegisteredEngines() []string {
	mu.RLock()
	defer mu.RUnlock()
	return sortedLocked()
}

// HasEngine reports whether engine registered a classifier.
func HasEngine(engine string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := classifiers[engine]
	return ok
}

func sortedLocked() []string {
	keys := make([]string, 0, len(classifiers))
	for e := range classifiers {
		keys = append(keys, e)
	}
	sort.Strings(keys)
	return keys
}
