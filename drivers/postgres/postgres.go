// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package postgres registers the pgx/v5 driver for a Quark application.
//
//	import _ "github.com/jcsvwinston/quark/drivers/postgres"
//
// It registers NO classifier, and that is deliberate rather than an omission.
// Quark reads a PostgreSQL SQLSTATE through the `SQLState() string` method
// that every PostgreSQL driver exposes, so unique violations, deadlocks and
// connection failures are already classified without naming a driver type —
// which is what lets the same code cover lib/pq, pq and pgx alike. Adding a
// classifier typed to one of them would quietly stop covering the others.
package postgres

import _ "github.com/jackc/pgx/v5/stdlib"
