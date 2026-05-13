package quark_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

// testNullable covers the Phase-1 F1-3 deliverable: Nullable[T] generic
// (alias of database/sql's Null[T]) round-trips through Migrate, Create,
// Find for primitive types, time.Time, and constructors SomeOf / NullOf.
func testNullable(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type NullProfile struct {
		ID    int64                     `db:"id" pk:"true"`
		Name  string                    `db:"name"`
		Bio   quark.Nullable[string]    `db:"bio"`
		Age   quark.Nullable[int64]     `db:"age"`
		Born  quark.Nullable[time.Time] `db:"born"`
		Score quark.Nullable[float64]   `db:"score"`
	}

	dropTable(baseClient, "null_profiles")
	if err := baseClient.Migrate(ctx, &NullProfile{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "null_profiles")

	t.Run("RoundTripValuesAndNulls", func(t *testing.T) {
		p := &NullProfile{
			Name:  "Alice",
			Bio:   quark.SomeOf("Hello, world."),
			Age:   quark.SomeOf(int64(31)),
			Born:  quark.NullOf[time.Time](),
			Score: quark.SomeOf(98.6),
		}
		if err := quark.For[NullProfile](ctx, baseClient).Create(p); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := quark.For[NullProfile](ctx, baseClient).Find(p.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if !got.Bio.Valid || got.Bio.V != "Hello, world." {
			t.Errorf("Bio round-trip: %+v", got.Bio)
		}
		if !got.Age.Valid || got.Age.V != 31 {
			t.Errorf("Age round-trip: %+v", got.Age)
		}
		if got.Born.Valid {
			t.Errorf("Born expected NULL, got Valid=true V=%v", got.Born.V)
		}
		// Postgres maps Go float64 → SQL `real` (single-precision IEEE-754)
		// unless the schema requests `double precision`. The roundtrip of
		// 98.6 through float32 lands at 98.5999984741211. SQLite and
		// MySQL/MariaDB preserve full double precision. Compare with a
		// tolerance large enough to cover the float32 rounding (~1e-5)
		// but small enough to catch a genuine roundtrip bug.
		if !got.Score.Valid || math.Abs(got.Score.V-98.6) > 1e-4 {
			t.Errorf("Score round-trip: %+v (want ≈ 98.6)", got.Score)
		}
	})

	t.Run("ExplicitNullSomeAndNone", func(t *testing.T) {
		// Insert an all-null profile (apart from required Name).
		none := &NullProfile{
			Name:  "Empty",
			Bio:   quark.NullOf[string](),
			Age:   quark.NullOf[int64](),
			Born:  quark.NullOf[time.Time](),
			Score: quark.NullOf[float64](),
		}
		if err := quark.For[NullProfile](ctx, baseClient).Create(none); err != nil {
			t.Fatalf("create empty: %v", err)
		}
		got, err := quark.For[NullProfile](ctx, baseClient).Find(none.ID)
		if err != nil {
			t.Fatalf("find empty: %v", err)
		}
		if got.Bio.Valid || got.Age.Valid || got.Born.Valid || got.Score.Valid {
			t.Errorf("expected every nullable field to round-trip as NULL: %+v", got)
		}
	})

	t.Run("SomeOfPreservesValues", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		p := &NullProfile{
			Name: "Tagged",
			Born: quark.SomeOf(now),
		}
		if err := quark.For[NullProfile](ctx, baseClient).Create(p); err != nil {
			t.Fatalf("create with SomeOf time.Time: %v", err)
		}
		got, err := quark.For[NullProfile](ctx, baseClient).Find(p.ID)
		if err != nil {
			t.Fatalf("find: %v", err)
		}
		if !got.Born.Valid {
			t.Fatal("Born should be Valid after SomeOf")
		}
		// Across drivers the round-tripped time may lose monotonic clock
		// data; compare with .Equal which the Phase-1 F1-1 test work
		// already established as the right comparison for time values.
		if !got.Born.V.Equal(now) {
			t.Errorf("Born: expected %v got %v", now, got.Born.V)
		}
	})
}
