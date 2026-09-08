// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// This file is in package schema (not schema_test) because parseDBTag is
// unexported and is one half of the pair under test: the OTHER half,
// ColumnFromDBTag, is the exported shortcut the hot paths in package quark use
// to read the same tag without computing a whole ModelMeta. Two readers of one
// string are exactly the shape that drifts, and the column name they produce is
// what reaches guard.ValidateIdentifier and then the SQL.
package schema

import (
	"strconv"
	"strings"
	"testing"
)

// clip bounds a fuzz input. Struct tags are source text a human typed; nothing
// is learned from a mutator-grown kilobyte, and the rescans cost throughput.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// FuzzColumnNaming drives both ways a column gets its name: the db tag when
// there is one, and the derivation from the Go field name when there is not.
// Whichever produced it, the name goes to guard.ValidateIdentifier and then
// into the SQL, so this is one surface with two entry points.
//
// Properties:
//
//   - The two readers of a db tag must name the same column. ColumnFromDBTag
//     feeds identifiers to the SQL guard on the hot paths while parseDBTag
//     feeds the schema; if they disagree about the column, the table is built
//     with one name and queried with another.
//   - Whatever parseDBTag understood must survive being written back out.
//     Re-serialising the parsed options and parsing again has to yield the
//     same values, or an option means something different on the second read
//     than on the first.
//   - The derivation cannot make a name less safe than it found it: an ASCII
//     Go identifier must come out as something the guard's charset accepts,
//     so the only reasons a derived column can be rejected are the ones that
//     would have applied to the field name itself (length, reserved keyword).
//   - The derivation is idempotent, so a column name never depends on how
//     many times the schema layer looked at the field.
func FuzzColumnNaming(f *testing.F) {
	tagSeeds := []string{
		// Shapes the model/migration tests carry.
		"", "id", "name", "extra_field", "-",
		"name,size=512", "price,precision=18,scale=4", "qty,size=10",
		// DX-8: the typos that used to produce silent DDL.
		"price,lenght=10", "qty,size=abc", "name,size=", "name,=10",
		"name,size=0", "name,size=-1", "name,size=+5", "name,SIZE=7",
		"name, size = 512 ", " name ", "name,,size=8", "name,size=1,size=2",
		"name,size=9223372036854775808",
		// Identifier payloads: the column name reaches the guard.
		"id; DROP TABLE t;--", "a\"b", "a`b", "a.b", "a b",
	}
	fieldSeeds := []string{
		"ID", "UserID", "CreatedAt", "HTTPServer", "OAuthToken", "A", "_private",
		"Name", "Price", "Qty", "ExtraField", "ABC", "aB", "A1B2", "X_Y",
		"", "ñame", "Ñame", "日本", "user-name", "user id",
	}
	for i, tag := range tagSeeds {
		f.Add(tag, fieldSeeds[i%len(fieldSeeds)])
	}
	for _, field := range fieldSeeds {
		f.Add("", field)
	}

	f.Fuzz(func(t *testing.T, tag, fieldName string) {
		tag, fieldName = clip(tag, 512), clip(fieldName, 256)

		col, size, precision, scale := parseDBTag(tag)

		if got := ColumnFromDBTag(tag); got != col {
			t.Fatalf("the two readers of db:%q disagree about the column: ColumnFromDBTag=%q, parseDBTag=%q", tag, got, col)
		}
		if size < 0 || precision < 0 || scale < 0 {
			t.Fatalf("parseDBTag(%q) produced a negative sizing hint: size=%d precision=%d scale=%d", tag, size, precision, scale)
		}
		if strings.Contains(col, ",") {
			t.Fatalf("parseDBTag(%q) returned a column %q containing the option separator", tag, col)
		}

		// Round-trip: write the understood options back out and read them
		// again. The column is never re-quoted, so a column carrying a comma
		// could not round-trip — parseDBTag cannot produce one (asserted
		// above), which is what makes the re-serialisation total.
		var b strings.Builder
		b.WriteString(col)
		for _, opt := range []struct {
			key string
			val int
		}{{"size", size}, {"precision", precision}, {"scale", scale}} {
			if opt.val > 0 {
				b.WriteString("," + opt.key + "=" + strconv.Itoa(opt.val))
			}
		}
		rt := b.String()
		col2, size2, precision2, scale2 := parseDBTag(rt)
		if col2 != col || size2 != size || precision2 != precision || scale2 != scale {
			t.Fatalf("db:%q parsed as (%q,%d,%d,%d), re-serialised to %q and re-parsed as (%q,%d,%d,%d)",
				tag, col, size, precision, scale, rt, col2, size2, precision2, scale2)
		}

		// The other entry point: no usable db tag, so the column name is
		// DERIVED from the Go field name and goes to the guard just the same.
		derived := ToSnakeCase(fieldName)
		if again := ToSnakeCase(derived); again != derived {
			t.Fatalf("ToSnakeCase is not idempotent: %q -> %q -> %q", fieldName, derived, again)
		}
		if !goIdentifierCharset(fieldName) {
			return
		}
		for i := 0; i < len(derived); i++ {
			c := derived[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				t.Fatalf("ToSnakeCase(%q) = %q, which carries a byte the SQL guard rejects", fieldName, derived)
			}
		}
		if derived != "" && derived[0] >= '0' && derived[0] <= '9' {
			t.Fatalf("ToSnakeCase(%q) = %q, which starts with a digit", fieldName, derived)
		}
	})
}

// goIdentifierCharset reports whether name is an ASCII Go identifier — the
// case where the derived column has to stay guard-safe.
func goIdentifierCharset(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
