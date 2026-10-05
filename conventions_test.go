// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/jcsvwinston/quark/internal/schema"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// The three conventions Quark calls on a caller's type each have an exported
// interface (A11 Q6, control CON-07 of internal/extbench): a model's
// TableName and Validate, a driver error's SQLState. These assertions are
// the point of the interfaces — a misspelt method fails to compile instead of
// being silently skipped — and they compile only while the methods below
// satisfy them.
var (
	_ TableNamer            = conventionModel{}
	_ Validator             = (*conventionModel)(nil)
	_ quarkdriver.SQLStater = conventionStateErr("")
)

type conventionModel struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (conventionModel) TableName() string { return "convention_models" }

var errConventionInvalid = errors.New("convention model: name is required")

func (m *conventionModel) Validate(context.Context) error {
	if m.Name == "" {
		return errConventionInvalid
	}
	return nil
}

type conventionStateErr string

func (e conventionStateErr) Error() string    { return "SQLSTATE " + string(e) }
func (e conventionStateErr) SQLState() string { return string(e) }

// TestConventionInterfacesAreWhatQuarkAsserts pins that each exported
// interface is the one Quark's own code asserts, not a copy beside it: an
// application that satisfies the exported name is honoured, and the two
// cannot drift apart.
func TestConventionInterfacesAreWhatQuarkAsserts(t *testing.T) {
	t.Run("TableNamer", func(t *testing.T) {
		// The model metadata asserts internal/schema.TableNamer; the
		// exported name is an alias of that type.
		if reflect.TypeFor[TableNamer]() != reflect.TypeFor[schema.TableNamer]() {
			t.Fatalf("quark.TableNamer is %v, the metadata asserts %v: an alias was replaced by a copy",
				reflect.TypeFor[TableNamer](), reflect.TypeFor[schema.TableNamer]())
		}
		if got := GetModelMeta[conventionModel]().Table; got != "convention_models" {
			t.Fatalf("table = %q, want the name TableName returns", got)
		}
	})

	t.Run("Validator", func(t *testing.T) {
		c := &Client{}
		err := c.Validate(context.Background(), &conventionModel{})
		if !errors.Is(err, errConventionInvalid) {
			t.Fatalf("Validate = %v, want the model's own error", err)
		}
		if err := c.Validate(context.Background(), &conventionModel{Name: "ok"}); err != nil {
			t.Fatalf("Validate on a valid model = %v", err)
		}
	})

	t.Run("SQLStater", func(t *testing.T) {
		wrapped := fmt.Errorf("insert: %w", conventionStateErr("23505"))
		if !IsUniqueViolation(wrapped) {
			t.Error("a wrapped SQLStater with 23505 is not a unique violation")
		}
		if !IsDeadlock(conventionStateErr("40P01")) {
			t.Error("an SQLStater with 40P01 is not a deadlock")
		}
		if !isTransientConnErr(conventionStateErr("08006")) {
			t.Error("an SQLStater of class 08 is not a transient connection failure")
		}
		if IsUniqueViolation(conventionStateErr("23503")) {
			t.Error("a foreign-key violation (23503) classified as unique")
		}
	})
}
