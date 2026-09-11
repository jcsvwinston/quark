// Copyright 2026 jcsvwinston/quark
// SPDX-License-Identifier: Apache-2.0

package enginesuite

import (
	"context"
	"errors"
	"math"
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
	dropTable(client, "tw_wides")
	if err := client.Migrate(ctx, &twWide{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, "tw_wides")

	// Values chosen to fail on the narrow types and survive the wide ones.
	const bigVal = int64(9_007_199_254_740_993) // > 2^53, and far past 2^31
	const ubigVal = uint64(9_223_372_036_854_775_807)
	// 15 significant digits: representable in float64, lost in float32.
	const exactVal = 1234567890.12345

	row := &twWide{ID: 1, Big: bigVal, Ubig: ubigVal, Exact: exactVal}
	if err := quark.For[twWide](ctx, client).Create(row); err != nil {
		t.Fatalf("insert of a value that needs the wide type failed: %v\n"+
			"this is QK-21: the generated column is too narrow for the Go type", err)
	}

	got, err := quark.For[twWide](ctx, client).Find(int64(1))
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
	dropTable(client, "tw_auto_pks")
	if err := client.Migrate(ctx, &twAutoPK{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(client, "tw_auto_pks")

	// Assigning a key past the 32-bit range proves the column is wide
	// enough to reach it, without having to insert two billion rows.
	beyond32 := int64(math.MaxInt32) + 1000
	if err := quark.For[twAutoPK](ctx, client).Create(&twAutoPK{ID: beyond32, Name: "far"}); err != nil {
		if errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Skipf("engine does not accept a caller-assigned key here: %v", err)
		}
		t.Fatalf("a primary key past 2^31 was rejected: %v\n"+
			"this is QK-21: the auto-increment key column is 32-bit", err)
	}
	got, err := quark.For[twAutoPK](ctx, client).Find(beyond32)
	if err != nil {
		t.Fatalf("read back a key past 2^31: %v", err)
	}
	if got.ID != beyond32 {
		t.Errorf("pk round-trip: got %d, want %d", got.ID, beyond32)
	}
}
