// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// tzClientDoc has a plain time.Time field with no tz tag — its timezone
// behaviour is driven entirely by the client default (WithDefaultTZ).
type tzClientDoc struct {
	ID        int64     `db:"id" pk:"true"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

// tzTagDoc carries per-column tz tags: two required time.Time columns in
// different zones plus a Nullable[time.Time] column, exercising the unwrap
// path. The tags must win over any client default.
type tzTagDoc struct {
	ID        int64                     `db:"id" pk:"true"`
	Name      string                    `db:"name"`
	Madrid    time.Time                 `db:"madrid" quark:"tz=Europe/Madrid"`
	Tokyo     time.Time                 `db:"tokyo" quark:"tz=Asia/Tokyo"`
	OptMadrid quark.Nullable[time.Time] `db:"opt_madrid" quark:"tz=Europe/Madrid"`
}

// testTZ exercises the per-column timezone feature (ADR-0010) on every
// dialect the SharedSuite covers. The wire contract is UTC-always: a
// time.Time is stored as the same instant regardless of the column's tz,
// and converted to the configured location in memory on scan.
func testTZ(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation New_York: %v", err)
	}
	madrid, _ := time.LoadLocation("Europe/Madrid")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")

	// A fixed instant, truncated to the second because some engines drop
	// sub-second precision on round-trip (same rationale as the F1-1 work).
	instant := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC).Truncate(time.Second)

	t.Run("ClientDefaultRoundTrip", func(t *testing.T) {
		client, err := baseClient.WithOptions(quark.WithDefaultTZ(newYork))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		dropTable(client, "tz_client_docs")
		if err := client.Migrate(ctx, &tzClientDoc{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		defer dropTable(client, "tz_client_docs")

		d := &tzClientDoc{Name: "alice", CreatedAt: instant.In(madrid)}
		if err := quark.For[tzClientDoc](ctx, client).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[tzClientDoc](ctx, client).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if !got.CreatedAt.Equal(instant) {
			t.Errorf("instant lost: want %v, got %v", instant, got.CreatedAt)
		}
		if got.CreatedAt.Location().String() != "America/New_York" {
			t.Errorf("client default not applied: location = %v, want America/New_York",
				got.CreatedAt.Location())
		}
	})

	t.Run("TagOverrideRoundTrip", func(t *testing.T) {
		// Client default is New_York, but the column tags must win.
		client, err := baseClient.WithOptions(quark.WithDefaultTZ(newYork))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		dropTable(client, "tz_tag_docs")
		if err := client.Migrate(ctx, &tzTagDoc{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		defer dropTable(client, "tz_tag_docs")

		d := &tzTagDoc{Name: "bob", Madrid: instant, Tokyo: instant, OptMadrid: quark.SomeOf(instant)}
		if err := quark.For[tzTagDoc](ctx, client).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[tzTagDoc](ctx, client).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if got.Madrid.Location().String() != "Europe/Madrid" {
			t.Errorf("Madrid column: location = %v, want Europe/Madrid (tag must beat client default)",
				got.Madrid.Location())
		}
		if got.Tokyo.Location().String() != "Asia/Tokyo" {
			t.Errorf("Tokyo column: location = %v, want Asia/Tokyo", got.Tokyo.Location())
		}
		if !got.Madrid.Equal(instant) || !got.Tokyo.Equal(instant) {
			t.Errorf("instant lost: madrid=%v tokyo=%v want %v", got.Madrid, got.Tokyo, instant)
		}
	})

	t.Run("NullableTimeWithTZ", func(t *testing.T) {
		dropTable(baseClient, "tz_tag_docs")
		if err := baseClient.Migrate(ctx, &tzTagDoc{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		defer dropTable(baseClient, "tz_tag_docs")

		withVal := &tzTagDoc{Name: "carol", Madrid: instant, Tokyo: instant, OptMadrid: quark.SomeOf(instant)}
		withNull := &tzTagDoc{Name: "dave", Madrid: instant, Tokyo: instant, OptMadrid: quark.NullOf[time.Time]()}
		for _, d := range []*tzTagDoc{withVal, withNull} {
			if err := quark.For[tzTagDoc](ctx, baseClient).Create(d); err != nil {
				t.Fatalf("create %s: %v", d.Name, err)
			}
		}

		gotVal, err := quark.For[tzTagDoc](ctx, baseClient).Find(withVal.ID)
		if err != nil {
			t.Fatalf("find withVal: %v", err)
		}
		if !gotVal.OptMadrid.Valid {
			t.Fatal("OptMadrid should be Valid")
		}
		if gotVal.OptMadrid.V.Location().String() != "Europe/Madrid" {
			t.Errorf("Nullable column: location = %v, want Europe/Madrid", gotVal.OptMadrid.V.Location())
		}
		if !gotVal.OptMadrid.V.Equal(instant) {
			t.Errorf("Nullable instant lost: want %v, got %v", instant, gotVal.OptMadrid.V)
		}

		gotNull, err := quark.For[tzTagDoc](ctx, baseClient).Find(withNull.ID)
		if err != nil {
			t.Fatalf("find withNull: %v", err)
		}
		if gotNull.OptMadrid.Valid {
			t.Errorf("OptMadrid should be NULL, got %+v", gotNull.OptMadrid)
		}
	})

	t.Run("WireInstantStableAcrossZones", func(t *testing.T) {
		// The same instant written into two columns with different tz tags
		// must read back as the same instant — proof that the wire format
		// is normalised (UTC) and the tag only affects the in-memory view.
		// If the wire were not normalised, Madrid and Tokyo would persist
		// different values.
		dropTable(baseClient, "tz_tag_docs")
		if err := baseClient.Migrate(ctx, &tzTagDoc{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		defer dropTable(baseClient, "tz_tag_docs")

		d := &tzTagDoc{Name: "erin", Madrid: instant, Tokyo: instant, OptMadrid: quark.SomeOf(instant)}
		if err := quark.For[tzTagDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[tzTagDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if !got.Madrid.Equal(got.Tokyo) {
			t.Errorf("wire not stable across zones: madrid=%v tokyo=%v (must be the same instant)",
				got.Madrid.UTC(), got.Tokyo.UTC())
		}
		if !got.Madrid.Equal(instant) {
			t.Errorf("stored instant drifted: want %v, got %v", instant, got.Madrid.UTC())
		}
		// The in-memory locations still differ — same instant, different view.
		if got.Madrid.Location() == got.Tokyo.Location() {
			t.Errorf("expected different in-memory locations, both are %v", got.Madrid.Location())
		}
	})

	t.Run("UpdateFieldsWithTZ", func(t *testing.T) {
		// UpdateFields is a distinct bind call site from buildUpdate —
		// pin that it also honours the column tz tag.
		dropTable(baseClient, "tz_tag_docs")
		if err := baseClient.Migrate(ctx, &tzTagDoc{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		defer dropTable(baseClient, "tz_tag_docs")

		d := &tzTagDoc{Name: "grace", Madrid: instant, Tokyo: instant, OptMadrid: quark.SomeOf(instant)}
		if err := quark.For[tzTagDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}

		newInstant := instant.Add(48 * time.Hour)
		d.Madrid = newInstant.In(tokyo) // deliberately in a different zone
		if _, err := quark.For[tzTagDoc](ctx, baseClient).UpdateFields(d, "madrid"); err != nil {
			t.Fatalf("UpdateFields: %v", err)
		}

		got, err := quark.For[tzTagDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if !got.Madrid.Equal(newInstant) {
			t.Errorf("UpdateFields lost the instant: want %v, got %v", newInstant, got.Madrid.UTC())
		}
		if got.Madrid.Location().String() != "Europe/Madrid" {
			t.Errorf("UpdateFields round-trip location = %v, want Europe/Madrid", got.Madrid.Location())
		}
	})

	t.Run("NoDefaultNoTagIsPassthrough", func(t *testing.T) {
		// baseClient has no WithDefaultTZ and tzClientDoc has no tag — this
		// is the historical v0.6 path. The feature must be fully opt-in:
		// the instant round-trips unchanged and nothing converts it.
		dropTable(baseClient, "tz_client_docs")
		if err := baseClient.Migrate(ctx, &tzClientDoc{}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		defer dropTable(baseClient, "tz_client_docs")

		d := &tzClientDoc{Name: "frank", CreatedAt: instant}
		if err := quark.For[tzClientDoc](ctx, baseClient).Create(d); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := quark.For[tzClientDoc](ctx, baseClient).Find(d.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if !got.CreatedAt.Equal(instant) {
			t.Errorf("passthrough must preserve the instant: want %v, got %v", instant, got.CreatedAt)
		}
	})
}

// TestRegisterModel_InvalidTimezone pins the fail-fast contract: an invalid
// IANA name in a quark:"tz=..." tag breaks RegisterModel with ErrInvalidTimezone
// rather than surfacing on the first query.
func TestRegisterModel_InvalidTimezone(t *testing.T) {
	type badTZModel struct {
		ID   int64     `db:"id" pk:"true"`
		When time.Time `db:"when" quark:"tz=Pluto/Capital"`
	}
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	err = c.RegisterModel(&badTZModel{})
	if !errors.Is(err, quark.ErrInvalidTimezone) {
		t.Fatalf("RegisterModel: want ErrInvalidTimezone, got %v", err)
	}
}

// TestMigrate_InvalidTimezone pins the same fail-fast contract on Migrate:
// no DDL is emitted for a model whose tz tag is invalid.
func TestMigrate_InvalidTimezone(t *testing.T) {
	type badTZMigrateModel struct {
		ID   int64     `db:"id" pk:"true"`
		When time.Time `db:"when" quark:"tz=Nowhere/Land"`
	}
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared", t.Name(), time.Now().UnixNano())
	c, err := quark.New("sqlite", dsn)
	if err != nil {
		t.Fatalf("quark.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	err = c.Migrate(context.Background(), &badTZMigrateModel{})
	if !errors.Is(err, quark.ErrInvalidTimezone) {
		t.Fatalf("Migrate: want ErrInvalidTimezone, got %v", err)
	}
}
