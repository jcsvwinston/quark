// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build !integration
// +build !integration

package commands

import (
	"os"
	"testing"
)

// provisionTestPostgresDSN without -tags=integration: only an explicit env
// DSN enables the provision test; otherwise it skips (same contract as the
// root package's engine suites).
func provisionTestPostgresDSN(t *testing.T) string {
	t.Helper()
	return os.Getenv("QUARK_TEST_POSTGRES_DSN")
}
