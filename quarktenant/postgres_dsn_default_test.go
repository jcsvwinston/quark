// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build !integration
// +build !integration

package quarktenant_test

import (
	"os"
	"testing"
)

// postgresDSN under the default build returns the env-var DSN when
// set, otherwise the empty string — the caller (the PG integration
// test) then skips. To boot a container automatically, rebuild with
// `go test -tags=integration`.
func postgresDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_POSTGRES_DSN")
}
