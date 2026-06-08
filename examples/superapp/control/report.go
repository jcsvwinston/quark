package control

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

// Status es el resultado de un par (método, motor).
type Status string

const (
	StatusPassed          Status = "PASS"     // invocado y las aserciones funcionales pasaron
	StatusSkippedExpected Status = "SKIP-EXP" // motor sin la capacidad; devolvió el error esperado
	StatusFailed          Status = "FAIL"     // invocado pero una aserción falló
	StatusMissing         Status = "MISSING"  // en el manifiesto, nunca invocado en este motor
)

// Cell es una entrada de la matriz método × motor.
type Cell struct {
	Method string
	Engine Engine
	Status Status
	Detail string
}

// Report acumula celdas y produce la matriz + el veredicto del gate.
type Report struct {
	Cells []Cell
}

// Add registra una celda.
func (r *Report) Add(method string, e Engine, s Status, detail string) {
	r.Cells = append(r.Cells, Cell{Method: method, Engine: e, Status: s, Detail: detail})
}

// AddAll agrega un lote de celdas (p.ej. la salida de Manifest.Reconcile).
func (r *Report) AddAll(cells []Cell) { r.Cells = append(r.Cells, cells...) }

// Render escribe la matriz método × motor en w.
func (r *Report) Render(w io.Writer) {
	grid := map[string]map[Engine]Status{}
	for _, c := range r.Cells {
		if grid[c.Method] == nil {
			grid[c.Method] = map[Engine]Status{}
		}
		// FAIL/MISSING ganan a PASS si una misma celda se reporta dos veces.
		if prev, ok := grid[c.Method][c.Engine]; !ok || worse(c.Status, prev) {
			grid[c.Method][c.Engine] = c.Status
		}
	}

	names := make([]string, 0, len(grid))
	for m := range grid {
		names = append(names, m)
	}
	sort.Strings(names)

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprint(tw, "MÉTODO")
	for _, e := range AllEngines() {
		fmt.Fprintf(tw, "\t%s", e)
	}
	fmt.Fprintln(tw)
	for _, m := range names {
		fmt.Fprint(tw, m)
		for _, e := range AllEngines() {
			st, ok := grid[m][e]
			if !ok {
				st = StatusMissing
			}
			fmt.Fprintf(tw, "\t%s", st)
		}
		fmt.Fprintln(tw)
	}
	_ = tw.Flush()
}

// severity ordena los estados para que el "peor" prevalezca en la matriz.
func severity(s Status) int {
	switch s {
	case StatusFailed:
		return 3
	case StatusMissing:
		return 2
	case StatusSkippedExpected:
		return 1
	default: // StatusPassed
		return 0
	}
}

func worse(a, b Status) bool { return severity(a) > severity(b) }

// Gate devuelve error si, en modo estricto, hay alguna celda FAIL o MISSING que
// no esté justificada en la allowlist. SkippedExpected y Passed nunca fallan.
func (r *Report) Gate(strict bool, allow Allowlist) error {
	var fails, missing int
	for _, c := range r.Cells {
		switch c.Status {
		case StatusFailed:
			fails++
		case StatusMissing:
			if !allow.Has(c.Method) {
				missing++
			}
		}
	}
	if fails > 0 || (strict && missing > 0) {
		return fmt.Errorf("gate: %d fallos, %d métodos sin cubrir fuera de allowlist", fails, missing)
	}
	return nil
}
