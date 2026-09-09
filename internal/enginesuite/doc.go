// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package enginesuite holds Quark's per-engine test suites.
//
// It is a module of its own, and it exists for what it does NOT put in the
// library's go.mod. The suites start real PostgreSQL, MySQL, MariaDB, SQL
// Server and Oracle databases in containers and connect to them, so they
// require the five engine drivers and five testcontainers modules. While they
// shared the library's module, every application that imported Quark carried
// those eleven modules — and the twenty-odd they pull — in its own build list,
// its SBOM and its dependency dashboard, without a single one of them ending
// up in its binary. ADR-0024 measured that gap and moved them here.
//
// Nothing imports this package and nothing publishes it: it is a directory of
// tests with a go.mod, consumed through a local replace the way the benchmark
// and bug-bash harnesses are. Its tests are the same tests, under the same
// names, that the integration matrix has always run — the CI lanes now run
// them from inside this directory.
package enginesuite
