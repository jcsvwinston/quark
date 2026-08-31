// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"fmt"

	"github.com/jcsvwinston/quark"
)

// tableGuard is the shared identifier validator for CLI commands. Every table
// name that reaches SQL through introspection (inspect, validate, model
// --from-table) is interpolated into the query — PRAGMA table_info(%s),
// DESCRIBE <t>, information_schema lookups — so it must pass the same SQLGuard
// the ORM uses on its own identifiers (AQ-09/QC-3). The guard rejects empty
// names, over-long names, reserved keywords, and anything outside
// [A-Za-z_][A-Za-z0-9_]* before a single query is built.
var tableGuard = quark.NewSQLGuard()

// validateTableName returns a friendly error when name is not a safe SQL
// identifier. The wrapped error is quark.ErrInvalidIdentifier, so callers and
// tests can errors.Is it.
func validateTableName(name string) error {
	if err := tableGuard.ValidateIdentifier(name); err != nil {
		return fmt.Errorf("invalid table name %q: %w", name, err)
	}
	return nil
}
