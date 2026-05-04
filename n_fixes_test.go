package quark_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jcsvwinston/quark"
)

// testNFixes is wired into SharedSuite and provides regression coverage for
// bugs N1-N5 found during the V2 competitive audit.
//
//   - N1: Oracle MERGE AS alias syntax
//   - N2: Oracle CreateBatch INSERT ALL
//   - N4: MSSQL composite PK create (no SCOPE_IDENTITY)
//   - N5: Oracle DISTINCT / GROUP BY + auto-ORDER BY pagination
func testNFixes(ctx context.Context, t *testing.T, client *quark.Client) {
	// ── shared model ─────────────────────────────────────────────────────────
	type NFUser struct {
		ID    int64  `db:"id"    pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
		Age   int    `db:"age"`
	}

	dropTable(client, "nf_users")
	if err := client.Migrate(ctx, &NFUser{}); err != nil {
		t.Fatalf("migrate nf_users: %v", err)
	}

	seed := []*NFUser{
		{Name: "Alpha", Email: "a@nf.com", Age: 10},
		{Name: "Alpha", Email: "b@nf.com", Age: 20},
		{Name: "Beta", Email: "c@nf.com", Age: 30},
		{Name: "Beta", Email: "d@nf.com", Age: 40},
		{Name: "Gamma", Email: "e@nf.com", Age: 50},
	}

	// ── N2: CreateBatch (Oracle INSERT ALL, others multi-row VALUES) ──────────
	t.Run("N2_CreateBatch", func(t *testing.T) {
		if err := quark.For[NFUser](ctx, client).CreateBatch(seed); err != nil {
			t.Fatalf("N2 CreateBatch: %v", err)
		}
		count, err := quark.For[NFUser](ctx, client).Count()
		if err != nil {
			t.Fatalf("N2 count: %v", err)
		}
		if count != int64(len(seed)) {
			t.Errorf("N2: expected %d rows, got %d", len(seed), count)
		}
	})

	// ── N1: Upsert / MERGE (Oracle no AS alias) ───────────────────────────────
	t.Run("N1_Upsert_Merge", func(t *testing.T) {
		first, err := quark.For[NFUser](ctx, client).Where("email", "=", "a@nf.com").First()
		if err != nil {
			t.Fatalf("N1 find first: %v", err)
		}
		updated := &NFUser{ID: first.ID, Name: "Alpha-Updated", Email: first.Email, Age: 99}
		if err := quark.For[NFUser](ctx, client).Upsert(updated, []string{"id"}, []string{"name", "age"}); err != nil {
			t.Fatalf("N1 Upsert (MERGE): %v", err)
		}
		got, err := quark.For[NFUser](ctx, client).Find(first.ID)
		if err != nil {
			t.Fatalf("N1 find after upsert: %v", err)
		}
		if got.Name != "Alpha-Updated" {
			t.Errorf("N1: expected Name='Alpha-Updated', got %q", got.Name)
		}
		if got.Age != 99 {
			t.Errorf("N1: expected Age=99, got %d", got.Age)
		}
	})

	// ── N5a: DISTINCT + pagination (Oracle ORA-01791 guard) ──────────────────
	t.Run("N5_Distinct_Paginate", func(t *testing.T) {
		results, err := quark.For[NFUser](ctx, client).
			Select("name").
			Distinct().
			Limit(2).
			Offset(0).
			List()
		if err != nil {
			t.Fatalf("N5 Distinct+Paginate: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("N5 Distinct+Paginate: expected 2 rows (limit), got %d", len(results))
		}
		seen := map[string]int{}
		for _, r := range results {
			seen[r.Name]++
		}
		for name, cnt := range seen {
			if cnt > 1 {
				t.Errorf("N5 Distinct: name %q duplicated %d times", name, cnt)
			}
		}
	})

	// ── N5b: GROUP BY + pagination (Oracle ORA-00979 guard) ──────────────────
	t.Run("N5_GroupBy_Paginate", func(t *testing.T) {
		results, err := quark.For[NFUser](ctx, client).
			Select("name").
			GroupBy("name").
			Limit(2).
			Offset(0).
			List()
		if err != nil {
			t.Fatalf("N5 GroupBy+Paginate: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("N5 GroupBy+Paginate: expected 2 rows (limit), got %d", len(results))
		}
	})

	// ── N4: CompositePK create on MSSQL (no SCOPE_IDENTITY scan on NULL) ─────
	t.Run("N4_CompositePK_Create", func(t *testing.T) {
		type NFCPK struct {
			TenantID int64  `db:"tenant_id" pk:"true"`
			SlotID   int64  `db:"slot_id"   pk:"true"`
			Label    string `db:"label"`
		}

		// Table name derived from struct NFCPK: Pluralize→NFCPKs, ToSnakeCase→nfcp_ks
		dropTable(client, "nfcp_ks")
		if err := client.Migrate(ctx, &NFCPK{}); err != nil {
			t.Fatalf("N4 migrate nfcp_ks: %v", err)
		}
		row := NFCPK{TenantID: 42, SlotID: 7, Label: "test"}
		if err := quark.For[NFCPK](ctx, client).Create(&row); err != nil {
			t.Fatalf("N4 CompositePK Create: %v", err)
		}
		all, err := quark.For[NFCPK](ctx, client).Where("tenant_id", "=", 42).List()
		if err != nil {
			t.Fatalf("N4 list: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("N4: expected 1 row, got %d", len(all))
		}
		_ = fmt.Sprintf("dialect=%s N4 ok", client.Dialect().Name())
	})
}
