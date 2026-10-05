// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
)

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
