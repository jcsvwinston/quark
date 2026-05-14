// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"database/sql"
	"reflect"
	"testing"
	"time"
)

// TestBindTimeValue pins the wire-conversion contract (ADR-0010): with a
// non-nil location every time-shaped value is normalised to UTC; with a nil
// location, and for every non-time value, the input passes through unchanged.
func TestBindTimeValue(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	// 2026-01-15 10:00:00 in Madrid (winter, UTC+1) == 09:00:00 UTC.
	madridTime := time.Date(2026, 1, 15, 10, 0, 0, 0, madrid)
	wantUTC := madridTime.UTC()

	t.Run("NilLocPassesThrough", func(t *testing.T) {
		got := bindTimeValue(madridTime, nil)
		gt, ok := got.(time.Time)
		if !ok || !gt.Equal(madridTime) || gt.Location() != madrid {
			t.Errorf("nil loc must pass through untouched: got %v (%v)", got, gt.Location())
		}
		// Non-time values always pass through.
		if v := bindTimeValue("hello", madrid); v != "hello" {
			t.Errorf("non-time value mutated: %v", v)
		}
		if v := bindTimeValue(int64(42), madrid); v != int64(42) {
			t.Errorf("non-time value mutated: %v", v)
		}
	})

	t.Run("TimeTimeConvertsToUTC", func(t *testing.T) {
		got, ok := bindTimeValue(madridTime, madrid).(time.Time)
		if !ok {
			t.Fatalf("expected time.Time, got %T", got)
		}
		if !got.Equal(wantUTC) || got.Location() != time.UTC {
			t.Errorf("want %v UTC, got %v (%v)", wantUTC, got, got.Location())
		}
	})

	t.Run("PointerTimeConvertsToUTC", func(t *testing.T) {
		got, ok := bindTimeValue(&madridTime, madrid).(*time.Time)
		if !ok || got == nil {
			t.Fatalf("expected *time.Time, got %T", got)
		}
		if !got.Equal(wantUTC) || got.Location() != time.UTC {
			t.Errorf("want %v UTC, got %v (%v)", wantUTC, *got, got.Location())
		}
		// A nil pointer must survive untouched.
		var nilPtr *time.Time
		if v := bindTimeValue(nilPtr, madrid); v.(*time.Time) != nil {
			t.Errorf("nil *time.Time must stay nil, got %v", v)
		}
	})

	t.Run("NullableTimeConvertsToUTC", func(t *testing.T) {
		valid := sql.Null[time.Time]{V: madridTime, Valid: true}
		got, ok := bindTimeValue(valid, madrid).(sql.Null[time.Time])
		if !ok {
			t.Fatalf("expected sql.Null[time.Time], got %T", got)
		}
		if !got.Valid || !got.V.Equal(wantUTC) || got.V.Location() != time.UTC {
			t.Errorf("valid Nullable: want %v UTC, got %+v", wantUTC, got)
		}
		// An invalid (NULL) Nullable must pass through without touching V.
		null := sql.Null[time.Time]{Valid: false}
		gn := bindTimeValue(null, madrid).(sql.Null[time.Time])
		if gn.Valid {
			t.Errorf("NULL Nullable must stay invalid, got %+v", gn)
		}
	})
}

// TestResolveFieldTZ pins the precedence: column tag → client default → nil.
func TestResolveFieldTZ(t *testing.T) {
	madrid, _ := time.LoadLocation("Europe/Madrid")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")

	cases := []struct {
		name          string
		fm            *FieldMeta
		clientDefault *time.Location
		want          *time.Location
	}{
		{"TagWins", &FieldMeta{TZ: madrid}, tokyo, madrid},
		{"FallsBackToClientDefault", &FieldMeta{}, tokyo, tokyo},
		{"NilFieldMetaUsesClientDefault", nil, tokyo, tokyo},
		{"NoTagNoDefaultIsNil", &FieldMeta{}, nil, nil},
		{"NilEverythingIsNil", nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveFieldTZ(tc.fm, tc.clientDefault); got != tc.want {
				t.Errorf("resolveFieldTZ = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestComputeModelMeta_TZ pins the eager tag parsing in computeModelMeta:
// a valid quark:"tz=..." tag populates FieldMeta.TZ and flips ModelMeta.HasTZ;
// an invalid one records ModelMeta.TZError and leaves the field untagged.
func TestComputeModelMeta_TZ(t *testing.T) {
	t.Run("ValidTagPopulatesTZ", func(t *testing.T) {
		type ValidTZModel struct {
			ID        int64     `db:"id" pk:"true"`
			CreatedAt time.Time `db:"created_at" quark:"tz=Europe/Madrid"`
			PlainTime time.Time `db:"plain_time"`
		}
		meta := GetModelMetaByType(reflect.TypeOf(ValidTZModel{}))
		if meta.TZError != nil {
			t.Fatalf("unexpected TZError: %v", meta.TZError)
		}
		if !meta.HasTZ {
			t.Error("HasTZ should be true when a field carries a valid tz tag")
		}
		created := meta.FieldByCol["created_at"]
		if created == nil || created.TZ == nil {
			t.Fatal("created_at should have a resolved TZ location")
		}
		if created.TZName != "Europe/Madrid" || created.TZ.String() != "Europe/Madrid" {
			t.Errorf("created_at TZ = %q / %v, want Europe/Madrid", created.TZName, created.TZ)
		}
		if plain := meta.FieldByCol["plain_time"]; plain == nil || plain.TZ != nil {
			t.Errorf("plain_time must have no TZ, got %v", plain.TZ)
		}
	})

	t.Run("InvalidTagRecordsTZError", func(t *testing.T) {
		type InvalidTZModel struct {
			ID        int64     `db:"id" pk:"true"`
			CreatedAt time.Time `db:"created_at" quark:"tz=Not/AReal/Zone"`
		}
		meta := GetModelMetaByType(reflect.TypeOf(InvalidTZModel{}))
		if meta.TZError == nil {
			t.Fatal("TZError should be set for an invalid IANA timezone")
		}
		if meta.HasTZ {
			t.Error("HasTZ must stay false when the only tz tag is invalid")
		}
		if created := meta.FieldByCol["created_at"]; created == nil || created.TZ != nil {
			t.Errorf("invalid-tz field must be left untagged, got TZ=%v", created.TZ)
		}
	})

	t.Run("NoTagMeansNoTZState", func(t *testing.T) {
		type PlainModel struct {
			ID   int64     `db:"id" pk:"true"`
			When time.Time `db:"when"`
		}
		meta := GetModelMetaByType(reflect.TypeOf(PlainModel{}))
		if meta.HasTZ || meta.TZError != nil {
			t.Errorf("plain model must have HasTZ=false TZError=nil, got %v / %v", meta.HasTZ, meta.TZError)
		}
	})
}
