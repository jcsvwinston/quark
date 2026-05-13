package quark_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testJoinOnSecurity is the regression test for P0-5. Before the fix the
// `on` argument of Join/LeftJoin/RightJoin was concatenated raw into the
// final SQL with no validation — an inconsistency with WHERE which already
// validated identifiers. After the fix the on clause must match the minimal
// identifier-only grammar; injection payloads return ErrInvalidJoin at
// execution time without running any SQL.
func testJoinOnSecurity(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type JoinUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}

	type JoinOrder struct {
		ID     int64  `db:"id" pk:"true"`
		UserID int64  `db:"user_id"`
		Status string `db:"status"`
	}

	dropTable(baseClient, "join_orders")
	dropTable(baseClient, "join_users")
	if err := baseClient.Migrate(ctx, &JoinUser{}, &JoinOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "join_orders")
	defer dropTable(baseClient, "join_users")

	t.Run("ValidJoinExecutes", func(t *testing.T) {
		// A canonical identifier-only ON clause must pass the validator and
		// execute without error. The typed builder form `.On(left, op, right)`
		// is the v0.4 idiomatic shape — it composes the ON clause from
		// validated parts and forwards through the same ValidateJoinOn path.
		//
		// `Count()` instead of `List()` so MSSQL doesn't reject the implicit
		// `SELECT *` over a multi-table join with overlapping `id` columns
		// ("Ambiguous column name 'id'"). The contract being pinned is
		// "ON clause is accepted and the JOIN executes" — Count exercises
		// both as well as List does, without the projection ambiguity.
		_, err := quark.For[JoinOrder](ctx, baseClient).
			Join("join_users").On("join_users.id", "=", "join_orders.user_id").
			Count()
		if err != nil {
			t.Errorf("expected valid ON clause to execute, got: %v", err)
		}
	})

	t.Run("ValidMultiConditionJoinExecutes", func(t *testing.T) {
		// Multi-condition ON clauses fall back to OnRaw, which still
		// validates through guard.ValidateJoinOn (AND-chained binary
		// identifier comparisons are accepted). Count() for the same
		// reason as ValidJoinExecutes.
		_, err := quark.For[JoinOrder](ctx, baseClient).
			LeftJoin("join_users").OnRaw("join_users.id = join_orders.user_id AND join_users.id = join_orders.user_id").
			Count()
		if err != nil {
			t.Errorf("expected valid AND-joined ON clause to execute, got: %v", err)
		}
	})

	t.Run("InjectionAttemptRejected", func(t *testing.T) {
		injectionClauses := []string{
			"join_users.id = join_orders.user_id; DROP TABLE join_orders",
			"join_users.id = join_orders.user_id -- comment",
			"join_users.id = join_orders.user_id /* x */",
			"join_users.id = 1",
			"join_users.id = 'alice'",
			"(join_users.id = join_orders.user_id)",
			"join_users.id = join_orders.user_id OR 1=1",
			"join_users.id = join_orders.user_id UNION SELECT 1",
		}
		for _, bad := range injectionClauses {
			t.Run(bad, func(t *testing.T) {
				_, err := quark.For[JoinOrder](ctx, baseClient).
					Join("join_users").OnRaw(bad).
					Limit(10).
					List()
				if err == nil {
					t.Errorf("ON clause %q should have been rejected, got nil error", bad)
					return
				}
				if !errors.Is(err, quark.ErrInvalidJoin) {
					t.Errorf("ON %q: expected ErrInvalidJoin, got %v", bad, err)
				}
			})
		}
	})

	t.Run("InjectionAttemptRejectedInCount", func(t *testing.T) {
		// Count() builds the JOIN SQL on its own path (see query_exec.go)
		// — verify the validator covers it too.
		_, err := quark.For[JoinOrder](ctx, baseClient).
			Join("join_users").OnRaw("join_users.id = join_orders.user_id; DROP TABLE join_orders").
			Count()
		if err == nil {
			t.Fatal("Count with injectable ON clause should have errored")
		}
		if !errors.Is(err, quark.ErrInvalidJoin) {
			t.Errorf("Count: expected ErrInvalidJoin, got %v", err)
		}
	})
}
