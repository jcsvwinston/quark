// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testUniqueViolationClassifiable pins that a rejected insert reaches the
// caller as the ENGINE's error, classifiable on every dialect.
//
// The regression it guards is SQL Server specific but the assertion is not:
// there, Create sends the INSERT and SCOPE_IDENTITY() as one batch, and the
// server answers the SELECT with NULL when the INSERT is rejected. Scanning
// that into a plain int64 failed with "converting NULL to int64 is
// unsupported", and database/sql surfaced that conversion error INSTEAD of
// the driver's — so a duplicate key arrived as a scan error naming no
// constraint, no table and no column, and neither the sentinel nor the
// predicate could see it.
//
// Running this on every engine is the point: the defect was invisible on the
// five that do not take that code path, and the promise the predicate makes
// is that the answer does not depend on which engine is underneath.
func testUniqueViolationClassifiable(ctx context.Context, t *testing.T, client *quark.Client) {
	type UVProbe struct {
		ID    int64  `db:"id"    pk:"true"`
		Email string `db:"email" quark:"unique"`
	}

	dropTable(client, "uv_probes")
	if err := client.Migrate(ctx, &UVProbe{}); err != nil {
		t.Fatalf("migrate uv_probes: %v", err)
	}

	first := &UVProbe{Email: "dup@uv.test"}
	if err := quark.For[UVProbe](ctx, client).Create(first); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The happy path must keep back-filling the generated PK: the fix touches
	// exactly the scan that reads it, so a regression there would be silent.
	if first.ID == 0 {
		t.Error("Create did not back-fill the generated PK")
	}

	err := quark.For[UVProbe](ctx, client).Create(&UVProbe{Email: "dup@uv.test"})
	if err == nil {
		t.Fatal("the second Create with the same email must violate the unique constraint")
	}
	if !quark.IsUniqueViolation(err) {
		t.Errorf("IsUniqueViolation did not recognise a real engine violation: %v", err)
	}
	if !errors.Is(err, quark.ErrConstraintViolation) {
		t.Errorf("ErrConstraintViolation does not wrap the unique violation: %v", err)
	}
	// A deadlock and a unique violation must not be confused for one another.
	if quark.IsDeadlock(err) {
		t.Errorf("IsDeadlock classified a unique violation: %v", err)
	}
}
