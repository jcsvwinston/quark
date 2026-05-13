// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// arrayDoc is the canonical fixture for the Array[T] integration test.
// Two Array fields of different element types catch any per-T quirk in
// the JSON roundtrip on engines whose JSON column types are non-trivial
// (Oracle CLOB, MSSQL NVARCHAR(MAX)).
type arrayDoc struct {
	ID     int64                `db:"id" pk:"true"`
	Name   string               `db:"name"`
	Tags   quark.Array[string]  `db:"tags"`
	Scores quark.Array[float64] `db:"scores"`
}

// testArray exercises the Array[T] wrapper through Migrate → Create →
// Find → Update on every dialect that the SharedSuite covers. The same
// JSON-shaped column type backs all engines (`jsonColumnType` in
// internal/migrate); this test pins the round-trip on each one.
//
// Skip on MSSQL: the MSSQL JSON+NVARCHAR(MAX) encoding bug (F0-8
// followup E, bug 8) makes any JSON-shaped column fail Scan on that
// dialect. Array[T] piggybacks on the same column type, so it inherits
// the skip. When the JSON Scan path is fixed for MSSQL, this skip
// disappears for free.
func testArray(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	if baseClient.Dialect().Name() == "mssql" {
		t.Skip("Array[T] inherits the MSSQL JSON NVARCHAR(MAX) scan bug — see F0-8 followup E")
	}

	dropTable(baseClient, "array_docs")
	if err := baseClient.Migrate(ctx, &arrayDoc{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "array_docs")

	t.Run("StringArrayRoundTrip", func(t *testing.T) {
		d := &arrayDoc{
			Name:   "alice",
			Tags:   quark.Array[string]{V: []string{"go", "orm", "quark"}},
			Scores: quark.Array[float64]{V: []float64{9.5, 8.2, 7.7}},
		}
		if err := quark.For[arrayDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := quark.For[arrayDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Tags.Len() != 3 || got.Tags.Slice()[0] != "go" || got.Tags.Slice()[2] != "quark" {
			t.Errorf("Tags round-trip lost data: %+v", got.Tags.Slice())
		}
		if got.Scores.Len() != 3 || got.Scores.Slice()[1] != 8.2 {
			t.Errorf("Scores round-trip lost data: %+v", got.Scores.Slice())
		}
	})

	t.Run("ZeroValueArraysRoundTrip", func(t *testing.T) {
		// A row inserted with the zero-value Array fields should
		// round-trip cleanly — Value() emits `[]`, Scan() resolves
		// that back to a zero-length slice (or nil; both are
		// acceptable since Len()==0).
		d := &arrayDoc{Name: "bob"}
		if err := quark.For[arrayDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[arrayDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Tags.Len() != 0 || got.Scores.Len() != 0 {
			t.Errorf("expected zero-length arrays, got Tags=%v Scores=%v",
				got.Tags.Slice(), got.Scores.Slice())
		}
	})

	t.Run("UpdateReplacesArrayContents", func(t *testing.T) {
		d := &arrayDoc{
			Name: "carol",
			Tags: quark.Array[string]{V: []string{"old"}},
		}
		if err := quark.For[arrayDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Replace the Tags slice entirely. The dirty-tracking pipeline
		// (F1-1) compares Array.V slices via JSON equality through
		// the marshal/unmarshal path; replacing the slice triggers an
		// UPDATE on the column.
		tracked, err := quark.For[arrayDoc](ctx, baseClient).Track().Find(d.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		tracked.Entity.Tags = quark.Array[string]{V: []string{"new1", "new2"}}
		if _, err := tracked.Save(ctx); err != nil {
			t.Fatalf("save: %v", err)
		}

		got, _ := quark.For[arrayDoc](ctx, baseClient).Find(d.ID)
		if got.Tags.Len() != 2 || got.Tags.Slice()[0] != "new1" {
			t.Errorf("Update did not replace Tags contents: %+v", got.Tags.Slice())
		}
	})
}
