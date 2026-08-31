// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

// Quark carries no driver's error types: each ships as its own module under
// drivers/ (ADR-0027). The tests run against every engine — the live suites
// in CI connect to real PostgreSQL, MySQL, SQL Server and Oracle — so the
// TEST binary links them all and registers the same predicates the modules
// register, from the same package, so the two cannot drift.
import "github.com/jcsvwinston/quark/internal/driverclassify"

func init() { driverclassify.RegisterAll() }
