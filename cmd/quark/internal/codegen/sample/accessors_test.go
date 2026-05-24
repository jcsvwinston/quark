// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package sample_test

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/codegen/sample"

	_ "modernc.org/sqlite"
)

// TestF6_4_GeneratedAccessors exercises the generated AccountColumns typed
// accessors end-to-end through the real query pipeline: it proves the emitted
// code compiles, that WhereP predicates filter correctly, that the typed and
// string WHERE APIs are interchangeable and mix, and that the string-only
// LIKE operator is available on the string column.
//
// Compile-time column/value safety (the headline F6-4 guarantee) is enforced
// by the compiler, so it cannot be a runtime assertion: AccountColumns.Emial
// (typo) and AccountColumns.Age.Eq("x") (wrong value type) simply do not
// build. This test covers the runtime behaviour the accessors lower to.
//
// SQLite is sufficient here: WhereP introduces no new SQL shape — each
// predicate lowers to the same condition Where / WhereIn / WhereBetween
// produce (see typed_columns_test.go), which the SharedSuite already
// exercises across all six engines. This test only checks the generated
// accessors wire those conditions up correctly.
func TestF6_4_GeneratedAccessors(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)
	if err := c.Migrate(ctx, &sample.Account{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC)
	seed := []sample.Account{
		{Email: "alice@example.com", Age: 30, Active: true, Nickname: quark.Nullable[string]{V: "al", Valid: true}, CreatedAt: now},
		{Email: "bob@example.com", Age: 17, Active: true, CreatedAt: now},
		{Email: "carol@other.org", Age: 45, Active: false, Nickname: quark.Nullable[string]{V: "c", Valid: true}, CreatedAt: now},
	}
	for i := range seed {
		if err := quark.For[sample.Account](ctx, c).Create(&seed[i]); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}

	cols := sample.AccountColumns

	// Eq + Gte via WhereP.
	adults, err := quark.For[sample.Account](ctx, c).
		WhereP(cols.Active.Eq(true), cols.Age.Gte(18)).
		List()
	if err != nil {
		t.Fatalf("WhereP Eq/Gte: %v", err)
	}
	if len(adults) != 1 || adults[0].Email != "alice@example.com" {
		t.Fatalf("active adults = %d rows %v, want [alice]", len(adults), emails(adults))
	}

	// LIKE on the string column (TypedStringColumn).
	examples, err := quark.For[sample.Account](ctx, c).
		WhereP(cols.Email.Like("%@example.com")).
		List()
	if err != nil {
		t.Fatalf("WhereP Like: %v", err)
	}
	if len(examples) != 2 {
		t.Fatalf("Like @example.com = %d, want 2 (%v)", len(examples), emails(examples))
	}

	// IN.
	picked, err := quark.For[sample.Account](ctx, c).
		WhereP(cols.Email.In("alice@example.com", "carol@other.org")).
		List()
	if err != nil || len(picked) != 2 {
		t.Fatalf("WhereP In = %d (err %v), want 2", len(picked), err)
	}

	// Between.
	midAge, err := quark.For[sample.Account](ctx, c).
		WhereP(cols.Age.Between(18, 40)).
		List()
	if err != nil || len(midAge) != 1 || midAge[0].Email != "alice@example.com" {
		t.Fatalf("WhereP Between = %v (err %v), want [alice]", emails(midAge), err)
	}

	// IsNotNull.
	named, err := quark.For[sample.Account](ctx, c).
		WhereP(cols.Nickname.IsNotNull()).
		List()
	if err != nil || len(named) != 2 {
		t.Fatalf("WhereP IsNotNull = %d (err %v), want 2", len(named), err)
	}

	// Mixing typed WhereP with the string Where API on one query.
	mixed, err := quark.For[sample.Account](ctx, c).
		WhereP(cols.Age.Gte(18)).
		Where("active", "=", false).
		List()
	if err != nil || len(mixed) != 1 || mixed[0].Email != "carol@other.org" {
		t.Fatalf("mixed WhereP+Where = %v (err %v), want [carol]", emails(mixed), err)
	}

	// Equivalence: typed and string forms return the same rows.
	typed, _ := quark.For[sample.Account](ctx, c).WhereP(cols.Age.Gte(18)).OrderBy("id", "ASC").List()
	str, _ := quark.For[sample.Account](ctx, c).Where("age", ">=", 18).OrderBy("id", "ASC").List()
	if len(typed) != len(str) {
		t.Fatalf("typed (%d) and string (%d) row counts differ", len(typed), len(str))
	}
	for i := range typed {
		if typed[i].ID != str[i].ID {
			t.Errorf("row %d: typed id %d != string id %d", i, typed[i].ID, str[i].ID)
		}
	}
}

func emails(as []sample.Account) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Email
	}
	return out
}
