// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build !integration
// +build !integration

// Default (non-integration) suite DSN resolvers: read the env var, return
// empty string if absent. The per-engine TestSuite functions skip when
// the resolver returns "". To boot containers automatically, rebuild
// with `go test -tags=integration`.

package enginesuite

import (
	"os"
	"testing"
)

func resolvePostgresDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_POSTGRES_DSN")
}

func resolveMySQLDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_MYSQL_DSN")
}

// resolveMySQLConsecutiveDSN names a MySQL server started with
// innodb_autoinc_lock_mode=1, where CreateBatch reads a multi-row INSERT's
// keys back as one consecutive run (QK-36).
func resolveMySQLConsecutiveDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_MYSQL_AUTOINC1_DSN")
}

func resolveMariaDBDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_MARIADB_DSN")
}

func resolveMSSQLDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_MSSQL_DSN")
}

func resolveOracleDSN(_ *testing.T) string {
	return os.Getenv("QUARK_TEST_ORACLE_DSN")
}
