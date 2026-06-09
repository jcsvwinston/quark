//go:build superapp_engine

// Corre los exercisers contra Postgres real vía docker-run (tag superapp_engine,
// reusa el del paquete engine). Prueba que el patrón es cross-engine: el mismo
// crud+tx (soft-delete, lock optimista, commit/rollback) verde en PG, sin fugas.
//
//	go test -tags=superapp_engine -run TestExerciseDocker -v ./examples/superapp/exercise/
package exercise

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestExerciseDockerPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	engines := []control.Engine{control.Postgres}
	conns, err := engine.Up(ctx, engines...)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer engine.Down(engines...)

	results := Run(conns, 4, []Exerciser{CRUD, TX, BUILDER, RELATIONS, SECURITY, CACHE, TENANT, RLSNATIVE, SCHEMAPERTENANT, DBPERTENANT})
	cov := Coverage(results)
	for e, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", e, r.Err)
			continue
		}
		if !r.Leak.OK() {
			t.Errorf("%s fuga: %s", e, r.Leak)
		}
		// Cobertura por símbolo de las 4 estrategias de tenant — en PG todas
		// corren su path funcional completo; un skip accidental (p.ej. un return
		// nil antes del Note) se delataría aquí, no sólo en el conteo total.
		if e == control.Postgres {
			for _, k := range []string{
				QF("RowLevelSecurityClient"), QF("RowLevelSecurityNative"),
				QF("SchemaPerTenant"), QF("DatabasePerTenant"),
				TRM("Tx"), TRM("ActiveTenants"), TRM("ResolveTenant"), TRM("GetClient"),
			} {
				if !cov[e][k] {
					t.Errorf("%s cobertura: falta el símbolo %s", e, k)
				}
			}
		}
		t.Logf("%s: %d símbolos cubiertos, %d statements, leak %s",
			e, len(cov[e]), r.Rec.Count(), r.Leak)
	}
}
