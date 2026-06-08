package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testIdentifierSecurity is the cross-engine regression for the
// ErrInvalidIdentifier reachability gap the superapp surfaced: a hostile
// identifier in any builder position (Where / OrderBy / GroupBy column) is
// rejected before SQL runs AND surfaces as errors.Is(err,
// quark.ErrInvalidIdentifier) — consistent with ErrInvalidJoin / ErrInvalidJSONPath.
func testIdentifierSecurity(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type IdentDoc struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	dropTable(baseClient, "ident_docs")
	if err := baseClient.Migrate(ctx, &IdentDoc{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "ident_docs")

	const hostile = `id; DROP TABLE ident_docs;--`

	t.Run("WhereColumn", func(t *testing.T) {
		_, err := quark.For[IdentDoc](ctx, baseClient).Where(hostile, "=", 1).List()
		if !errors.Is(err, quark.ErrInvalidIdentifier) {
			t.Errorf("Where(hostile): errors.Is(ErrInvalidIdentifier)=false, err=%v", err)
		}
	})
	t.Run("OrderByColumn", func(t *testing.T) {
		_, err := quark.For[IdentDoc](ctx, baseClient).OrderBy(hostile, "ASC").Limit(1).List()
		if !errors.Is(err, quark.ErrInvalidIdentifier) {
			t.Errorf("OrderBy(hostile): errors.Is(ErrInvalidIdentifier)=false, err=%v", err)
		}
	})
	t.Run("GroupByColumn", func(t *testing.T) {
		_, err := quark.For[IdentDoc](ctx, baseClient).GroupBy(hostile).List()
		if !errors.Is(err, quark.ErrInvalidIdentifier) {
			t.Errorf("GroupBy(hostile): errors.Is(ErrInvalidIdentifier)=false, err=%v", err)
		}
	})
	t.Run("ValidIdentifierAccepted", func(t *testing.T) {
		if _, err := quark.For[IdentDoc](ctx, baseClient).Where("name", "=", "x").Limit(1).List(); err != nil {
			t.Errorf("valid identifier wrongly rejected: %v", err)
		}
	})
}
