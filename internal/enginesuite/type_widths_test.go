// Copyright 2026 jcsvwinston/quark
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// QK-21: Quark's migrate layer mapped every Go integer width onto a single
// INTEGER, and every float onto REAL for PostgreSQL and SQLite. On the
// engines where INTEGER is four bytes that does not hold an int64, and REAL
// is single precision — about seven significant digits, where a float64 has
// fifteen.
//
// A4/S0 measured this by reading the type mapper, and said in writing that
// the per-engine behaviour was NOT confirmed. This is that confirmation, and
// afterwards the regression test: it writes values that only fit in the wider
// types and reads them back.

type twWide struct {
	ID    int64   `db:"id" pk:"true"`
	Big   int64   `db:"big"`
	Ubig  uint64  `db:"ubig"`
	Exact float64 `db:"exact"`
}

// testTypeWidths runs inside the shared per-engine suite.
func testTypeWidths(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Helper()
	dropTable(client, quark.GetModelMeta[twWide]().Table)
	if err := client.Migrate(ctx, &twWide{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, quark.GetModelMeta[twWide]().Table)

	// Values chosen to fail on the narrow types and survive the wide ones.
	const bigVal = int64(9_007_199_254_740_993) // > 2^53, and far past 2^31
	const ubigVal = uint64(9_223_372_036_854_775_807)
	// 15 significant digits: representable in float64, lost in float32.
	const exactVal = 1234567890.12345

	// The key is left for the engine to generate: SQL Server and Oracle
	// refuse an explicit value for an identity column, and the width being
	// measured here is the DATA columns', not the key's — testAutoPKWidth
	// covers that one.
	row := &twWide{Big: bigVal, Ubig: ubigVal, Exact: exactVal}
	if err := quark.For[twWide](ctx, client).Create(row); err != nil {
		t.Fatalf("insert of a value that needs the wide type failed: %v\n"+
			"this is QK-21: the generated column is too narrow for the Go type", err)
	}
	if row.ID == 0 {
		t.Fatal("the engine did not report the generated key")
	}

	got, err := quark.For[twWide](ctx, client).Find(row.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Big != bigVal {
		t.Errorf("int64 round-trip: got %d, want %d — the column truncated it", got.Big, bigVal)
	}
	if got.Ubig != ubigVal {
		t.Errorf("uint64 round-trip: got %d, want %d", got.Ubig, ubigVal)
	}
	// float64 keeps ~15 significant digits; float32 keeps ~7. Allow the last
	// unit in the last place, not a precision class.
	if math.Abs(got.Exact-exactVal) > 1e-4 {
		t.Errorf("float64 round-trip: got %.6f, want %.6f — the column is single precision",
			got.Exact, exactVal)
	}
}

// twAutoPK checks the other half of QK-21: an integer primary key was mapped
// to SERIAL on PostgreSQL, which is four bytes and runs out at 2,147,483,647
// rows. pkg/model's scaffold emits BIGSERIAL for the same model.
type twAutoPK struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func testAutoPKWidth(ctx context.Context, t *testing.T, client *quark.Client) {
	t.Helper()
	dropTable(client, quark.GetModelMeta[twAutoPK]().Table)
	if err := client.Migrate(ctx, &twAutoPK{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, quark.GetModelMeta[twAutoPK]().Table)

	// Assigning a key past the 32-bit range proves the column reaches it,
	// without inserting two billion rows. Identity columns refuse a
	// caller-supplied value (SQL Server unless IDENTITY_INSERT is ON,
	// Oracle with ORA-32795), so there the width is read from the catalog
	// instead — which is the thing under test either way.
	beyond32 := int64(math.MaxInt32) + 1000
	err := quark.For[twAutoPK](ctx, client).Create(&twAutoPK{ID: beyond32, Name: "far"})
	if err == nil {
		got, ferr := quark.For[twAutoPK](ctx, client).Find(beyond32)
		if ferr != nil {
			t.Fatalf("read back a key past 2^31: %v", ferr)
		}
		if got.ID != beyond32 {
			t.Errorf("pk round-trip: got %d, want %d", got.ID, beyond32)
		}
		return
	}
	if !isIdentityInsertRefusal(err) {
		t.Fatalf("a primary key past 2^31 was rejected: %v\n"+
			"this is QK-21: the auto-increment key column is 32-bit", err)
	}

	schema, serr := client.IntrospectSchema(ctx)
	if serr != nil {
		t.Fatalf("introspect: %v", serr)
	}
	// Ask the metadata for the table name rather than guessing it: the
	// pluraliser turns twAutoPK into tw_auto_p_ks, not tw_auto_pks.
	table := quark.GetModelMeta[twAutoPK]().Table
	col, ok := findColumn(schema, table, "id")
	if !ok {
		t.Fatalf("column %s.id not found in the catalog", table)
	}
	if !isWideIntegerType(col.Type) {
		t.Errorf("the identity key column is %q, want a 64-bit type\n"+
			"this is QK-21: the auto-increment key column is 32-bit", col.Type)
	}
}

// isIdentityInsertRefusal reports whether the engine refused the insert
// because the key is an identity column, not because of its width.
func isIdentityInsertRefusal(err error) bool {
	if errors.Is(err, quark.ErrUnsupportedFeature) {
		return true
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "ORA-32795") || strings.Contains(msg, "IDENTITY_INSERT")
}

// isWideIntegerType reports whether a catalog type name is 64-bit.
func isWideIntegerType(t string) bool {
	u := strings.ToUpper(strings.TrimSpace(t))
	switch {
	case strings.HasPrefix(u, "BIGINT"):
		return true
	case strings.HasPrefix(u, "NUMBER"): // Oracle identity columns
		return true
	case u == "INTEGER": // SQLite: INTEGER PRIMARY KEY is the 64-bit rowid
		return true
	default:
		return false
	}
}

func findColumn(s quark.Schema, table, column string) (quark.Column, bool) {
	for _, tb := range s.Tables {
		if !strings.EqualFold(tb.Name, table) {
			continue
		}
		for _, c := range tb.Columns {
			if strings.EqualFold(c.Name, column) {
				return c, true
			}
		}
	}
	return quark.Column{}, false
}
