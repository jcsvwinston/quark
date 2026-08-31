// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

// maPost / maLabel use the SHORT tag form rel:"m2m". Before the AQ-14 fix
// the schema parser stored the alias verbatim, so the eager-loading path
// (which accepted both spellings) diverged from Migrate and the recursive
// save (which matched only "many_to_many"): the join table was never
// created and links were never written — a model that preloaded fine in
// tests with hand-seeded data failed silently in production. The parser
// now normalizes the alias once, so every subsystem sees "many_to_many".
type maPost struct {
	ID     int64     `db:"id" pk:"true"`
	Title  string    `db:"title"`
	Labels []maLabel `rel:"m2m" m2m:"ma_post_labels:post_id:label_id"`
}

type maLabel struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func TestM2MAliasBehavesLikeLongForm(t *testing.T) {
	ctx := context.Background()
	client, err := quark.New("sqlite", "file:m2malias?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer client.Close()

	// Migrate must create the join table for the alias too.
	if err := client.Migrate(ctx, &maPost{}, &maLabel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The recursive save must write the labels AND the join rows.
	post := maPost{
		Title:  "hello",
		Labels: []maLabel{{Name: "go"}, {Name: "sql"}},
	}
	if err := quark.For[maPost](ctx, client).Create(&post); err != nil {
		t.Fatalf("create with m2m alias: %v", err)
	}

	// Preload reads back through the join table quark itself wrote —
	// no hand-seeded rows, so this fails if either half diverged.
	got, err := quark.For[maPost](ctx, client).
		Where("id", "=", post.ID).
		Preload("Labels").
		List()
	if err != nil {
		t.Fatalf("preload: %v", err)
	}
	if len(got) != 1 || len(got[0].Labels) != 2 {
		t.Fatalf(`rel:"m2m" diverged from rel:"many_to_many": expected 2 preloaded labels, got %+v`, got)
	}
}
