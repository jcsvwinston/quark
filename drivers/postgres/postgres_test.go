// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package postgres_test

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	_ "github.com/jcsvwinston/quark/drivers/postgres"
	"github.com/jcsvwinston/quark/quarkdriver"
)

func TestRegistersTheSQLDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "pgx") {
		t.Errorf("importing this module must register the \"pgx\" driver; registered: %v", sql.Drivers())
	}
}

// This module deliberately registers NO classifier, and that is worth a test
// rather than a comment: if someone "fixes" the apparent omission by adding
// one, it would necessarily be typed to a single driver and would quietly
// stop covering lib/pq and pq, which Quark's dialect layer also accepts.
func TestClassificationNeedsNoRegistration(t *testing.T) {
	if quarkdriver.HasEngine("postgres") {
		t.Error("this module must not register a classifier: Quark reads the SQLSTATE through the SQLState() method, which works for every PostgreSQL driver")
	}
}

// The listener, on the other hand, IS registered here: it needs the pgx
// connection underneath database/sql, so it cannot live in the ORM.
func TestRegistersTheListener(t *testing.T) {
	if _, ok := quarkdriver.LookupListener("postgres"); !ok {
		t.Error("importing this module must register the LISTEN/NOTIFY listener")
	}
}

// The other half of the rule the engine modules assert from their side: this
// driver's error type MUST expose `SQLState() string`, because that method is
// the whole reason this module registers no classifier. If a pgx release
// dropped or renamed it, PostgreSQL would stop classifying entirely and no
// classifier is registered to take over.
func TestErrorExposesSQLState(t *testing.T) {
	var err error = &pgconn.PgError{Code: "23505"}
	s, ok := err.(interface{ SQLState() string })
	if !ok {
		t.Fatal("the pgx error type no longer exposes SQLState(): Quark classifies PostgreSQL through that method and this module registers no classifier")
	}
	if got := s.SQLState(); got != "23505" {
		t.Errorf("SQLState() = %q, want \"23505\"", got)
	}
}
