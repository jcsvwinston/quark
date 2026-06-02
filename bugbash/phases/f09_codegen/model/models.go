// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package model holds the F9 (codegen) sample models. It carries NO build tag
// so the `quark gen` binary and the Go toolchain both see it normally — the
// committed quark_gen.go (emitted by `quark gen ./phases/f09_codegen/model/`)
// registers the typed scanner/binder with Quark's runtime via init().
//
// The bug-bash module cannot import Quark's internal codegen package, so F9
// drives the real `quark gen` binary (the external-consumer flow) rather than
// calling the generator in-process.
//
// To regenerate quark_gen.go after changing a model (run from the bugbash dir):
//
//	go build -o /tmp/quarkgen ../cmd/quark && /tmp/quarkgen gen ./phases/f09_codegen/model/
package model

import (
	"time"

	"github.com/jcsvwinston/quark"
)

// Account has an integer PK, so the generator emits BOTH a typed scanner and a
// typed INSERT binder (contract v3). It mixes the scalar, JSON, Nullable, and
// time-pointer shapes the scanner must round-trip.
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
}

// Doc has a string PK. The generator emits a typed scanner but consciously
// NOT a real INSERT binder (the v3 binder is integer-PK only), so its write
// path falls back to reflection — the F9 "non-integer PK → binder falls back"
// case.
type Doc struct {
	Code  string `db:"code" pk:"true"`
	Title string `db:"title"`
}
