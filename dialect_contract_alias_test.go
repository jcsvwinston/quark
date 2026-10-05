// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// TestDialectContractAliases pins ADR-0026: every name of the dialect
// contract in package quark is the SAME type as the one quarkdriver declares
// — not a copy with the same shape — so a dialect, a schema or a lock written
// against either set of names is handed to the other without a conversion.
func TestDialectContractAliases(t *testing.T) {
	same := []struct {
		name          string
		quark, inLeaf reflect.Type
	}{
		{"Dialect", reflect.TypeFor[quark.Dialect](), reflect.TypeFor[quarkdriver.Dialect]()},
		{"LockMode", reflect.TypeFor[quark.LockMode](), reflect.TypeFor[quarkdriver.LockMode]()},
		{"LockOptions", reflect.TypeFor[quark.LockOptions](), reflect.TypeFor[quarkdriver.LockOptions]()},
		{"SavepointDialect", reflect.TypeFor[quark.SavepointDialect](), reflect.TypeFor[quarkdriver.SavepointDialect]()},
		{"ColumnTypeMapper", reflect.TypeFor[quark.ColumnTypeMapper](), reflect.TypeFor[quarkdriver.ColumnTypeMapper]()},
		{"MigrationLocker", reflect.TypeFor[quark.MigrationLocker](), reflect.TypeFor[quarkdriver.MigrationLocker]()},
		{"MigrationLock", reflect.TypeFor[quark.MigrationLock](), reflect.TypeFor[quarkdriver.MigrationLock]()},
		{"DBConnector", reflect.TypeFor[quark.DBConnector](), reflect.TypeFor[quarkdriver.DBConnector]()},
		{"DBConn", reflect.TypeFor[quark.DBConn](), reflect.TypeFor[quarkdriver.DBConn]()},
		{"Result", reflect.TypeFor[quark.Result](), reflect.TypeFor[quarkdriver.Result]()},
		{"Row", reflect.TypeFor[quark.Row](), reflect.TypeFor[quarkdriver.Row]()},
		{"SchemaIntrospector", reflect.TypeFor[quark.SchemaIntrospector](), reflect.TypeFor[quarkdriver.SchemaIntrospector]()},
		{"Executor", reflect.TypeFor[quark.Executor](), reflect.TypeFor[quarkdriver.Executor]()},
		{"Schema", reflect.TypeFor[quark.Schema](), reflect.TypeFor[quarkdriver.Schema]()},
		{"Table", reflect.TypeFor[quark.Table](), reflect.TypeFor[quarkdriver.Table]()},
		{"Column", reflect.TypeFor[quark.Column](), reflect.TypeFor[quarkdriver.Column]()},
		{"Index", reflect.TypeFor[quark.Index](), reflect.TypeFor[quarkdriver.Index]()},
		{"ForeignKey", reflect.TypeFor[quark.ForeignKey](), reflect.TypeFor[quarkdriver.ForeignKey]()},
		{"Check", reflect.TypeFor[quark.Check](), reflect.TypeFor[quarkdriver.Check]()},
	}
	for _, s := range same {
		if s.quark != s.inLeaf {
			t.Errorf("quark.%s is %v, not quarkdriver.%s (%v): a copy, not an alias", s.name, s.quark, s.name, s.inLeaf)
		}
		if got := s.inLeaf.PkgPath(); got != "github.com/jcsvwinston/quark/quarkdriver" {
			t.Errorf("quarkdriver.%s is declared in %q", s.name, got)
		}
	}

	if quark.LockNone != quarkdriver.LockNone || quark.LockForUpdate != quarkdriver.LockForUpdate || quark.LockForShare != quarkdriver.LockForShare {
		t.Error("the LockMode constants differ between quark and quarkdriver")
	}

	// One value under two names: errors.Is matches whichever name built it.
	for _, s := range []struct {
		name          string
		quark, inLeaf error
	}{
		{"ErrUnsupportedFeature", quark.ErrUnsupportedFeature, quarkdriver.ErrUnsupportedFeature},
		{"ErrLockTimeout", quark.ErrLockTimeout, quarkdriver.ErrLockTimeout},
	} {
		fromDriver := fmt.Errorf("%w: from a driver module", s.inLeaf)
		fromApp := fmt.Errorf("%w: from an application", s.quark)
		if s.quark != s.inLeaf || !errors.Is(fromDriver, s.quark) || !errors.Is(fromApp, s.inLeaf) {
			t.Errorf("%s is not one value under both names", s.name)
		}
	}
}

// TestDialectRegistryOneUnderTwoNames: quark.RegisterDialect and
// quarkdriver.RegisterDialect write one registry, and quark.DetectDialect,
// quark.DetectDialectByName and quarkdriver.LookupDialect read it, with the
// semantics the registry had in package quark: a second registration under a
// name replaces the first, and a registered name wins over a built-in one.
func TestDialectRegistryOneUnderTwoNames(t *testing.T) {
	quarkdriver.RegisterDialect("q3_from_driver", quark.MySQL())
	if d, err := quark.DetectDialect("q3_from_driver"); err != nil || d.Name() != "mysql" {
		t.Fatalf("quark.DetectDialect does not see a quarkdriver registration: %v, %v", d, err)
	}
	if d, err := quark.DetectDialectByName("q3_from_driver"); err != nil || d.Name() != "mysql" {
		t.Fatalf("quark.DetectDialectByName does not see a quarkdriver registration: %v, %v", d, err)
	}

	quark.RegisterDialect("q3_from_app", quark.Oracle())
	if d, ok := quarkdriver.LookupDialect("q3_from_app"); !ok || d.Name() != "oracle" {
		t.Fatalf("quarkdriver.LookupDialect does not see a quark registration: %v, %v", d, ok)
	}

	quark.RegisterDialect("q3_from_driver", quark.MSSQL())
	if d, _ := quarkdriver.LookupDialect("q3_from_driver"); d == nil || d.Name() != "mssql" {
		t.Fatalf("a second registration did not replace the first: %v", d)
	}

	if _, ok := quarkdriver.LookupDialect("pgx"); ok {
		t.Fatal("LookupDialect answered a built-in name it was never given")
	}
	if d, err := quark.DetectDialect("pgx"); err != nil || d.Name() != "postgres" {
		t.Fatalf("a built-in name is no longer resolved: %v, %v", d, err)
	}
	if _, err := quark.DetectDialect("q3_never_registered"); !errors.Is(err, quark.ErrDialectNotSupported) {
		t.Fatalf("an unknown name: %v", err)
	}
}
