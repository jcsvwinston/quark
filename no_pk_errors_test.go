// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for DX-9 (DX audit 2026-08-16, §4.A3): a model with no
// primary key — PK column not named "id" and no pk:"true" tag — produced
// `sql: no rows in result set` on Create and `invalid identifier:
// identifier is empty` on Find. Neither message named the model nor the
// words "primary key". And sql.ErrNoRows leaked raw instead of mapping to
// quark.ErrNotFound.
package quark_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// gadget deliberately has no id column and no pk tag.
type gadget struct {
	Code string `db:"code"`
	Name string `db:"name"`
}

func TestFindWithoutPrimaryKeyIsActionable(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Migrate(ctx, &gadget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, err = quark.For[gadget](ctx, client).Find("g1")
	if err == nil {
		t.Fatal("Find on a PK-less model must fail")
	}
	if !strings.Contains(err.Error(), "primary key") || !strings.Contains(err.Error(), "gadget") {
		t.Errorf("Find error must name the model and the missing primary key, got: %v", err)
	}
}

func TestCreateWithoutPrimaryKeyIsActionable(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Migrate(ctx, &gadget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	err = quark.For[gadget](ctx, client).Create(&gadget{Code: "g1", Name: "first"})
	if err == nil {
		return // if Create learns to insert PK-less rows, that's also a valid fix
	}
	if !strings.Contains(err.Error(), "primary key") || !strings.Contains(err.Error(), "gadget") {
		t.Errorf("Create error must name the model and the missing primary key, got: %v", err)
	}
}

// sql.ErrNoRows must surface as quark.ErrNotFound through the error wrapper.
func TestNoRowsMapsToErrNotFound(t *testing.T) {
	type widgetPK struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Migrate(ctx, &widgetPK{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, err = quark.For[widgetPK](ctx, client).Find(999)
	if err == nil {
		t.Fatal("Find of a missing row must fail")
	}
	if !errors.Is(err, quark.ErrNotFound) {
		t.Errorf("missing row must map to quark.ErrNotFound, got: %v", err)
	}
}
