// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package driverclassify

// RegisterAll links every driver this project publishes and registers its
// classifier. It is for the two consumers that legitimately want all of them
// — the `quark` CLI and Quark's own test binaries — and for nothing else: an
// application links the one engine it uses, through its own module.
//
// It is a function rather than an init() so that importing the predicates
// (which the driver modules do, one at a time) does not drag in every driver.

import (
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// RegisterAll registers the classifier for every engine. PostgreSQL is absent
// on purpose: Quark reads its SQLSTATE through the method every PostgreSQL
// driver exposes, which is what lets the same code cover lib/pq, pq and pgx —
// and registering one typed to a single driver would quietly stop covering
// the others.
//
// Calling it twice panics, by design: two registrations for one engine mean
// two answers, and the second is unreachable.
func RegisterAll() {
	quarkdriver.MustRegister("mysql", quarkdriver.Classifier{
		UniqueViolation: MySQLUniqueViolation,
		Deadlock:        MySQLDeadlock,
		TransientConn:   MySQLTransientConn,
	})
	quarkdriver.MustRegister("sqlserver", quarkdriver.Classifier{
		UniqueViolation: MSSQLUniqueViolation,
		Deadlock:        MSSQLDeadlock,
		TransientConn:   MSSQLTransientConn,
	})
	quarkdriver.MustRegister("oracle", quarkdriver.Classifier{
		UniqueViolation: OracleUniqueViolation,
		Deadlock:        OracleDeadlock,
		TransientConn:   OracleTransientConn,
	})
	quarkdriver.MustRegister("sqlite", quarkdriver.Classifier{
		UniqueViolation: SQLiteUniqueViolation,
		Deadlock:        SQLiteDeadlock,
		TransientConn:   SQLiteTransientConn,
	})
}
