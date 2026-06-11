package exercise

import (
	"os"
	"testing"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"

	_ "modernc.org/sqlite"
)

// TestParitySQLite valida la maquinaria del oráculo con un solo motor:
// payload completo (todas las sondas), DETERMINISTA (dos runs → idéntico
// byte a byte; si esto falla, el oráculo daría falsos positivos entre
// motores), y CompareParity sin divergencias contra sí mismo.
func TestParitySQLite(t *testing.T) {
	run := func() ParityPayload {
		t.Helper()
		conns, err := engine.Up(t.Context(), control.SQLite)
		if err != nil {
			t.Fatalf("Up: %v", err)
		}
		defer func() {
			engine.Down(control.SQLite)
			_ = os.Remove(conns[control.SQLite].DSN)
		}()
		payloads, errs := RunParity(conns, 2)
		if err := errs[control.SQLite]; err != nil {
			t.Fatalf("RunParity: %v", err)
		}
		return payloads[control.SQLite]
	}

	p1 := run()
	if len(p1) != len(parityProbes) {
		t.Fatalf("payload con %d sondas, esperaba %d", len(p1), len(parityProbes))
	}
	// "null" es el JSON de un valor nil (json.Marshal(nil)), no el literal Go.
	for name, v := range p1 {
		if v == "" || v == "null" {
			t.Errorf("sonda %s con payload vacío: %q", name, v)
		}
	}

	// Determinismo: un segundo run desde cero produce el MISMO payload.
	p2 := run()
	for name := range p1 {
		if p1[name] != p2[name] {
			t.Errorf("sonda %s no determinista:\n  run1: %s\n  run2: %s", name, p1[name], p2[name])
		}
	}

	// Un motor contra sí mismo no diverge (sanity del comparador) y el
	// comparador detecta una divergencia inyectada.
	if divs := CompareParity(map[control.Engine]ParityPayload{control.SQLite: p1, "sqlite-bis": p2}); len(divs) != 0 {
		t.Errorf("divergencias inesperadas: %v", divs)
	}
	mutated := ParityPayload{}
	for k, v := range p2 {
		mutated[k] = v
	}
	mutated["aggregates"] = `{"sum":"0.000000"}`
	divs := CompareParity(map[control.Engine]ParityPayload{control.SQLite: p1, "mutado": mutated})
	if len(divs) != 1 || divs[0].Probe != "aggregates" {
		t.Errorf("esperaba exactamente la divergencia inyectada en aggregates, got %v", divs)
	}
}
