package quark

import "testing"

// TestHasJSONValidCheck pins the MariaDB JSON-column detection (Finding D). The
// auto-generated CHECK for a JSON column is exactly `json_valid(`col`)`; the
// match is by whole-expression equality (modulo backticks/whitespace/case) so a
// hand-written compound CHECK over a genuine LONGTEXT column never false-fires.
func TestHasJSONValidCheck(t *testing.T) {
	tests := []struct {
		name   string
		col    string
		checks []Check
		want   bool
	}{
		{"auto check backticked", "data", []Check{{Name: "data", Expression: "json_valid(`data`)"}}, true},
		{"case-insensitive", "Data", []Check{{Name: "Data", Expression: "JSON_VALID(`Data`)"}}, true},
		{"whitespace tolerated", "data", []Check{{Name: "data", Expression: "json_valid( `data` )"}}, true},
		{"no backticks", "data", []Check{{Name: "data", Expression: "json_valid(data)"}}, true},
		{"different column", "body", []Check{{Name: "data", Expression: "json_valid(`data`)"}}, false},
		{"no checks", "data", nil, false},
		// The false-positive vector that whole-expression equality closes: a
		// genuine LONGTEXT column carrying a compound CHECK that merely mentions
		// json_valid must NOT be detected as a JSON column.
		{"compound check is not a json column", "body", []Check{{Name: "ck", Expression: "json_valid(`body`) or `body` is null"}}, false},
		{"prefix is not a match", "x", []Check{{Name: "ck", Expression: "json_valid(`xlong`)"}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasJSONValidCheck(tc.checks, tc.col); got != tc.want {
				t.Errorf("hasJSONValidCheck(%q) = %v, want %v", tc.col, got, tc.want)
			}
		})
	}
}

// TestRelabelMariaDBJSONColumns: only a LONGTEXT column carrying the json_valid
// CHECK is relabelled to "json"; a genuine LONGTEXT and non-LONGTEXT columns are
// left untouched.
func TestRelabelMariaDBJSONColumns(t *testing.T) {
	cols := []Column{
		{Name: "id", Type: "int"},
		{Name: "data", Type: "longtext"}, // JSON column → should become "json"
		{Name: "body", Type: "longtext"}, // genuine LONGTEXT → stays
		{Name: "name", Type: "varchar(255)"},
	}
	checks := []Check{{Name: "data", Expression: "json_valid(`data`)"}}
	relabelMariaDBJSONColumns(cols, checks)

	want := map[string]string{"id": "int", "data": "json", "body": "longtext", "name": "varchar(255)"}
	for _, c := range cols {
		if want[c.Name] != c.Type {
			t.Errorf("column %q: type = %q, want %q", c.Name, c.Type, want[c.Name])
		}
	}
}
