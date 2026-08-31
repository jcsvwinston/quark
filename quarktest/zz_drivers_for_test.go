// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktest

// Quark carries no driver's error types (ADR-0027). These tests open
// databases, so the test binary links them the way an application would —
// through the shared predicates, so this file and the drivers/ modules cannot
// drift apart.
import "github.com/jcsvwinston/quark/internal/driverclassify"

func init() { driverclassify.RegisterAll() }
