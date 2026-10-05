// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertest

import (
	"errors"
	"testing"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// The checks that need no engine: what a dialect's methods promise about
// each other. They run even when the case has no database, so a driver
// whose CI cannot reach its engine still learns these.

var contractChecks = []contractCheck{
	{"Name", contractName},
	{"Placeholder", contractPlaceholders},
	{"Returning", contractReturning},
	{"LockSuffix", contractLockSuffix},
	{"JSONExtract", contractJSONExtract},
	{"ColumnTypeMapper", contractColumnTypeMapper},
	{"ColumnTyper", contractColumnTyper},
	{"IdempotentDDL", contractIdempotentDDL},
}

func contractName(t *testing.T, d quarkdriver.Dialect) {
	if d.Name() == "" {
		t.Error("Dialect.Name() is empty: Quark logs it, errors carry it and quark.RegisterTypeMapper hands it to every mapper")
	}
	if d.Name() != d.Name() {
		t.Error("Dialect.Name() answers differently on two calls")
	}
}

// contractPlaceholders: Quark builds some statements with Placeholder(i) and
// others with Placeholders(n); the two have to agree, and the markers are
// either all the same (positional, like ?) or all different (numbered).
func contractPlaceholders(t *testing.T, d quarkdriver.Dialect) {
	for n := 0; n <= 12; n++ {
		got := d.Placeholders(n)
		if len(got) != n {
			t.Fatalf("Dialect.Placeholders(%d) returned %d markers", n, len(got))
		}
		for i, p := range got {
			if want := d.Placeholder(i + 1); p != want {
				t.Errorf("Dialect.Placeholders(%d)[%d] = %q, but Placeholder(%d) = %q: a statement built with one and bound by the other binds the wrong values", n, i, p, i+1, want)
			}
		}
	}
	seen := map[string]bool{}
	for i := 1; i <= 12; i++ {
		p := d.Placeholder(i)
		if p == "" {
			t.Fatalf("Dialect.Placeholder(%d) is empty", i)
		}
		seen[p] = true
	}
	if len(seen) != 1 && len(seen) != 12 {
		t.Errorf("Dialect.Placeholder(1..12) gives %d distinct markers: a dialect's markers are either all the same (positional) or all different (numbered)", len(seen))
	}
}

// contractReturning: the interface documents that Returning answers "" when
// the engine has no RETURNING; Quark appends what it returns.
func contractReturning(t *testing.T, d quarkdriver.Dialect) {
	if !d.SupportsReturning() {
		if r := d.Returning("id"); r != "" {
			t.Errorf("Dialect.SupportsReturning() is false but Returning(\"id\") = %q: Quark appends what Returning writes", r)
		}
		return
	}
	if d.Returning("id") == "" {
		t.Error("Dialect.SupportsReturning() is true but Returning(\"id\") is empty: Quark would read a generated key from a statement that returns no row")
	}
}

// contractLockSuffix: the zero options must write nothing — every SELECT
// without a lock asks.
func contractLockSuffix(t *testing.T, d quarkdriver.Dialect) {
	hint, suffix, err := d.LockSuffix(quarkdriver.LockOptions{})
	if hint != "" || suffix != "" || err != nil {
		t.Errorf("Dialect.LockSuffix(LockOptions{}) = (%q, %q, %v), want (\"\", \"\", nil): it is asked for every SELECT, locked or not", hint, suffix, err)
	}
}

// hostileJSONPaths are paths a caller can pass to WhereJSON that must never
// reach the SQL: each one would end the literal or the expression it is
// placed in, if a dialect inlined it unvalidated.
var hostileJSONPaths = []string{
	"",
	"a'b",
	"a\"b",
	"a;DROP TABLE x",
	"a b",
	"a)--",
	"a/*b*/",
	"$.a",
	"a\x00b",
}

// contractJSONExtract: the path arrives as the caller wrote it, and the
// dialect validates it (the interface's documentation). A dialect from
// outside cannot reach Quark's internal validator, so this is where a
// missing one shows.
func contractJSONExtract(t *testing.T, d quarkdriver.Dialect) {
	if _, _, err := d.JSONExtract("doc", "user.name"); err != nil {
		t.Fatalf("Dialect.JSONExtract(\"doc\", \"user.name\") refused a dotted identifier path: %v", err)
	}
	for _, p := range hostileJSONPaths {
		if sql, _, err := d.JSONExtract("doc", p); err == nil {
			t.Errorf("Dialect.JSONExtract accepted the path %q (wrote %q): a path must be validated before it reaches the SQL — a dotted chain of identifiers and nothing else", p, sql)
		}
	}
}

// contractColumnTypeMapper: MapColumnType runs on types that may already be
// native, so it must be idempotent.
func contractColumnTypeMapper(t *testing.T, d quarkdriver.Dialect) {
	m, ok := d.(quarkdriver.ColumnTypeMapper)
	if !ok {
		t.Skip("the dialect does not implement quarkdriver.ColumnTypeMapper: a hand-built plan's column types reach the DDL unchanged")
	}
	inputs := []string{"TEXT", "VARCHAR(255)", "BIGINT", "INTEGER", "BOOLEAN", "TIMESTAMP", "DECIMAL(10,2)", "BLOB"}
	if ct, ok := d.(quarkdriver.ColumnTyper); ok {
		for k := quarkdriver.KindOther; k <= quarkdriver.KindRange; k++ {
			if s := ct.ColumnType(quarkdriver.ColumnSpec{Kind: k, Elem: quarkdriver.KindInt64}); s != "" {
				inputs = append(inputs, s)
			}
		}
	}
	for _, in := range inputs {
		once := m.MapColumnType(in)
		if once == "" {
			t.Errorf("ColumnTypeMapper.MapColumnType(%q) is empty", in)
			continue
		}
		if twice := m.MapColumnType(once); twice != once {
			t.Errorf("ColumnTypeMapper.MapColumnType is not idempotent: %q → %q → %q; a type that is already the engine's own must pass through", in, once, twice)
		}
	}
}

// contractColumnTyper: the two boolean literals a DEFAULT clause writes must
// differ, or a default of false is a default of true.
func contractColumnTyper(t *testing.T, d quarkdriver.Dialect) {
	ct, ok := d.(quarkdriver.ColumnTyper)
	if !ok {
		t.Skip("the dialect does not implement quarkdriver.ColumnTyper: Quark writes its portable types (the engine half of the kit checks the engine accepts them)")
	}
	yes, no := ct.BoolLiteral(true), ct.BoolLiteral(false)
	if (yes == "") != (no == "") {
		t.Errorf("ColumnTyper.BoolLiteral answers %q for true and %q for false: answer both or neither", yes, no)
	}
	if yes != "" && yes == no {
		t.Errorf("ColumnTyper.BoolLiteral writes %q for both true and false", yes)
	}
}

// contractIdempotentDDL: Quark counts an error IsAlreadyExists recognises as
// success, so recognising nil, or an error that is not the engine's, turns a
// failure into silence.
func contractIdempotentDDL(t *testing.T, d quarkdriver.Dialect) {
	id, ok := d.(quarkdriver.IdempotentDDL)
	if !ok {
		t.Skip("the dialect does not implement quarkdriver.IdempotentDDL: Quark writes CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS")
	}
	for _, obj := range []quarkdriver.SchemaObject{quarkdriver.ObjectTable, quarkdriver.ObjectIndex, quarkdriver.ObjectConstraint} {
		if id.IsAlreadyExists(obj, nil) {
			t.Errorf("IdempotentDDL.IsAlreadyExists(%d, nil) is true", obj)
		}
		if id.IsAlreadyExists(obj, errors.New("connection refused")) {
			t.Errorf("IdempotentDDL.IsAlreadyExists(%d, \"connection refused\") is true: a failed CREATE would count as done", obj)
		}
	}
}
