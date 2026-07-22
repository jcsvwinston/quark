// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// TestWithLimitsPartialLiteral is the red/green regression for #262: a
// partial Limits literal — the natural way to raise ONE limit — used to be
// copied verbatim into the client, leaving QueryTimeout at 0. Every builder
// execution path derives context.WithTimeout(ctx, limits.QueryTimeout), so
// the context was born already expired and every query failed instantly
// with a deadline error. After the fix, WithLimits fills zero-valued
// numeric fields from DefaultLimits and the same client works.
func TestWithLimitsPartialLiteral(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:",
		quark.WithLimits(quark.Limits{MaxResults: 500, AllowRawQueries: true}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })

	ctx := context.Background()
	if err := client.Exec(ctx, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL,
		name TEXT NOT NULL,
		active BOOLEAN NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	u := &User{Email: "partial@example.com", Name: "Partial", Active: true}
	if err := quark.For[User](ctx, client).Create(u); err != nil {
		t.Fatalf("Create under partial Limits literal failed: %v", err)
	}

	got, err := quark.For[User](ctx, client).Find(u.ID)
	if err != nil {
		t.Fatalf("Find under partial Limits literal failed: %v", err)
	}
	if got.Email != "partial@example.com" {
		t.Fatalf("Find returned wrong row: %+v", got)
	}

	list, err := quark.For[User](ctx, client).Where("active", "=", true).List()
	if err != nil {
		t.Fatalf("List under partial Limits literal failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(list))
	}
}

// partialLimitsWarnEvent is the structured event New() emits when a partial
// Limits literal leaves SafeMigrations=false.
const partialLimitsWarnEvent = "quark.limits.partial_literal_safe_migrations_off"

// newClientLogs builds a client with the given options plus a captured
// logger, closes it, and returns everything New() logged.
func newClientLogs(t *testing.T, opts ...any) string {
	t.Helper()
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	client, err := quark.New("sqlite", ":memory:", append(opts, quark.WithLogger(logger))...)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	return buf.String()
}

// TestWithLimitsPartialLiteralSafeMigrationsWarn pins the WARN both ways.
// The boolean asymmetry of Limits (#263) is documented but easy to miss: a
// partial literal gets its numeric zeros filled from DefaultLimits, yet
// SafeMigrations silently stays false — destructive Sync allowed — because
// booleans cannot distinguish "unset" from "explicit false". New() must
// emit ONE structured WARN when the partial-literal signal is present (the
// numeric normalization actually filled something) AND SafeMigrations ended
// up false; it must stay silent when the caller passed a full literal
// (deliberate SafeMigrations=false) or kept SafeMigrations=true. Behaviour
// is unchanged either way — the WARN is the only artifact.
func TestWithLimitsPartialLiteralSafeMigrationsWarn(t *testing.T) {
	t.Run("partial literal leaving SafeMigrations=false warns once", func(t *testing.T) {
		// WithLimits deliberately BEFORE WithLogger (appended last by the
		// helper): the WARN is emitted after the options loop, so it must
		// reach the caller's logger regardless of option order.
		out := newClientLogs(t, quark.WithLimits(quark.Limits{MaxResults: 500}))
		if got := strings.Count(out, partialLimitsWarnEvent); got != 1 {
			t.Fatalf("partial literal with SafeMigrations=false: warn emitted %d times, want exactly 1; logs:\n%s", got, out)
		}
		if !strings.Contains(out, "DefaultLimits()") {
			t.Fatalf("warn does not tell the caller how to fix it (start from DefaultLimits()); logs:\n%s", out)
		}
	})

	t.Run("full literal with deliberate SafeMigrations=false stays silent", func(t *testing.T) {
		l := quark.DefaultLimits()
		l.SafeMigrations = false // every numeric field set: no partial-literal signal
		out := newClientLogs(t, quark.WithLimits(l))
		if strings.Contains(out, partialLimitsWarnEvent) {
			t.Fatalf("deliberate full literal flagged as partial; logs:\n%s", out)
		}
	})

	t.Run("partial literal keeping SafeMigrations=true stays silent", func(t *testing.T) {
		l := quark.DefaultLimits()
		l.MaxResults = 0 // zero back one numeric field: normalization fills it
		out := newClientLogs(t, quark.WithLimits(l))
		if strings.Contains(out, partialLimitsWarnEvent) {
			t.Fatalf("SafeMigrations=true must not warn even under a partial literal; logs:\n%s", out)
		}
	})

	t.Run("no WithLimits at all stays silent", func(t *testing.T) {
		out := newClientLogs(t)
		if strings.Contains(out, partialLimitsWarnEvent) {
			t.Fatalf("client without WithLimits warned; logs:\n%s", out)
		}
	})
}
