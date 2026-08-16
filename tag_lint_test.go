// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for DX-8 (DX audit 2026-08-16, §4.A3): five deliberate
// struct-tag typos survived RegisterModel and Migrate without a single
// warning, producing DDL with no NOT NULL, no UNIQUE and a missing column —
// and downstream errors ("sql: no rows in result set") that never named the
// model or the words "primary key". The tz= token already fails fast; this
// extends the same contract to the rest of the tag vocabulary.
package quark_test

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// The §4.A3 Widget: every field carries a typo a human plausibly writes.
type lintWidget struct {
	ID    int64   `db:"id" pk:"True"`               // case — must be ACCEPTED (EqualFold)
	Name  string  `db:"name" quark:"notnull"`       // token is not_null
	Price float64 `db:"price,lenght=10"`            // option is size
	Qty   int     `db:"qty,size=abc"`               // size value must be numeric
	Extra string  `column:"extra" db:"extra_field"` // column: is not a quark tag
}

func TestRegisterModelRejectsUnknownTagTokens(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	err = client.RegisterModel(&lintWidget{})
	if err == nil {
		t.Fatal("RegisterModel accepted five tag typos in silence — DDL would omit NOT NULL/UNIQUE/columns without a word")
	}
	for _, fragment := range []string{"notnull", "not_null", "lenght", "size", "column"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error does not mention %q:\n%v", fragment, err)
		}
	}
}

// pk:"True" (any case) must WORK: codegen_registry already accepts it with
// EqualFold while schema.FindPKs demanded the exact literal — the two halves
// of the product disagreed about the same tag.
func TestPKTagIsCaseInsensitive(t *testing.T) {
	type caser struct {
		ID   int64  `db:"id" pk:"True"`
		Name string `db:"name"`
	}
	client, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.RegisterModel(&caser{}); err != nil {
		t.Fatalf("pk:\"True\" must be accepted case-insensitively: %v", err)
	}
}
