// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package tenant holds the quarktenant tests that need a real PostgreSQL.
//
// Native row-level security is a PostgreSQL feature, so these tests install
// policies on a live server and read them back — which needs the pgx driver
// and a container to start it in. Both used to sit in the library's go.mod
// because these tests lived beside the package they exercise (ADR-0024). The
// quarktenant tests that need no server stayed there.
package tenant
