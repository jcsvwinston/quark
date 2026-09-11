// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"strings"
	"testing"
)

// Without a frame, a window with ORDER BY runs from the start of the
// partition to the current row. That default is what makes SUM() OVER a
// running total — and what silently turns a moving average into a running
// one, which is the bug these tests exist to prevent.

func TestWindowRowsFrame(t *testing.T) {
	w := NewWindow().OrderBy(Col("id"), false).Rows(Preceding(2), CurrentRow())
	sql, _, err := Over(Func("AVG", Col("total")), w).ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("Over: %v", err)
	}
	want := `AVG("total") OVER (ORDER BY "id" ROWS BETWEEN 2 PRECEDING AND CURRENT ROW)`
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
}

func TestWindowRangeFrame(t *testing.T) {
	w := NewWindow().PartitionBy(Col("user_id")).OrderBy(Col("placed_at"), false).
		Range(UnboundedPreceding(), CurrentRow())
	sql, _, err := Over(Func("SUM", Col("total")), w).ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("Over: %v", err)
	}
	if !strings.Contains(sql, "RANGE BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW") {
		t.Errorf("sql = %q, want a RANGE frame", sql)
	}
}

func TestWindowFrameBoundsRender(t *testing.T) {
	for _, tc := range []struct {
		bound FrameBound
		want  string
	}{
		{UnboundedPreceding(), "UNBOUNDED PRECEDING"},
		{UnboundedFollowing(), "UNBOUNDED FOLLOWING"},
		{CurrentRow(), "CURRENT ROW"},
		{Preceding(3), "3 PRECEDING"},
		{Following(1), "1 FOLLOWING"},
		// A negative offset is clamped: every engine rejects it, and failing
		// at the engine beats emitting malformed SQL from here.
		{Preceding(-5), "0 PRECEDING"},
	} {
		if string(tc.bound) != tc.want {
			t.Errorf("bound = %q, want %q", string(tc.bound), tc.want)
		}
	}
}

func TestWindowWithoutFrameIsUnchanged(t *testing.T) {
	// The default stays the default: adding frames must not alter what
	// existing windows emit.
	w := NewWindow().OrderBy(Col("id"), false)
	sql, _, err := Over(RowNumber(), w).ToSQL(SQLite(), NewSQLGuard())
	if err != nil {
		t.Fatalf("Over: %v", err)
	}
	if strings.Contains(sql, "ROWS") || strings.Contains(sql, "RANGE") {
		t.Errorf("sql = %q, a window without a frame must emit none", sql)
	}
}

func TestWindowIsStillImmutable(t *testing.T) {
	base := NewWindow().OrderBy(Col("id"), false)
	framed := base.Rows(Preceding(1), CurrentRow())
	baseSQL, _, _ := Over(RowNumber(), base).ToSQL(SQLite(), NewSQLGuard())
	framedSQL, _, _ := Over(RowNumber(), framed).ToSQL(SQLite(), NewSQLGuard())
	if strings.Contains(baseSQL, "ROWS") {
		t.Errorf("Rows() mutated the receiver: %q", baseSQL)
	}
	if !strings.Contains(framedSQL, "ROWS") {
		t.Errorf("framed window lost its frame: %q", framedSQL)
	}
}
