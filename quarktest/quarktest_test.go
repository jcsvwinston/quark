// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// The kit tested BY USING it — the same three calls the package doc shows a
// user, verified by execution: schema up in one line, writes visible inside
// Tx, erased after, and pooled connections all seeing the same database
// (the ":memory:" trap the kit exists to avoid).
package quarktest_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktest"
)

type kitOrder struct {
	ID     int64  `db:"id" pk:"true"`
	Status string `db:"status"`
}

func TestSQLiteMigrateAndTxRollback(t *testing.T) {
	ctx := context.Background()
	client := quarktest.SQLite(t)
	quarktest.Migrate(t, client, &kitOrder{})

	// Inside Tx: the write is visible through the same tx.
	quarktest.Tx(t, client, func(tx *quark.Tx) {
		if err := quark.ForTx[kitOrder](ctx, tx).Create(&kitOrder{Status: "new"}); err != nil {
			t.Fatalf("create in tx: %v", err)
		}
		n, err := quark.ForTx[kitOrder](ctx, tx).Count()
		if err != nil || n != 1 {
			t.Fatalf("count inside tx: want 1, got %d (%v)", n, err)
		}
	})

	// After Tx: rolled back — the table is schema-only again.
	n, err := quark.For[kitOrder](ctx, client).Count()
	if err != nil {
		t.Fatalf("count after tx: %v", err)
	}
	if n != 0 {
		t.Fatalf("Tx must always roll back: want 0 rows after, got %d", n)
	}

	// Outside Tx: normal writes persist across pooled connections — the
	// file-backed default gives every connection the same database.
	if err := quark.For[kitOrder](ctx, client).Create(&kitOrder{Status: "kept"}); err != nil {
		t.Fatalf("create outside tx: %v", err)
	}
	for range 5 {
		n, err := quark.For[kitOrder](ctx, client).Count()
		if err != nil || n != 1 {
			t.Fatalf("pooled read: want 1, got %d (%v)", n, err)
		}
	}
}

// Migrate fails the test loudly on a typoed tag — the fail-fast linter of
// RegisterModel surfaces here, not as a missing column three asserts later.
func TestMigrateSurfacesTagErrors(t *testing.T) {
	type badTag struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name,lenght=10"` // typo: lenght
	}
	client := quarktest.SQLite(t)

	// Run Migrate through a probe TB so the expected failure does not fail
	// THIS test.
	probe := &probeTB{TB: t}
	func() {
		defer func() { _ = recover() }() // FailNow on a fake TB panics via Goexit-substitute
		quarktest.Migrate(probe, client, &badTag{})
	}()
	if !probe.failed {
		t.Fatal("Migrate must fail on a typoed db: tag")
	}
	if !strings.Contains(probe.lastLog, "lenght") {
		t.Fatalf("failure must name the typoed token, got %q", probe.lastLog)
	}
}

// probeTB records failures instead of aborting the real test.
type probeTB struct {
	testing.TB
	failed  bool
	lastLog string
}

func (p *probeTB) Helper() {}
func (p *probeTB) Fatalf(format string, args ...any) {
	p.failed = true
	p.lastLog = strings.TrimSpace(fmt.Sprintf(format, args...))
	panic("probeTB: FailNow")
}
