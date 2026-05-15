// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newClientForSlowLog returns a Client wired to a JSON slog handler that
// writes into buf, with the given threshold (0 disables the feature).
func newClientForSlowLog(buf *bytes.Buffer, threshold time.Duration) *Client {
	return &Client{
		logger:             slog.New(slog.NewJSONHandler(buf, nil)),
		slowQueryThreshold: threshold,
	}
}

// TestSlowQueryLog_DisabledByDefault is the F4-3 headline contract: with
// the default zero threshold, no log line is emitted no matter how slow
// the operation is. The feature is fully opt-in.
func TestSlowQueryLog_DisabledByDefault(t *testing.T) {
	var buf bytes.Buffer
	c := newClientForSlowLog(&buf, 0)
	c.logSlowQueryIfNeeded(QueryEvent{Duration: time.Second, SQL: "SELECT 1"})
	if buf.Len() > 0 {
		t.Errorf("threshold = 0 must disable logging, got: %s", buf.String())
	}
}

// TestSlowQueryLog_NegativeThresholdDisabled treats a negative threshold
// as disabled — same as zero, to keep the boundary check single-comparison.
func TestSlowQueryLog_NegativeThresholdDisabled(t *testing.T) {
	var buf bytes.Buffer
	c := newClientForSlowLog(&buf, -time.Millisecond)
	c.logSlowQueryIfNeeded(QueryEvent{Duration: time.Second, SQL: "SELECT 1"})
	if buf.Len() > 0 {
		t.Errorf("negative threshold must disable logging, got: %s", buf.String())
	}
}

// TestSlowQueryLog_BelowThreshold: configured threshold, but the
// operation finished under it — no log.
func TestSlowQueryLog_BelowThreshold(t *testing.T) {
	var buf bytes.Buffer
	c := newClientForSlowLog(&buf, 100*time.Millisecond)
	c.logSlowQueryIfNeeded(QueryEvent{Duration: 50 * time.Millisecond, SQL: "SELECT 1"})
	if buf.Len() > 0 {
		t.Errorf("duration below threshold must not log, got: %s", buf.String())
	}
}

// TestSlowQueryLog_EqualThreshold: at exactly the threshold the line
// does emit — the comparison is strict-less-than, so duration == threshold
// crosses the bar. This matters because callers reason in round-number
// thresholds ("100ms is slow") and want the equal case to fire.
func TestSlowQueryLog_EqualThreshold(t *testing.T) {
	var buf bytes.Buffer
	c := newClientForSlowLog(&buf, 100*time.Millisecond)
	c.logSlowQueryIfNeeded(QueryEvent{Duration: 100 * time.Millisecond, SQL: "SELECT 1"})
	if buf.Len() == 0 {
		t.Error("duration == threshold must emit (strict-less-than guard)")
	}
}

// TestSlowQueryLog_AboveThreshold: full happy path — verifies every
// structured field lands on the log record.
func TestSlowQueryLog_AboveThreshold(t *testing.T) {
	var buf bytes.Buffer
	c := newClientForSlowLog(&buf, 100*time.Millisecond)
	c.logSlowQueryIfNeeded(QueryEvent{
		Duration:  250 * time.Millisecond,
		Operation: "SELECT",
		Table:     "users",
		Rows:      3,
		SQL:       "SELECT * FROM users WHERE id = ?",
	})

	out := buf.String()
	for _, want := range []string{
		`"msg":"slow query"`,
		`"duration_ms":250`,
		`"threshold_ms":100`,
		`"operation":"SELECT"`,
		`"table":"users"`,
		`"rows":3`,
		`"sql":"SELECT * FROM users WHERE id = ?"`,
		`"level":"WARN"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log line missing %q\ngot: %s", want, out)
		}
	}
}

// TestSlowQueryLog_NoArgs pins the redaction contract: bind arguments
// are NEVER part of the slow-query log line, mirroring F4-2's default
// span redaction. The parameterised SQL is the observable surface.
func TestSlowQueryLog_NoArgs(t *testing.T) {
	var buf bytes.Buffer
	c := newClientForSlowLog(&buf, 10*time.Millisecond)
	c.logSlowQueryIfNeeded(QueryEvent{
		Duration: time.Second,
		SQL:      "UPDATE users SET password = ? WHERE id = ?",
		Args:     []any{"super-secret-password", int64(42)},
	})
	if strings.Contains(buf.String(), "super-secret-password") {
		t.Errorf("slow log must not contain bind args; got: %s", buf.String())
	}
	if strings.Contains(buf.String(), `"args"`) {
		t.Errorf("slow log must not emit an args field; got: %s", buf.String())
	}
}

// TestSlowQueryLog_NilLoggerIsSafe: a Client without a logger doesn't
// panic when the threshold is crossed — defensive guard.
func TestSlowQueryLog_NilLoggerIsSafe(t *testing.T) {
	c := &Client{slowQueryThreshold: 10 * time.Millisecond}
	// Must not panic.
	c.logSlowQueryIfNeeded(QueryEvent{Duration: time.Second, SQL: "SELECT 1"})
}
