package quarktenant_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktenant"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The client the package's own docs prescribe: quark.New with models
// registered, no options. Until QCD-QK-1 this could not run the preflight
// at all — RawQuery is off by default, so a boot guardrail failed
// indistinguishably from a real outage.
func TestVerifyRLSPolicies_WorksWithTheDocumentedClient(t *testing.T) {
	dsn := postgresDSN(t)
	if dsn == "" {
		t.Skip("postgres DSN unavailable")
	}
	ctx := context.Background()
	client, err := quark.New("pgx", dsn)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	type qk1Order struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
	}
	// Setup/teardown DDL goes through Raw(): this client deliberately has
	// no limits (that is the point of the test), so client.Exec would be
	// refused as a raw query and the table would survive between runs
	// carrying its installed policy.
	drop := func() {
		if _, err := client.Raw().ExecContext(ctx, "DROP TABLE IF EXISTS qk1_orders CASCADE"); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}
	drop()
	t.Cleanup(drop)
	if err := client.RegisterModel(&qk1Order{}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := client.MigrateRegistered(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	opts := quarktenant.DefaultInstallOptions()
	_, err = quarktenant.VerifyRLSPolicies(ctx, client, opts)
	if !errors.Is(err, quarktenant.ErrRLSNotEnforced) {
		t.Fatalf("the documented client must reach a verdict (not-enforced here), got: %v", err)
	}
	if _, err := quarktenant.InstallRLSPolicies(ctx, client, opts); err != nil {
		t.Fatalf("install: %v", err)
	}
	if findings, err := quarktenant.VerifyRLSPolicies(ctx, client, opts); err != nil || len(findings) != 0 {
		t.Fatalf("want clean after install, got findings=%+v err=%v", findings, err)
	}
}
