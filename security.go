// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"github.com/jcsvwinston/quark/internal/guard"
)

// SQLGuard re-exports the internal guard.SQLGuard.
// It provides SQL injection prevention utilities for Quark ORM.
type SQLGuard = guard.SQLGuard

// NewSQLGuard creates a new SQLGuard with default settings.
func NewSQLGuard() *SQLGuard {
	return guard.New()
}

// HasPlaceholders checks if a query string contains parameter placeholders.
var HasPlaceholders = guard.HasPlaceholders
