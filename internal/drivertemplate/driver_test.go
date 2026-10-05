// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertemplate_test

import (
	"database/sql"
	"go/build"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest"
	"github.com/jcsvwinston/quark/quarkdriver/drivertest/suite"

	"example.com/drivertemplate"
)

// dsn names a private in-memory database. cache=shared lets every connection
// of the pool see the same database, and foreign_keys(1) makes the engine
// enforce the foreign keys the kit declares. A driver for an engine behind a
// network reads its DSN from the environment instead, and skips when it is
// not set.
func dsn(name string) string {
	return "file:" + name + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
}

// open opens a pool through the driver's own database/sql name.
func open(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open(drivertemplate.Name, dsn(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestClassifier runs the classifier half of the kit on errors the engine
// raised: a duplicate key it must recognise, and a NOT NULL failure and a
// syntax error it must not claim.
func TestClassifier(t *testing.T) {
	db := open(t, "classifier")
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes (title) VALUES ('a')`); err != nil {
		t.Fatal(err)
	}
	_, unique := db.Exec(`INSERT INTO notes (title) VALUES ('a')`)
	_, notNull := db.Exec(`INSERT INTO notes (title) VALUES (NULL)`)
	_, syntax := db.Exec(`INSERT INTO`)
	if unique == nil || notNull == nil || syntax == nil {
		t.Fatalf("the engine accepted a statement it must refuse: unique=%v notNull=%v syntax=%v", unique, notNull, syntax)
	}
	drivertest.Verify(t, drivertest.Case{
		Engine:     drivertemplate.Name,
		Classifier: drivertemplate.Classifier,
		Unique:     unique,
		Neither:    []error{notNull, syntax},
	})
}

// TestDialect runs the dialect half of the kit: every method of the dialect
// and every optional interface it implements, through Quark's queries and its
// schema path, judged by what the engine then holds or refuses. DriverName
// also checks that Quark finds the dialect by the driver's name.
func TestDialect(t *testing.T) {
	drivertest.VerifyDialect(t, drivertest.DialectCase{
		Dialect:    drivertemplate.Dialect{},
		DB:         open(t, "dialect"),
		DriverName: drivertemplate.Name,
	})
}

// TestEngineSuite runs the engine suite Quark's own engines run, on a client
// the application's way: quark.New with the driver's name, and no
// WithDialect.
func TestEngineSuite(t *testing.T) {
	client, err := quark.New(drivertemplate.Name, dsn("engine_suite"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	suite.Run(t, client)
}

// TestDriverImportsOnlyQuarkdriver keeps the driver on the contract: its code
// — not its tests, which play the application — imports quarkdriver and no
// other package of Quark. A driver whose dialect wraps one of Quark's
// built-in dialects imports package quark to reach it, and drops this test.
func TestDriverImportsOnlyQuarkdriver(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	const quarkModule = "github.com/jcsvwinston/quark"
	for _, imp := range pkg.Imports {
		if (imp == quarkModule || strings.HasPrefix(imp, quarkModule+"/")) && imp != quarkModule+"/quarkdriver" {
			t.Errorf("the driver imports %s: a driver is written against quarkdriver, and package quark is the application's", imp)
		}
	}
}
