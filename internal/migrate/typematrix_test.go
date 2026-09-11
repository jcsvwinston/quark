// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"flag"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark/internal/migrate"
)

var writeMatrix = flag.Bool("write-type-matrix", false,
	"rewrite website/docs/reference/type-matrix.mdx from the live type mapper")

// The type matrix is GENERATED, not written. A hand-maintained table of what
// each engine gets for each Go type is a promise nobody can keep: it was
// wrong about integers for as long as it existed (QK-21), and reading it was
// how A4/S0 first got the measurement wrong.
//
// TestTypeMatrixIsCurrent fails when the published page and the mapper
// disagree. Regenerate with:
//
//	go test ./internal/migrate -run TestTypeMatrix -write-type-matrix
func TestTypeMatrixIsCurrent(t *testing.T) {
	got := renderTypeMatrix()
	const path = "../../website/docs/reference/type-matrix.mdx"

	if *writeMatrix {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		t.Log("type matrix rewritten")
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published matrix: %v\nregenerate with -write-type-matrix", err)
	}
	if string(want) != got {
		t.Errorf("the published type matrix is stale.\n" +
			"Regenerate it:\n\n\tgo test ./internal/migrate -run TestTypeMatrix -write-type-matrix\n\n" +
			"and commit the result.")
	}
}

type matrixRow struct {
	label string
	typ   reflect.Type
	opts  migrate.TypeOptions
	note  string
}

func matrixRows() []matrixRow {
	return []matrixRow{
		{"`string`", reflect.TypeOf(""), migrate.TypeOptions{}, ""},
		{"`string` with `size=255`", reflect.TypeOf(""), migrate.TypeOptions{Size: 255}, ""},
		{"`bool`", reflect.TypeOf(false), migrate.TypeOptions{}, ""},
		{"`int8` / `int16`", reflect.TypeOf(int16(0)), migrate.TypeOptions{}, ""},
		{"`int32`", reflect.TypeOf(int32(0)), migrate.TypeOptions{}, ""},
		{"`int` / `int64`", reflect.TypeOf(int64(0)), migrate.TypeOptions{}, "Go's `int` is 64-bit on every supported platform."},
		{"`uint64`", reflect.TypeOf(uint64(0)), migrate.TypeOptions{}, "Stored signed; values above 2⁶³−1 do not fit."},
		{"`float32`", reflect.TypeOf(float32(0)), migrate.TypeOptions{}, ""},
		{"`float64`", reflect.TypeOf(float64(0)), migrate.TypeOptions{}, ""},
		{"`float64` with `precision=18,scale=4`", reflect.TypeOf(float64(0)), migrate.TypeOptions{Precision: 18, Scale: 4}, "Use this for money: binary floats cannot represent every decimal."},
		{"`time.Time`", reflect.TypeOf(time.Time{}), migrate.TypeOptions{}, ""},
		{"`[]byte`", reflect.TypeOf([]byte{}), migrate.TypeOptions{}, ""},
		{"`time.Duration`", reflect.TypeOf(time.Duration(0)), migrate.TypeOptions{}, "Stored as an integer count of nanoseconds."},
		{"integer primary key", reflect.TypeOf(int64(0)), migrate.TypeOptions{IsPK: true}, "Auto-increment."},
		{"`string` primary key", reflect.TypeOf(""), migrate.TypeOptions{IsPK: true}, "For UUID/ULID keys the caller supplies."},
		{"`[]string`, `[]int64`, `map[string]any`", reflect.TypeOf([]string{}), migrate.TypeOptions{}, "Serialised as text; no native array or JSONB column yet."},
	}
}

func renderTypeMatrix() string {
	dialects := []string{"postgres", "mysql", "mariadb", "sqlite", "mssql", "oracle"}
	bt := "`"
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: Type matrix\n")
	b.WriteString("description: The SQL type Quark generates for each Go type, per engine.\n")
	b.WriteString("---\n\n")
	b.WriteString("# Type matrix\n\n")
	b.WriteString("What " + bt + "Migrate" + bt + " and " + bt + "PlanMigration" + bt +
		" generate for each Go type, on each engine.\n\n")
	b.WriteString("**This page is generated from the type mapper itself**, so it cannot drift\n" +
		"from what the code does. A hand-written version was wrong about integer\n" +
		"widths for as long as it existed.\n\n")
	b.WriteString("| Go type | PostgreSQL | MySQL | MariaDB | SQLite | SQL Server | Oracle |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
	var notes []string
	for _, r := range matrixRows() {
		b.WriteString("| " + r.label)
		for _, d := range dialects {
			b.WriteString(" | " + bt + migrate.SQLTypeWithOpts(d, r.typ, r.opts) + bt)
		}
		b.WriteString(" |\n")
		if r.note != "" {
			notes = append(notes, "- **"+strings.ReplaceAll(r.label, bt, "")+"** — "+r.note)
		}
	}
	if len(notes) > 0 {
		b.WriteString("\n## Notes\n\n")
		b.WriteString(strings.Join(notes, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\n## Integer width\n\n")
	b.WriteString("Integers map by the width of the Go type. They used to collapse onto a\n" +
		"single " + bt + "INTEGER" + bt + ", which is four bytes on PostgreSQL, MySQL and SQL\n" +
		"Server: an " + bt + "int64" + bt + " past 2\u00b3\u00b9 was rejected by those engines while\n" +
		"looking fine on SQLite, whose " + bt + "INTEGER" + bt + " is dynamically sized.\n" +
		"Auto-increment keys were narrow for the same reason and ran out at\n" +
		"2,147,483,647 rows.\n\n")
	b.WriteString("If you have tables created before this changed, " + bt + "PlanMigration" + bt + "\n" +
		"reports the widening as an " + bt + "ALTER COLUMN" + bt + ". Applying it is safe —\n" +
		"widening loses no data — but on a large table the engine may rewrite it, so\n" +
		"run it when a rewrite is acceptable rather than at deploy time.\n\n")
	b.WriteString("## Floating point\n\n")
	b.WriteString(bt + "REAL" + bt + " is single precision on PostgreSQL: about seven significant\n" +
		"digits, where a " + bt + "float64" + bt + " carries fifteen. SQLite's " + bt + "REAL" + bt +
		" is an\n8-byte IEEE double and was never affected.\n\n")
	b.WriteString("For money, use " + bt + "precision" + bt + " and " + bt + "scale" + bt +
		" and get a " + bt + "DECIMAL" + bt + ":\nno binary float represents every decimal fraction exactly.\n")
	return b.String()
}
