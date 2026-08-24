// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package quarktest is the minimal test kit for applications built on
// quark: a ready SQLite client with cleanup, one-line schema setup, and the
// transaction-per-test isolation pattern. Zero external services, zero
// boilerplate — the three pieces every quark user was writing by hand.
//
//	func TestOrders(t *testing.T) {
//	    client := quarktest.SQLite(t)
//	    quarktest.Migrate(t, client, &Order{})
//
//	    quarktest.Tx(t, client, func(tx *quark.Tx) {
//	        _ = quark.ForTx[Order](t.Context(), tx).Create(&Order{Status: "new"})
//	        // assertions here see the row…
//	    })
//	    // …and after Tx returns, the rollback has erased it: each test
//	    // starts from the same schema-only state.
//	}
//
// Declared limits, honestly:
//   - SQLite only. The kit covers the fast unit lane; engine-specific
//     behaviour (PostgreSQL RLS, dialect DDL, LISTEN/NOTIFY) needs a real
//     engine — run those against the DSNs the integration suite uses
//     (QUARK_TEST_POSTGRES_DSN etc.), exactly like quark's own matrix.
//   - No fixtures DSL. Seed data with the same CRUD your application uses,
//     inside Tx when you want it erased, outside when you want it shared.
package quarktest

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jcsvwinston/quark"
)

// SQLite returns a quark.Client over a fresh SQLite database file in a
// per-test temporary directory, closed via tb.Cleanup. Extra quark options
// (quark.WithLimits, quark.WithLogger, …) are passed through.
//
// A FILE, deliberately not ":memory:": quark pools connections and every
// pooled connection to ":memory:" opens its OWN empty database — the
// classic trap where the schema exists on one connection and the query
// runs on another. The file gives every connection the same database and
// costs nothing in a tmpfs-backed test dir.
func SQLite(tb testing.TB, opts ...any) *quark.Client {
	tb.Helper()
	dsn := "file:" + filepath.Join(tb.TempDir(), "quarktest.db")
	client, err := quark.New("sqlite", dsn, opts...)
	if err != nil {
		tb.Fatalf("quarktest: open sqlite client: %v", err)
	}
	tb.Cleanup(func() {
		if err := client.Close(); err != nil {
			tb.Errorf("quarktest: close client: %v", err)
		}
	})
	return client
}

// Migrate registers the given models on the client and creates their
// tables — the one-line "schema up" for a test. It fails the test on any
// error, including the tag-linter rejections RegisterModel performs
// (a typoed db: tag dies here, not as a missing column three asserts
// later).
func Migrate(tb testing.TB, client *quark.Client, models ...any) {
	tb.Helper()
	if len(models) == 0 {
		tb.Fatal("quarktest: Migrate needs at least one model")
	}
	if err := client.RegisterModel(models...); err != nil {
		tb.Fatalf("quarktest: register models: %v", err)
	}
	if err := client.MigrateRegistered(context.Background()); err != nil {
		tb.Fatalf("quarktest: migrate: %v", err)
	}
}

// errAlwaysRollback is the sentinel Tx returns from the transaction closure
// so quark rolls the transaction back; it never escapes to the caller.
var errAlwaysRollback = errors.New("quarktest: intentional rollback")

// Tx runs fn inside a transaction that is ALWAYS rolled back — the
// per-test isolation pattern. Writes made through the *quark.Tx (use
// quark.ForTx for the typed query surface) are visible inside fn and gone
// when Tx returns, so tests never leak rows into each other and need no
// per-table cleanup.
//
// The enforced rollback means fn CANNOT observe commit-time behaviour
// (deferred constraint checks, post-commit hooks); test those through
// client.Tx directly.
func Tx(tb testing.TB, client *quark.Client, fn func(tx *quark.Tx)) {
	tb.Helper()
	err := client.Tx(context.Background(), func(tx *quark.Tx) error {
		fn(tx)
		return errAlwaysRollback
	})
	if err != nil && !errors.Is(err, errAlwaysRollback) {
		tb.Fatalf("quarktest: transaction: %v", err)
	}
}
