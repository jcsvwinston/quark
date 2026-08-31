// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

type obdUser struct {
	ID  int64 `db:"id" pk:"true"`
	Age int   `db:"age"`
}

// TestOrderByDirectionValidation is the AQ-06 regression: OrderBy used to
// compare the direction against the two exact literals "DESC"/"desc" and
// silently treat EVERYTHING else — "Desc", "descending", hostile garbage —
// as ASC. Now ASC/DESC match case-insensitively, "" keeps meaning ASC, and
// any other value surfaces ErrInvalidQuery at execution, the same contract
// the operator whitelist already enforces.
func TestOrderByDirectionValidation(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", "file:orderbydir?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &obdUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, age := range []int{5, 42} {
		u := obdUser{Age: age}
		if err := quark.For[obdUser](ctx, client).Create(&u); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	t.Run("MixedCaseDescOrdersDescending", func(t *testing.T) {
		// The measured bug: "Desc" fell through to ASC and returned age=5 first.
		got, err := quark.For[obdUser](ctx, client).OrderBy("age", "Desc").Limit(2).List()
		if err != nil {
			t.Fatalf("OrderBy Desc: %v", err)
		}
		if len(got) != 2 || got[0].Age != 42 {
			t.Errorf(`OrderBy("age", "Desc") must order descending; got %+v`, got)
		}
	})

	t.Run("MixedCaseAscAccepted", func(t *testing.T) {
		got, err := quark.For[obdUser](ctx, client).OrderBy("age", "Asc").Limit(2).List()
		if err != nil {
			t.Fatalf("OrderBy Asc: %v", err)
		}
		if len(got) != 2 || got[0].Age != 5 {
			t.Errorf(`OrderBy("age", "Asc") must order ascending; got %+v`, got)
		}
	})

	t.Run("EmptyDirectionKeepsMeaningAsc", func(t *testing.T) {
		got, err := quark.For[obdUser](ctx, client).OrderBy("age", "").Limit(2).List()
		if err != nil {
			t.Fatalf("OrderBy empty: %v", err)
		}
		if len(got) != 2 || got[0].Age != 5 {
			t.Errorf(`OrderBy("age", "") must keep the ASC default; got %+v`, got)
		}
	})

	t.Run("UnknownDirectionRejected", func(t *testing.T) {
		for _, dir := range []string{"descending", "ASCC", "ASC; DROP TABLE obd_users", "down"} {
			_, err := quark.For[obdUser](ctx, client).OrderBy("age", dir).List()
			if !errors.Is(err, quark.ErrInvalidQuery) {
				t.Errorf("OrderBy(%q) must surface ErrInvalidQuery, got %v", dir, err)
			}
		}
	})
}
