//go:build superapp_engine

// Validación del camino docker-run del runner (build tag superapp_engine; no
// corre en `go test` normal). Arranca Postgres vía docker run (o reusa
// SUPERAPP_DSN_POSTGRES), migra el dominio del superapp, y verifica que tras
// cerrar el Client no haya fugas de pool ni de goroutines.
//
//	go test -tags=superapp_engine -run TestEngineDocker -v ./examples/superapp/engine/
package engine

import (
	"context"
	"testing"
	"time"

	"github.com/jcsvwinston/quark/examples/superapp/control"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestEngineDockerPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	engines := []control.Engine{control.Postgres}
	conns, err := Up(ctx, engines...)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer Down(engines...)

	// tol 4: pgx/database/sql puede dejar alguna goroutine residual frente a la
	// línea base; el pool sí debe quedar a 0 estrictamente.
	results := Run(conns, 4, newTestClient, exercise(ctx))
	for e, r := range results {
		if r.Err != nil {
			t.Errorf("%s exercise: %v", e, r.Err)
			continue
		}
		t.Logf("leak — %s", r.Leak)
		if r.Leak.PoolLeaked() {
			t.Errorf("%s: fuga de pool tras Close (inUse=%d open=%d)", e, r.Leak.PoolInUse, r.Leak.PoolOpen)
		}
		if r.Leak.GoroutinesLeaked() {
			t.Errorf("%s: fuga de goroutines %d→%d (tol %d)", e, r.Leak.GoroutinesBefore, r.Leak.GoroutinesAfter, r.Leak.Tolerance)
		}
	}
}
