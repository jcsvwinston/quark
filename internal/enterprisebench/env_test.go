// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// quiet keeps the bench's output to its own verdicts.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// recorder captures the SQL the builder actually emitted.
//
// Several controls are about a CLAUSE — whether an ESCAPE is written, whether
// a schema prefix survives into a transaction, whether a WHERE is injected.
// Asking the database for rows cannot answer those: two different statements
// can return the same rows and only one of them is the one the control is
// about. So the bench reads the statement, not the result.
type recorder struct {
	mu     sync.Mutex
	events []quark.QueryEvent
}

func (r *recorder) ObserveQuery(ev quark.QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

// reset drops what was recorded so far and returns the recorder, so a probe
// can scope its reading to one statement.
func (r *recorder) reset() *recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
	return r
}

// sql returns the statements recorded since the last reset, in order.
func (r *recorder) sql() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.SQL)
	}
	return out
}

// last returns the most recent statement, or "" when nothing was recorded.
func (r *recorder) last() string {
	all := r.sql()
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// env is the shared client every probe measures against, plus the recorder
// that reads what it emitted.
//
// One client for the whole bench, on a shared-cache in-memory SQLite: probes
// that need a database of their own build it themselves and say why in their
// own comment. A probe that quietly replaces the shared one is how a bench
// starts measuring an environment nobody else has.
type env struct {
	tb  testing.TB
	c   *quark.Client
	rec *recorder
	ctx context.Context
}

func newEnv(tb testing.TB) *env {
	tb.Helper()
	rec := &recorder{}
	c, err := quark.New("sqlite", "file:enterprisebench?mode=memory&cache=shared",
		quark.WithLogger(quiet),
		quark.WithQueryObserver(rec),
	)
	if err != nil {
		tb.Fatalf("open the bench client: %v", err)
	}
	tb.Cleanup(func() { _ = c.Close() })
	return &env{tb: tb, c: c, rec: rec, ctx: context.Background()}
}

// fresh opens a client of its own, for a probe that needs a database nothing
// else has touched — a schema it will rewrite, a tenant router with its own
// strategy, a migration it will apply. It returns the client and its recorder.
func (e *env) fresh(t *testing.T, name string, opts ...any) (*quark.Client, *recorder) {
	t.Helper()
	rec := &recorder{}
	opts = append([]any{
		quark.WithLogger(quiet),
		quark.WithQueryObserver(rec),
	}, opts...)
	c, err := quark.New("sqlite", "file:"+name+"?mode=memory&cache=shared", opts...)
	if err != nil {
		t.Fatalf("open a client for %s: %v", name, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, rec
}
