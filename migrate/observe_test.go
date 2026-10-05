// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
)

type ledgerObserver struct {
	mu     sync.Mutex
	events []quark.QueryEvent
}

func (o *ledgerObserver) ObserveQuery(ev quark.QueryEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, ev)
}

// kindMW records the kind the chain is told for each statement.
type kindMW struct {
	quark.BaseMiddleware
	mu    sync.Mutex
	kinds map[string]quark.StatementKind
}

func (m *kindMW) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, ex quark.Executor, s string, a []any) (sql.Result, error) {
		m.mu.Lock()
		m.kinds[s] = quark.StatementKindFromContext(ctx)
		m.mu.Unlock()
		return next(ctx, ex, s, a)
	}
}

func (m *kindMW) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, ex quark.Executor, s string, a []any) (*sql.Rows, error) {
		m.mu.Lock()
		m.kinds[s] = quark.StatementKindFromContext(ctx)
		m.mu.Unlock()
		return next(ctx, ex, s, a)
	}
}

// TestMigratorLedgerPassesTheSeam: the migrator's own statements — the
// ledger table's CREATE, the read of what is applied, and the ledger row an
// UpTx migration commits with — pass the client's middleware chain and reach
// its observers as schema work (A11 Q7). What the UpTx sends on its *sql.Tx
// does not: that transaction is the caller's, and the godoc says so.
func TestMigratorLedgerPassesTheSeam(t *testing.T) {
	migrate.Reset()
	t.Cleanup(migrate.Reset)
	obs := &ledgerObserver{}
	mw := &kindMW{kinds: map[string]quark.StatementKind{}}
	client, err := quark.New("sqlite", "file:migrate_ledger_seam?mode=memory&cache=shared",
		quark.WithLimits(quark.Limits{AllowRawQueries: true, SafeMigrations: true}),
		quark.WithMiddleware(mw), quark.WithQueryObserver(obs))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	migrate.Register(&migrate.Migration{
		ID: "0001", Name: "widgets",
		UpTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `CREATE TABLE widgets (id INTEGER PRIMARY KEY)`)
			return err
		},
	})
	if err := migrate.NewMigrator(client, migrate.WithoutLock()).Up(context.Background(), 0); err != nil {
		t.Fatalf("Up: %v", err)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	want := map[string]quark.StatementKind{
		`CREATE TABLE IF NOT EXISTS "quark_migrations"`: quark.StatementDDL,
		"SELECT id FROM quark_migrations":               quark.StatementIntrospection,
		"INSERT INTO quark_migrations":                  quark.StatementDDL,
	}
	for prefix, kind := range want {
		found := false
		for _, ev := range obs.events {
			if strings.HasPrefix(ev.SQL, prefix) {
				found = true
				if ev.Kind != kind {
					t.Errorf("%q reached the observer as %q, want %q", prefix, ev.Kind, kind)
				}
				if got := mw.kinds[ev.SQL]; got != kind {
					t.Errorf("%q passed the middleware as %q, want %q", prefix, got, kind)
				}
			}
		}
		if !found {
			t.Errorf("the migrator's %q reached no observer", prefix)
		}
	}
	for _, ev := range obs.events {
		if strings.HasPrefix(ev.SQL, "CREATE TABLE widgets") {
			t.Errorf("the UpTx's own statement reached the observer: %q — the *sql.Tx is the caller's, and the godoc says it is not observed", ev.SQL)
		}
	}
}
