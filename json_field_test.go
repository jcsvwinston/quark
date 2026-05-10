package quark_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testJSONField covers the Phase-1 F1-2 deliverables for typed JSON columns
// and []byte / BLOB / BYTEA / VARBINARY mapping.
func testJSONField(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type Settings struct {
		Theme  string   `json:"theme"`
		Volume int      `json:"volume"`
		Tags   []string `json:"tags"`
	}

	type RichDoc struct {
		ID       int64                      `db:"id" pk:"true"`
		Name     string                     `db:"name"`
		Settings quark.JSON[Settings]       `db:"settings"`
		Tags     quark.JSON[[]string]       `db:"tags"`
		Counts   quark.JSON[map[string]int] `db:"counts"`
		Blob     []byte                     `db:"blob"`
	}

	dropTable(baseClient, "rich_docs")
	if err := baseClient.Migrate(ctx, &RichDoc{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "rich_docs")

	t.Run("StructValueRoundTrip", func(t *testing.T) {
		d := &RichDoc{
			Name:     "alice",
			Settings: quark.JSON[Settings]{V: Settings{Theme: "dark", Volume: 7, Tags: []string{"a", "b"}}},
			Tags:     quark.JSON[[]string]{V: []string{"x", "y"}},
			Counts:   quark.JSON[map[string]int]{V: map[string]int{"a": 1, "b": 2}},
			Blob:     []byte{0xDE, 0xAD, 0xBE, 0xEF},
		}
		if err := quark.For[RichDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := quark.For[RichDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}

		if got.Settings.V.Theme != "dark" || got.Settings.V.Volume != 7 {
			t.Errorf("Settings round-trip: %+v", got.Settings.V)
		}
		if len(got.Settings.V.Tags) != 2 || got.Settings.V.Tags[0] != "a" {
			t.Errorf("Settings.Tags round-trip: %+v", got.Settings.V.Tags)
		}
		if len(got.Tags.V) != 2 || got.Tags.V[0] != "x" {
			t.Errorf("Tags slice round-trip: %v", got.Tags.V)
		}
		if got.Counts.V["a"] != 1 || got.Counts.V["b"] != 2 {
			t.Errorf("Counts map round-trip: %v", got.Counts.V)
		}
		if !bytes.Equal(got.Blob, []byte{0xDE, 0xAD, 0xBE, 0xEF}) {
			t.Errorf("Blob round-trip: % x", got.Blob)
		}
	})

	t.Run("ZeroValueScansAsZero", func(t *testing.T) {
		// A row inserted with the zero-value JSON field should round-trip
		// to the zero value of T (empty Settings struct, nil slice, nil map).
		d := &RichDoc{Name: "bob"}
		if err := quark.For[RichDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[RichDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Settings.V.Theme != "" || got.Settings.V.Volume != 0 || len(got.Settings.V.Tags) != 0 {
			t.Errorf("expected zero Settings, got %+v", got.Settings.V)
		}
		if len(got.Tags.V) != 0 {
			t.Errorf("expected nil/empty Tags, got %v", got.Tags.V)
		}
		if len(got.Counts.V) != 0 {
			t.Errorf("expected empty Counts, got %v", got.Counts.V)
		}
	})

	t.Run("UpdateReplacesPayload", func(t *testing.T) {
		d := &RichDoc{
			Name:     "carol",
			Settings: quark.JSON[Settings]{V: Settings{Theme: "light", Volume: 1}},
		}
		if err := quark.For[RichDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Mutate the wrapped value and Update via the Tracked path so the
		// dirty-tracking pipeline (F1-1) compares JSON payloads correctly.
		tracked, err := quark.For[RichDoc](ctx, baseClient).Track().Find(d.ID)
		if err != nil {
			t.Fatalf("track find: %v", err)
		}
		tracked.Entity.Settings.V = Settings{Theme: "dark", Volume: 9, Tags: []string{"updated"}}
		if _, err := tracked.Save(ctx); err != nil {
			t.Fatalf("save: %v", err)
		}

		got, _ := quark.For[RichDoc](ctx, baseClient).Find(d.ID)
		if got.Settings.V.Theme != "dark" || got.Settings.V.Volume != 9 {
			t.Errorf("Settings update round-trip: %+v", got.Settings.V)
		}
	})
}
