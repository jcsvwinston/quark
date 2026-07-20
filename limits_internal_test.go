// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"testing"
	"time"
)

// TestLimitsNormalization pins the field-by-field contract of the #262 fix:
// WithLimits fills zero-valued NUMERIC fields from DefaultLimits (zero means
// "not set"), passes negative values through untouched (the explicit escape
// for "no cap" where a consumer honours it), and never touches the boolean
// fields — an explicit false is indistinguishable from the zero value, so
// booleans carry whatever the literal says.
func TestLimitsNormalization(t *testing.T) {
	d := DefaultLimits()

	t.Run("partial literal fills numeric zeros from defaults", func(t *testing.T) {
		var c Client
		WithLimits(Limits{MaxResults: 500})(&c)

		if c.limits.MaxResults != 500 {
			t.Errorf("MaxResults = %d, want 500 (explicit value kept)", c.limits.MaxResults)
		}
		if c.limits.MaxQueryLength != d.MaxQueryLength {
			t.Errorf("MaxQueryLength = %d, want default %d", c.limits.MaxQueryLength, d.MaxQueryLength)
		}
		if c.limits.MaxJoins != d.MaxJoins {
			t.Errorf("MaxJoins = %d, want default %d", c.limits.MaxJoins, d.MaxJoins)
		}
		if c.limits.MaxWhereConditions != d.MaxWhereConditions {
			t.Errorf("MaxWhereConditions = %d, want default %d", c.limits.MaxWhereConditions, d.MaxWhereConditions)
		}
		if c.limits.QueryTimeout != d.QueryTimeout {
			t.Errorf("QueryTimeout = %v, want default %v", c.limits.QueryTimeout, d.QueryTimeout)
		}
	})

	t.Run("negative values pass through untouched", func(t *testing.T) {
		var c Client
		WithLimits(Limits{MaxQueryLength: -1, MaxJoins: -1})(&c)

		if c.limits.MaxQueryLength != -1 {
			t.Errorf("MaxQueryLength = %d, want -1 (negative passthrough)", c.limits.MaxQueryLength)
		}
		if c.limits.MaxJoins != -1 {
			t.Errorf("MaxJoins = %d, want -1 (negative passthrough)", c.limits.MaxJoins)
		}
		if c.limits.QueryTimeout != d.QueryTimeout {
			t.Errorf("QueryTimeout = %v, want default %v", c.limits.QueryTimeout, d.QueryTimeout)
		}
	})

	t.Run("booleans are never normalized", func(t *testing.T) {
		var c Client
		// Partial literal: both booleans stay false even though
		// DefaultLimits().SafeMigrations is true.
		WithLimits(Limits{QueryTimeout: 5 * time.Second})(&c)
		if c.limits.AllowRawQueries {
			t.Error("AllowRawQueries = true, want false (literal value kept)")
		}
		if c.limits.SafeMigrations {
			t.Error("SafeMigrations = true, want false (booleans are NOT filled from defaults)")
		}
		if c.limits.QueryTimeout != 5*time.Second {
			t.Errorf("QueryTimeout = %v, want 5s (explicit value kept)", c.limits.QueryTimeout)
		}

		// Explicit true survives as well.
		WithLimits(Limits{AllowRawQueries: true, SafeMigrations: true})(&c)
		if !c.limits.AllowRawQueries || !c.limits.SafeMigrations {
			t.Errorf("explicit booleans lost: %+v", c.limits)
		}
	})

	t.Run("full DefaultLimits round-trips unchanged", func(t *testing.T) {
		var c Client
		WithLimits(d)(&c)
		if c.limits != d {
			t.Errorf("DefaultLimits round-trip changed: got %+v want %+v", c.limits, d)
		}
	})
}
