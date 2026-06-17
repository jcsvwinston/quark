// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// setUserA is the canonical fixture for set-op integration. The set-op
// methods take a `*Query[T]` operand of the same T as the base, so a
// single model is enough to exercise UNION / UNION ALL semantics —
// distinct operands come from different Where filters on the same
// table.
type setUserA struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email"`
}

type setOpCapturing struct {
	quark.BaseMiddleware
	mu      sync.Mutex
	queries []string
}

func (m *setOpCapturing) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		s := strings.TrimSpace(sqlStr)
		if strings.HasPrefix(s, "SELECT") || strings.HasPrefix(s, "(") || strings.HasPrefix(s, "WITH") {
			m.mu.Lock()
			m.queries = append(m.queries, sqlStr)
			m.mu.Unlock()
		}
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *setOpCapturing) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.queries))
	copy(out, m.queries)
	return out
}

func testSetOp(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "set_user_as")
	if err := baseClient.Migrate(ctx, &setUserA{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "set_user_as")

	for _, e := range []string{"alice@x", "bob@x", "carol@x", "dave@x"} {
		row := setUserA{Email: e}
		if err := quark.For[setUserA](ctx, baseClient).Create(&row); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// The set-op API requires matching Query[T] types on both sides
	// (you can't UNION a Query[setUserA] with a Query[setUserB] without
	// raw SQL). The tests below use Query[setUserA] on both sides and
	// distinguish operands through Where filters; UNION DISTINCT
	// dedups, UNION ALL retains duplicates.

	t.Run("UnionAllRendersFlatCompoundSelect", func(t *testing.T) {
		mw := &setOpCapturing{}
		client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
		if err != nil {
			t.Fatalf("WithOptions: %v", err)
		}
		// alice or carol via UNION ALL of two single-row queries.
		alice := quark.For[setUserA](ctx, client).Where("email", "=", "alice@x")
		carol := quark.For[setUserA](ctx, client).Where("email", "=", "carol@x")

		got, err := alice.UnionAll(carol).List()
		if err != nil {
			t.Fatalf("UnionAll list: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2 rows from UNION ALL, got %d", len(got))
		}
		// Captured SQL renders the standard SQL compound-select form
		// (`SELECT ... UNION ALL SELECT ...` — flat, no parens around
		// operands). SQLite rejects parenthesised operands, so the flat
		// form is the portable shape across all six target dialects.
		captured := mw.snapshot()
		var sel string
		for _, q := range captured {
			if strings.Contains(q, "UNION") {
				sel = q
				break
			}
		}
		if !strings.Contains(sel, " UNION ALL SELECT ") {
			t.Errorf("expected `... UNION ALL SELECT ...` flat compound, got %q", sel)
		}
		if strings.Contains(sel, ") UNION") {
			t.Errorf("set-op rendering must not wrap operands in parens (SQLite rejects that), got %q", sel)
		}
	})

	t.Run("UnionDeduplicates", func(t *testing.T) {
		// Two operands selecting overlapping email subsets.
		//   LHS: WHERE email IN ('alice@x','carol@x')
		//   RHS: WHERE email IN ('carol@x','bob@x')
		// UNION DISTINCT → {alice, bob, carol}.
		//
		// `.OrderBy("email", "ASC")` on the base exercises the explicit-ordering
		// path under a compound-select. It USED to be required on MSSQL: the
		// implicit LIMIT 100 from `List()` becomes OFFSET/FETCH there, which needs
		// an ORDER BY, and buildSelect auto-injected `ORDER BY [id]` — but `id`
		// isn't in the operand SELECT list (`SELECT email`), so UNION rejected it
		// ("ORDER BY items must appear in the select list" — Finding J). buildSelect
		// now auto-injects the positional `ORDER BY 1` for set-ops, so the explicit
		// OrderBy is no longer required (UnionWithLimitNoExplicitOrderBy covers that
		// path); it's kept here to exercise explicit ordering. No-op elsewhere.
		lhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"alice@x", "carol@x"}).
			OrderBy("email", "ASC")
		rhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"carol@x", "bob@x"})
		got, err := lhs.Union(rhs).List()
		if err != nil {
			t.Fatalf("Union list: %v", err)
		}
		seen := map[string]struct{}{}
		for _, r := range got {
			seen[r.Email] = struct{}{}
		}
		// UNION (non-ALL) deduplicates — exactly 3 distinct emails.
		if len(seen) != 3 {
			t.Errorf("expected 3 distinct emails, got %v", seen)
		}
	})

	t.Run("UnionWithLimitNoExplicitOrderBy", func(t *testing.T) {
		// Finding J regression: `.Union(...).Limit(N)` with NO explicit OrderBy
		// must execute on every dialect. On MSSQL/Oracle the implicit OFFSET/FETCH
		// needs an ORDER BY; buildSelect now auto-injects the positional `ORDER BY
		// 1` (a select-list ordinal, valid under a compound-select) instead of the
		// PK column — UNION/INTERSECT/EXCEPT reject a non-projected column with
		// "ORDER BY items must appear in the select list". The other setop subtests
		// sidestep this with an explicit OrderBy; this one exercises the fix (the
		// superapp's builder-advanced surfaced it on MSSQL).
		lhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"alice@x", "carol@x"})
		rhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"carol@x", "bob@x"})
		got, err := lhs.Union(rhs).Limit(10).List()
		if err != nil {
			t.Fatalf("Union+Limit without explicit OrderBy must execute on every dialect: %v", err)
		}
		seen := map[string]struct{}{}
		for _, r := range got {
			seen[r.Email] = struct{}{}
		}
		if len(seen) != 3 {
			t.Errorf("expected 3 distinct emails (alice, bob, carol), got %v", seen)
		}
	})

	t.Run("IntersectFiltersCommonRows", func(t *testing.T) {
		// INTERSECT isn't supported by MySQL/MariaDB; the API returns
		// ErrUnsupportedFeature there (see setop.go:setOpKeyword). The
		// happy-path semantic test only applies to engines that accept
		// the operator — skip on the ones that reject it. Engines that
		// do support it (PostgreSQL, MSSQL, Oracle, SQLite) verify the
		// row-set arithmetic below.
		switch baseClient.Dialect().Name() {
		case "mysql", "mariadb":
			t.Skip("INTERSECT not supported on MySQL/MariaDB — covered by the rejection contract")
		}

		// Two operands selecting overlapping email subsets. INTERSECT
		// returns the rows present in BOTH, deduplicated.
		//   LHS: alice, bob, carol
		//   RHS: bob, carol, dave
		//   ∩  : bob, carol
		//
		// See UnionDeduplicates for why the base has an explicit OrderBy
		// (MSSQL OFFSET/FETCH + compound-select require an ORDER BY whose
		// items are in the operand SELECT list).
		lhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"alice@x", "bob@x", "carol@x"}).
			OrderBy("email", "ASC")
		rhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"bob@x", "carol@x", "dave@x"})

		got, err := lhs.Intersect(rhs).List()
		if err != nil {
			t.Fatalf("Intersect list: %v", err)
		}
		seen := map[string]struct{}{}
		for _, r := range got {
			seen[r.Email] = struct{}{}
		}
		if len(seen) != 2 || (func() bool { _, ok := seen["bob@x"]; return !ok }()) ||
			(func() bool { _, ok := seen["carol@x"]; return !ok }()) {
			t.Errorf("expected {bob, carol}, got %v", seen)
		}
	})

	t.Run("ExceptFiltersUnique", func(t *testing.T) {
		// Same as IntersectFiltersCommonRows: MySQL/MariaDB don't
		// support EXCEPT, the dialect surfaces ErrUnsupportedFeature
		// in those cases — happy-path semantic test only applies to
		// the engines that accept the operator. Oracle spells it MINUS
		// (handled in setOpKeyword) but the Go-level API stays Except.
		switch baseClient.Dialect().Name() {
		case "mysql", "mariadb":
			t.Skip("EXCEPT not supported on MySQL/MariaDB — covered by the rejection contract")
		}

		// EXCEPT: rows in LHS not in RHS, deduplicated.
		//   LHS: alice, bob, carol
		//   RHS: bob, dave
		//   −  : alice, carol
		//
		// See UnionDeduplicates for the OrderBy rationale.
		lhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"alice@x", "bob@x", "carol@x"}).
			OrderBy("email", "ASC")
		rhs := quark.For[setUserA](ctx, baseClient).
			Select("email").
			WhereIn("email", []any{"bob@x", "dave@x"})

		got, err := lhs.Except(rhs).List()
		if err != nil {
			t.Fatalf("Except list: %v", err)
		}
		seen := map[string]struct{}{}
		for _, r := range got {
			seen[r.Email] = struct{}{}
		}
		if len(seen) != 2 || (func() bool { _, ok := seen["alice@x"]; return !ok }()) ||
			(func() bool { _, ok := seen["carol@x"]; return !ok }()) {
			t.Errorf("expected {alice, carol}, got %v", seen)
		}
	})

	t.Run("RejectsLockOnBase", func(t *testing.T) {
		// Pessimistic locking on the base + set-ops is an unsupported
		// combination because the dialect-specific lock suffix would
		// bind to the combined result in a way most engines don't model.
		other := quark.For[setUserA](ctx, baseClient).Where("email", "=", "alice@x")
		q := quark.For[setUserA](ctx, baseClient).
			Where("email", "=", "bob@x").
			ForUpdate().
			Union(other)
		_, err := q.List()
		if err == nil {
			t.Fatalf("expected ErrUnsupportedFeature, got nil")
		}
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("expected ErrUnsupportedFeature, got %v", err)
		}
	})

	t.Run("NilOperandRejected", func(t *testing.T) {
		_, err := quark.For[setUserA](ctx, baseClient).Union(nil).List()
		if err == nil {
			t.Fatalf("expected error for Union(nil)")
		}
		if !errors.Is(err, quark.ErrInvalidQuery) {
			t.Errorf("expected ErrInvalidQuery, got %v", err)
		}
	})

	t.Run("OperandWithOrderByRejected", func(t *testing.T) {
		// ORDER BY on the operand is rejected because it doesn't
		// translate to a portable SQL form across all six dialects.
		// The combined ORDER BY belongs on the outer query.
		bad := quark.For[setUserA](ctx, baseClient).
			Where("email", "=", "alice@x").
			OrderBy("id", "ASC")
		_, err := quark.For[setUserA](ctx, baseClient).Union(bad).List()
		if err == nil {
			t.Fatalf("expected ErrUnsupportedFeature for operand with ORDER BY")
		}
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("expected ErrUnsupportedFeature, got %v", err)
		}
	})

	t.Run("OperandWithLimitRejected", func(t *testing.T) {
		bad := quark.For[setUserA](ctx, baseClient).
			Where("email", "=", "alice@x").
			Limit(5)
		_, err := quark.For[setUserA](ctx, baseClient).Union(bad).List()
		if err == nil {
			t.Fatalf("expected ErrUnsupportedFeature for operand with LIMIT")
		}
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("expected ErrUnsupportedFeature, got %v", err)
		}
	})

	t.Run("IntersectExceptRejectedOnMySQL", func(t *testing.T) {
		// Mirror image of the happy-path subtests above: on MySQL and
		// MariaDB, the dialect should reject Intersect / Except with
		// ErrUnsupportedFeature. The other engines accept the operator
		// — skip there because there's nothing to assert.
		switch baseClient.Dialect().Name() {
		case "mysql", "mariadb":
			// expected to error
		default:
			t.Skip("dialect supports INTERSECT/EXCEPT — this rejection contract only applies to MySQL/MariaDB")
		}

		lhs := quark.For[setUserA](ctx, baseClient).Select("email")
		rhs := quark.For[setUserA](ctx, baseClient).Select("email")

		_, err := lhs.Intersect(rhs).List()
		if err == nil || !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("Intersect on MySQL/MariaDB should return ErrUnsupportedFeature, got %v", err)
		}

		_, err = lhs.Except(rhs).List()
		if err == nil || !errors.Is(err, quark.ErrUnsupportedFeature) {
			t.Errorf("Except on MySQL/MariaDB should return ErrUnsupportedFeature, got %v", err)
		}
	})
}
