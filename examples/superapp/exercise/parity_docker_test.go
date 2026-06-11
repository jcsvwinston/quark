//go:build superapp_engine

// El oráculo de paridad EN CROSS-ENGINE de verdad: SQLite (in-process) y
// Postgres (docker-run) corren las mismas sondas sobre el mismo dataset y sus
// payloads canónicos deben coincidir byte a byte. Cuando S7 encienda el resto
// de motores, basta añadirlos al slice de engines.
//
//	go test -tags=superapp_engine -run TestParityDocker -v ./examples/superapp/exercise/
package exercise

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestParityDockerSQLiteVsPostgres(t *testing.T) {
	// OJO S7: al añadir Oracle al slice, subir este timeout — su boot frío
	// tiene readyTimeout de 300s y excedería los 3 minutos totales.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	engines := []control.Engine{control.SQLite, control.Postgres}
	conns, err := engine.Up(ctx, engines...)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer func() {
		engine.Down(engines...)
		_ = os.Remove(conns[control.SQLite].DSN)
	}()

	payloads, errs := RunParity(conns, 4)
	for e, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", e, err)
		}
	}
	if len(payloads) != 2 {
		t.Fatalf("esperaba payloads de 2 motores, got %d", len(payloads))
	}

	divs := CompareParity(payloads)
	for _, d := range divs {
		t.Errorf("DIVERGENCIA: %s", d)
	}
	if len(divs) == 0 {
		t.Logf("paridad sqlite↔postgres: %d sondas idénticas", len(payloads[control.SQLite]))
	}
}
