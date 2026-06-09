package exercise

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// TestTenantDBDSN cubre los rewriters de DSN de DatabasePerTenant como funciones
// puras (sin motor): los formatos son los reales de engine.Up / la CI.
func TestTenantDBDSN(t *testing.T) {
	cases := []struct {
		engine control.Engine
		base   string
		want   string
	}{
		{control.SQLite, "/tmp/superapp-1.db", "/tmp/superapp-1.db.dbt_ta"},
		{control.Postgres, "postgres://postgres:quark@localhost:5435/postgres?sslmode=disable",
			"postgres://postgres:quark@localhost:5435/dbt_ta?sslmode=disable"},
		{control.MySQL, "root:quark@tcp(localhost:3310)/mysql?parseTime=true&multiStatements=true",
			"root:quark@tcp(localhost:3310)/dbt_ta?parseTime=true&multiStatements=true"},
		{control.MariaDB, "root:quark@tcp(localhost:3311)/mysql?parseTime=true",
			"root:quark@tcp(localhost:3311)/dbt_ta?parseTime=true"},
		// MySQL sin params: el dbname es el sufijo tras la última '/'.
		{control.MySQL, "root:quark@tcp(localhost:3310)/mysql",
			"root:quark@tcp(localhost:3310)/dbt_ta"},
		// Password hostil con '?' y '/' (posible vía SUPERAPP_DSN_MYSQL): el
		// split se ancla en el ")/" de tcp(addr), no en el primer '?'.
		{control.MySQL, "root:p?as/s@tcp(localhost:3310)/mysql?parseTime=true",
			"root:p?as/s@tcp(localhost:3310)/dbt_ta?parseTime=true"},
	}
	for _, c := range cases {
		got, err := tenantDBDSN(c.engine, c.base, "dbt_ta")
		if err != nil {
			t.Errorf("%s: %v", c.engine, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.engine, got, c.want)
		}
	}

	// MSSQL: url.Values reordena los params, así que se asierta por contenido.
	got, err := tenantDBDSN(control.MSSQL, "sqlserver://sa:Quark!2026@localhost:1435?database=master", "dbt_ta")
	if err != nil {
		t.Fatalf("mssql: %v", err)
	}
	if !strings.Contains(got, "database=dbt_ta") || !strings.HasPrefix(got, "sqlserver://sa:") {
		t.Errorf("mssql: DSN reescrito incorrecto: %q", got)
	}

	// Oracle no tiene rewriter (FeatDBPerTenantProvision lo excluye).
	if _, err := tenantDBDSN(control.Oracle, "oracle://system:quark@localhost:1523/FREEPDB1", "dbt_ta"); err == nil {
		t.Error("oracle debió devolver error (sin rewriter)")
	}
}
