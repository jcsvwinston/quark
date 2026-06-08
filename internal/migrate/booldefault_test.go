package migrate

import (
	"database/sql"
	"reflect"
	"testing"
)

// TestIsBoolColumn covers the unwrapping: a bool default must be normalized for
// bool, *bool and sql.Null[bool] (quark.Nullable[bool]) alike, and never for
// non-bool columns.
func TestIsBoolColumn(t *testing.T) {
	var b bool
	var pb *bool
	cases := []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{"bool", reflect.TypeOf(b), true},
		{"*bool", reflect.TypeOf(pb), true},
		{"Null[bool]", reflect.TypeOf(sql.Null[bool]{}), true},
		{"int", reflect.TypeOf(0), false},
		{"string", reflect.TypeOf(""), false},
		{"Null[int]", reflect.TypeOf(sql.Null[int]{}), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := IsBoolColumn(c.typ); got != c.want {
			t.Errorf("IsBoolColumn(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeBoolDefault(t *testing.T) {
	cases := []struct{ dialect, in, want string }{
		// PostgreSQL: BOOLEAN requires TRUE/FALSE.
		{"postgres", "1", "TRUE"}, {"postgres", "0", "FALSE"},
		{"postgres", "true", "TRUE"}, {"postgres", "FALSE", "FALSE"},
		{"postgresql", "1", "TRUE"},
		// MSSQL BIT / Oracle NUMBER(1): require 1/0.
		{"mssql", "1", "1"}, {"mssql", "true", "1"}, {"mssql", "false", "0"},
		{"oracle", "0", "0"}, {"oracle", "TRUE", "1"},
		// MySQL / MariaDB / SQLite: 1/0 also fine.
		{"mysql", "1", "1"}, {"mariadb", "true", "1"}, {"sqlite", "0", "0"},
		// Not a recognized bool literal → returned verbatim (strings, exprs).
		{"postgres", "'member'", "'member'"},
		{"postgres", "nextval('s')", "nextval('s')"},
		{"mssql", "2", "2"},
		{"sqlite", "CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP"},
	}
	for _, c := range cases {
		if got := NormalizeBoolDefault(c.dialect, c.in); got != c.want {
			t.Errorf("NormalizeBoolDefault(%q, %q) = %q, want %q", c.dialect, c.in, got, c.want)
		}
	}
}
