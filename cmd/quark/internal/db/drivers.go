// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package db

// The Quark library links no driver: each ships as its own module so an
// application pays only for the engine it uses (ADR-0023). The CLI is the one
// place where linking every engine is right — it is a tool people install
// once and point at whatever database they have, and asking them to rebuild
// it per engine would be absurd.
//
// It cannot import the drivers/ modules: those import Quark, and the CLI
// lives in the Quark module, so the requirement would be circular. It links
// the drivers directly and registers the same predicates the modules
// register, from the same package, so the two cannot drift.
import "github.com/jcsvwinston/quark/internal/driverclassify"

func init() { driverclassify.RegisterAll() }
