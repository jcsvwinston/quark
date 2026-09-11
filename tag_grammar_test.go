// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A4/S6, NU-50: Quark and Nucleus's pkg/model both read a tag called `db` and
// disagree about its grammar. Nucleus separates directives with semicolons
// and puts the column name in one of them (`db:"column:email;unique"`);
// Quark reads the first comma-separated element as the column NAME.
//
// Before this, a model written the Nucleus way produced a Quark column
// literally called `column:email;unique;not null` — no error, no warning, and
// a table nobody could query. These pin both halves of the fix: refuse what
// cannot be a column name, warn about what can.

type tgNucleusStyle struct {
	ID    uint   `db:"pk"`
	Email string `db:"column:email;unique;not null"`
}

func TestForeignTagGrammarIsRejected(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:taggrammar1?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	err = c.RegisterModel(&tgNucleusStyle{})
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("RegisterModel err = %v, want ErrInvalidTag", err)
	}
	// The message has to teach the fix, not just refuse: someone hitting
	// this has a model written for the other layer and needs to know how it
	// is spelled here.
	for _, want := range []string{"column name", "Nucleus", `db:"email"`, `pk:"true"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message is missing %q:\n%s", want, err)
		}
	}

	// Migrate has to refuse it too, or the DDL goes out anyway.
	if err := c.Migrate(ctx, &tgNucleusStyle{}); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("Migrate err = %v, want ErrInvalidTag", err)
	}
}

type tgDirectiveAsColumn struct {
	ID   int64  `db:"id" pk:"true"`
	Flag string `db:"readonly"`
}

func TestDirectiveShapedColumnNameWarnsButIsAllowed(t *testing.T) {
	// "readonly" is a legal column name, so refusing it would be wrong — a
	// table really can have one. It warns instead.
	meta := GetModelMeta[tgDirectiveAsColumn]()
	if meta.TagError != nil {
		t.Errorf("a legal column name must not be an error: %v", meta.TagError)
	}
	if len(meta.TagWarnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(meta.TagWarnings), meta.TagWarnings)
	}
	w := meta.TagWarnings[0]
	for _, want := range []string{"readonly", "Nucleus", "ignore this"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning is missing %q:\n%s", want, w)
		}
	}
}

type tgQuarkStyle struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email,size=255" quark:"unique,not_null"`
	Notes string `db:"notes"`
}

func TestQuarkGrammarIsUnaffected(t *testing.T) {
	// The check must not fire on the grammar it is defending.
	meta := GetModelMeta[tgQuarkStyle]()
	if meta.TagError != nil {
		t.Errorf("valid Quark tags must not error: %v", meta.TagError)
	}
	if len(meta.TagWarnings) != 0 {
		t.Errorf("valid Quark tags must not warn: %v", meta.TagWarnings)
	}
	if got := meta.Fields[1].Column; got != "email" {
		t.Errorf("column = %q, want email", got)
	}
}

func TestColumnNamesWithStrayCharactersAreRejected(t *testing.T) {
	// The rule is about column names, not about Nucleus: anything that
	// cannot be an identifier is caught whatever grammar produced it.
	// Quoted identifiers make almost anything legal to the engine, which is
	// why this has to be caught before the DDL.
	type strays struct {
		ID  int64  `db:"id" pk:"true"`
		Bad string `db:"user name"`
	}
	meta := GetModelMetaByType(reflect.TypeOf(strays{}))
	if meta.TagError == nil {
		t.Fatal("a column name with a space must be rejected")
	}
	if !strings.Contains(meta.TagError.Error(), `" "`) {
		t.Errorf("the message should name the offending character:\n%s", meta.TagError)
	}
}
