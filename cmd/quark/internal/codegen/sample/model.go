// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package sample holds a representative model used by the codegen tests:
// the conformance test loads it via go/packages (AST) and via reflection,
// and the golden test regenerates its quark_gen.go and compares. It is a
// real, importable package so reflection can see the same types the AST
// extractor parses.
package sample

import (
	"time"

	"github.com/jcsvwinston/quark"
)

// Account is a representative model: a primary key, plain scalar columns, a
// pointer column, and a Quark generic type. The field types are chosen so
// reflect.Type.String() and go/types render them identically after
// CanonicalType, exercising the conformance check without hitting the known
// alias edge cases.
type Account struct {
	ID        int64                  `db:"id" pk:"true"`
	Email     string                 `db:"email"`
	Age       int                    `db:"age"`
	Balance   float64                `db:"balance"`
	Active    bool                   `db:"active"`
	Settings  quark.JSON[string]     `db:"settings"`
	Nickname  quark.Nullable[string] `db:"nickname"`
	CreatedAt time.Time              `db:"created_at"`
	UpdatedAt *time.Time             `db:"updated_at"`

	// notPersisted has no db tag and must be ignored by the generator.
	notPersisted string
}
