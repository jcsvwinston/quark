package main

import (
	"errors"
	"testing"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"
	"github.com/jcsvwinston/quark/examples/superapp/exercise"
)

// sym construye un símbolo del manifiesto con la key "pkg.<name>".
func sym(name string) control.Symbol { return control.Symbol{Pkg: "pkg", Name: name, Kind: "method"} }

func manifestOf(names ...string) *control.Manifest {
	m := &control.Manifest{}
	for _, n := range names {
		m.Symbols = append(m.Symbols, sym(n))
	}
	return m
}

// cellIndex indexa las celdas por (método, motor) para aserciones. Verifica de
// paso que buildReport no emite duplicados (cada par aparece una sola vez).
func cellIndex(t *testing.T, cells []control.Cell) map[[2]string]control.Status {
	t.Helper()
	idx := map[[2]string]control.Status{}
	for _, c := range cells {
		k := [2]string{c.Method, string(c.Engine)}
		if _, dup := idx[k]; dup {
			t.Fatalf("celda duplicada para %v", k)
		}
		idx[k] = c.Status
	}
	return idx
}

func TestParseEngines(t *testing.T) {
	all := control.AllEngines()
	tests := []struct {
		in      string
		want    []control.Engine
		wantErr bool
	}{
		{"all", all, false},
		{"ALL", all, false},
		{"sqlite", []control.Engine{control.SQLite}, false},
		{"sqlite,postgres", []control.Engine{control.SQLite, control.Postgres}, false},
		{" sqlite , postgres ", []control.Engine{control.SQLite, control.Postgres}, false},
		{"sqlite,sqlite", []control.Engine{control.SQLite}, false}, // dedupe
		{"sqlite,nope", nil, true},
		{"", nil, true},
		{",", nil, true},
	}
	for _, tc := range tests {
		got, err := parseEngines(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseEngines(%q): esperaba error, got %v", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseEngines(%q): %v", tc.in, err)
			continue
		}
		if joinEngines(got) != joinEngines(tc.want) {
			t.Errorf("parseEngines(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestBuildReport_PartitionAndGate cubre la lógica central: PASS por símbolo
// invocado, MISSING por símbolo in-scope sin invocar (filtrado a los motores
// corridos), el símbolo allowlisted que NO genera MISSING, la fila de salud que
// marca FAIL el motor con error, y que el gate cuenta ese FAIL aunque no sea
// estricto.
func TestBuildReport_PartitionAndGate(t *testing.T) {
	m := manifestOf("A", "B", "C")
	allow := control.Allowlist{Reasons: map[string]string{"pkg.C": "fuera de scope a propósito"}}
	engines := []control.Engine{control.SQLite, control.Postgres}

	inv := control.Invoked{
		control.SQLite:   {"pkg.A": true}, // A invocado en sqlite
		control.Postgres: {"pkg.A": true}, // A invocado en postgres (antes de fallar)
	}
	results := map[control.Engine]exercise.EngineResult{
		control.SQLite:   {}, // zero-value: sin error, leak OK
		control.Postgres: {Err: errors.New("boom")},
	}

	report := buildReport(m, inv, allow, results, engines)
	idx := cellIndex(t, report.Cells)

	want := map[[2]string]control.Status{
		{healthRow, "sqlite"}:   control.StatusPassed,
		{healthRow, "postgres"}: control.StatusFailed,
		{"pkg.A", "sqlite"}:     control.StatusPassed,
		{"pkg.A", "postgres"}:   control.StatusPassed,
		{"pkg.B", "sqlite"}:     control.StatusMissing,
		{"pkg.B", "postgres"}:   control.StatusMissing,
	}
	for k, st := range want {
		if got := idx[k]; got != st {
			t.Errorf("celda %v = %q, want %q", k, got, st)
		}
	}
	// C está allowlisted → no debe emitir MISSING en ningún motor.
	for _, e := range []string{"sqlite", "postgres"} {
		if st, ok := idx[[2]string{"pkg.C", e}]; ok {
			t.Errorf("pkg.C en %s no debería tener celda (allowlisted), got %q", e, st)
		}
	}
	// Motores no seleccionados no deben aparecer.
	for _, e := range []string{"mysql", "mariadb", "mssql", "oracle"} {
		if st, ok := idx[[2]string{"pkg.A", e}]; ok {
			t.Errorf("motor %s no seleccionado no debería tener celdas, got %q", e, st)
		}
	}

	// Gate: el FAIL de postgres hace fallar el gate incluso en modo no estricto.
	if err := report.Gate(false, allow); err == nil {
		t.Error("Gate(false) debería fallar por el FAIL de postgres")
	}
	if err := report.Gate(true, allow); err == nil {
		t.Error("Gate(true) debería fallar (FAIL + MISSING)")
	}
}

// TestBuildReport_AllCovered: con todo cubierto y sin errores, el gate pasa en
// ambos modos.
func TestBuildReport_AllCovered(t *testing.T) {
	m := manifestOf("A")
	engines := []control.Engine{control.SQLite}
	inv := control.Invoked{control.SQLite: {"pkg.A": true}}
	results := map[control.Engine]exercise.EngineResult{control.SQLite: {}}

	report := buildReport(m, inv, control.Allowlist{}, results, engines)
	idx := cellIndex(t, report.Cells)
	if idx[[2]string{"pkg.A", "sqlite"}] != control.StatusPassed {
		t.Errorf("pkg.A/sqlite = %q, want PASS", idx[[2]string{"pkg.A", "sqlite"}])
	}
	if idx[[2]string{healthRow, "sqlite"}] != control.StatusPassed {
		t.Errorf("health/sqlite = %q, want PASS", idx[[2]string{healthRow, "sqlite"}])
	}
	if err := report.Gate(true, control.Allowlist{}); err != nil {
		t.Errorf("Gate(true) debería pasar: %v", err)
	}
	if err := report.Gate(false, control.Allowlist{}); err != nil {
		t.Errorf("Gate(false) debería pasar: %v", err)
	}
}

// TestBuildReport_AllowlistedButInvoked: un símbolo allowlisted que SÍ se invoca
// recibe PASS (el loop de cobertura no filtra por allowlist) y no genera MISSING,
// así que el gate estricto pasa.
func TestBuildReport_AllowlistedButInvoked(t *testing.T) {
	m := manifestOf("A")
	allow := control.Allowlist{Reasons: map[string]string{"pkg.A": "fuera de scope pero se ejerció igual"}}
	engines := []control.Engine{control.SQLite}
	inv := control.Invoked{control.SQLite: {"pkg.A": true}}
	results := map[control.Engine]exercise.EngineResult{control.SQLite: {}}

	report := buildReport(m, inv, allow, results, engines)
	idx := cellIndex(t, report.Cells)
	if got := idx[[2]string{"pkg.A", "sqlite"}]; got != control.StatusPassed {
		t.Errorf("pkg.A allowlisted+invocado = %q, want PASS", got)
	}
	if err := report.Gate(true, allow); err != nil {
		t.Errorf("Gate(true) debería pasar (ni FAIL ni MISSING): %v", err)
	}
}

// TestBuildReport_LeakFailsHealth: una fuga (pool abierto tras Close) marca FAIL
// la fila de salud aunque no haya error funcional.
func TestBuildReport_LeakFailsHealth(t *testing.T) {
	m := manifestOf("A")
	engines := []control.Engine{control.SQLite}
	inv := control.Invoked{control.SQLite: {"pkg.A": true}}
	results := map[control.Engine]exercise.EngineResult{
		control.SQLite: {Leak: engine.LeakReport{Engine: control.SQLite, PoolOpen: 1}},
	}
	report := buildReport(m, inv, control.Allowlist{}, results, engines)
	idx := cellIndex(t, report.Cells)
	if idx[[2]string{healthRow, "sqlite"}] != control.StatusFailed {
		t.Errorf("health/sqlite con fuga = %q, want FAIL", idx[[2]string{healthRow, "sqlite"}])
	}
	if err := report.Gate(false, control.Allowlist{}); err == nil {
		t.Error("Gate(false) debería fallar por la fuga")
	}
}

// TestPerEngine_Partition: covered + missing + allowlisted == total, y las claves
// invocadas fuera del manifiesto se cuentan como stray.
func TestPerEngine_Partition(t *testing.T) {
	m := manifestOf("A", "B", "C")
	allow := control.Allowlist{Reasons: map[string]string{"pkg.C": "x"}}
	seen := map[string]bool{"pkg.A": true, "pkg.ZZ": true} // A cubierto, ZZ es stray
	es := perEngine(control.SQLite, m, allow, seen, exercise.EngineResult{})

	if es.Covered != 1 || es.Missing != 1 || es.Allowlisted != 1 {
		t.Errorf("partición = covered %d missing %d allowlisted %d, want 1/1/1", es.Covered, es.Missing, es.Allowlisted)
	}
	if es.Covered+es.Missing+es.Allowlisted != es.Total {
		t.Errorf("covered+missing+allowlisted=%d != total=%d", es.Covered+es.Missing+es.Allowlisted, es.Total)
	}
	if es.Stray != 1 || len(es.StrayKeys) != 1 || es.StrayKeys[0] != "pkg.ZZ" {
		t.Errorf("stray = %d %v, want 1 [pkg.ZZ]", es.Stray, es.StrayKeys)
	}
}

func TestPreview(t *testing.T) {
	if got := preview([]string{"a", "b"}, 5); got != "a, b" {
		t.Errorf("preview corto = %q", got)
	}
	if got := preview([]string{"a", "b", "c"}, 2); got != "a, b …+1" {
		t.Errorf("preview truncado = %q", got)
	}
}
