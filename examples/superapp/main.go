// Command superapp ejerce TODA la superficie pública de Quark contra los motores
// seleccionados y DEMUESTRA la cobertura reconciliándola contra el manifiesto
// generado (apisurface.json, el denominador). Emite la matriz método×motor a
// REPORTS/ y gatea: falla si algún assert funcional es rojo (o hay una fuga) y
// —en modo estricto— si queda un símbolo in-scope sin cubrir fuera de la
// allowlist.
//
//	# un motor (por defecto, gate off → sólo falla con asserts rojos)
//	go run ./examples/superapp -engines=sqlite
//
//	# los 6 con gate estricto (Oracle requiere su contenedor o SUPERAPP_DSN_ORACLE)
//	go run ./examples/superapp -engines=all -gate=strict
//
// SQLite corre in-process; el resto se levanta por `docker run` (o se reusa vía
// SUPERAPP_DSN_<ENGINE>); ver engine.Up. La matriz va a REPORTS/superapp-<stamp>/
// (gitignored). Invócalo desde la raíz del repo, o pasa -manifest/-allowlist.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"
	"github.com/jcsvwinston/quark/examples/superapp/exercise"

	// Drivers SQL: el binario consumidor los registra (engine.Up sólo entrega
	// driver+DSN; quien hace sql.Open es quark.New dentro de exercise.Run).
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

// healthRow es una fila sintética de la matriz (no es un símbolo): resume, por
// motor, si su corrida terminó sin error funcional ni fuga. El prefijo "!!"
// la ordena la primera (sort byte-wise: '!' < cualquier letra del import path).
const healthRow = "!! engine-run (health: funcional + fugas)"

// errGateFailed señala que la corrida fue OK pero el gate dio veredicto negativo.
// run() ya imprimió la línea "✗ GATE …"; main() sólo traduce a exit 1 sin volver a
// imprimir (a diferencia de un error operativo, que sí lleva el prefijo "superapp:").
var errGateFailed = errors.New("gate failed")

func main() {
	err := run()
	if err == nil {
		return
	}
	if !errors.Is(err, errGateFailed) {
		fmt.Fprintln(os.Stderr, "superapp: "+err.Error())
	}
	os.Exit(1)
}

// run hace todo el trabajo y devuelve error en vez de salir, para que el defer del
// teardown de motores se ejecute en TODOS los caminos (incluido un fallo al escribir
// la matriz tras levantar contenedores — si no, quedan huérfanos).
func run() error {
	var (
		enginesFlag  = flag.String("engines", "sqlite", "motores a ejercer: lista por comas (sqlite,postgres,…) o 'all'")
		gateFlag     = flag.String("gate", "off", "modo gate: 'strict' falla si hay símbolos in-scope sin cubrir; 'off' sólo con asserts rojos")
		outFlag      = flag.String("out", "", "directorio de salida; vacío usa REPORTS/superapp-<stamp>")
		manifestFlag = flag.String("manifest", filepath.Join("examples", "superapp", "apisurface.json"), "ruta de apisurface.json (relativa a la cwd)")
		allowFlag    = flag.String("allowlist", filepath.Join("examples", "superapp", "allowlist.json"), "ruta de allowlist.json (relativa a la cwd)")
		keep         = flag.Bool("keep", false, "no hacer teardown de los contenedores al salir (para depurar)")
	)
	flag.Parse()

	engines, err := parseEngines(*enginesFlag)
	if err != nil {
		return err
	}
	strict := strings.EqualFold(strings.TrimSpace(*gateFlag), "strict")

	// 1. Manifiesto + allowlist primero: si faltan, no tiene sentido levantar
	//    motores. La allowlist ausente NO es fatal (LoadAllowlist da una vacía).
	manifest, err := control.LoadManifest(*manifestFlag)
	if err != nil {
		return fmt.Errorf("%w\n  (¿corres desde la raíz del repo? usa -manifest=<ruta a apisurface.json>)", err)
	}
	allow, err := control.LoadAllowlist(*allowFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aviso: %v — se ejerce sin allowlist\n", err)
	}

	// 2. Levanta los motores. SQLite in-process; el resto docker-run o
	//    SUPERAPP_DSN_<ENGINE>. Teardown al salir salvo -keep.
	ctx := context.Background()
	fmt.Printf("▶ levantando motor(es): %s\n", joinEngines(engines))
	conns, err := engine.Up(ctx, engines...)
	if err != nil {
		engine.Down(engines...) // limpia lo que sí arrancó
		return fmt.Errorf("levantando motores: %w", err)
	}
	if !*keep {
		defer engine.Down(engines...)
	}

	// 3. Corre los exercisers por motor (recorder + cobertura + anti-fugas).
	//    tol: los drivers con servidor dejan goroutines residuales; SQLite no. Es
	//    un único int para todos los motores corridos — la deuda de pasarlo a
	//    map[Engine]int (para no esconder una fuga de SQLite tras la tolerancia de
	//    pgx en una corrida mixta) está anotada en HANDOFF.md como follow-up de S4/S7.
	tol := 2
	for _, e := range engines {
		if e != control.SQLite {
			tol = 4
			break
		}
	}
	exercisers := exercise.AllExercisers()
	fmt.Printf("▶ ejerciendo %d áreas × %d motor(es)…\n", len(exercisers), len(conns))
	results := exercise.Run(conns, tol, exercisers)

	// 4. Reconcilia la cobertura contra el manifiesto y construye la matriz.
	inv := exercise.Coverage(results)
	report := buildReport(manifest, inv, allow, results, engines)

	// 5. Emite la matriz a REPORTS/ + un resumen máquina-legible.
	stamp := time.Now().Format("20060102-150405")
	outDir := *outFlag
	if outDir == "" {
		outDir = filepath.Join("examples", "superapp", "REPORTS", "superapp-"+stamp)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creando -out: %w", err)
	}
	matrixPath := filepath.Join(outDir, "matrix.txt")
	if err := writeMatrix(matrixPath, manifest, report, engines, strict); err != nil {
		return fmt.Errorf("escribiendo matriz: %w", err)
	}
	summaryPath := filepath.Join(outDir, "summary.json")
	if err := writeSummary(summaryPath, manifest, allow, inv, results, engines, strict); err != nil {
		return fmt.Errorf("escribiendo resumen: %w", err)
	}

	// 6. Resumen a stdout + veredicto del gate.
	printSummary(manifest, allow, inv, results, engines, matrixPath)
	if err := report.Gate(strict, allow); err != nil {
		fmt.Printf("\n✗ GATE %s: %v\n", gateMode(strict), err)
		return errGateFailed
	}
	fmt.Printf("\n✓ GATE %s: superficie in-scope cubierta y sin asserts rojos en %s\n", gateMode(strict), joinEngines(engines))
	return nil
}

// buildReport pliega cobertura + salud por motor en una matriz método×motor.
// Tres clases de celda: PASS (símbolo del manifiesto invocado), MISSING (símbolo
// in-scope no invocado — de Manifest.Reconcile, sólo para los motores corridos),
// y FAIL en la fila de salud (error funcional o fuga del motor). Las claves
// invocadas que NO están en el manifiesto (typos de los key-helpers) no son PASS:
// se cuentan aparte en el resumen, y su consecuencia —el símbolo real queda sin
// marcar— ya aflora como MISSING.
func buildReport(m *control.Manifest, inv control.Invoked, allow control.Allowlist, results map[control.Engine]exercise.EngineResult, engines []control.Engine) *control.Report {
	manifestKeys := make(map[string]bool, len(m.Symbols))
	for _, s := range m.Symbols {
		manifestKeys[s.Key()] = true
	}
	selected := make(map[control.Engine]bool, len(engines))
	for _, e := range engines {
		selected[e] = true
	}

	report := &control.Report{}

	// Fila de salud: PASS si el motor terminó limpio, FAIL si hubo error o fuga.
	// Hace que el Gate cuente los fallos funcionales, que no son celdas de método.
	for _, e := range engines {
		res := results[e]
		switch {
		case res.Err != nil:
			report.Add(healthRow, e, control.StatusFailed, firstLine(res.Err.Error()))
		case !res.Leak.OK():
			report.Add(healthRow, e, control.StatusFailed, "fuga: "+res.Leak.String())
		default:
			report.Add(healthRow, e, control.StatusPassed, "")
		}
	}

	// PASS por cada símbolo del manifiesto invocado en un motor seleccionado.
	for e, seen := range inv {
		if !selected[e] {
			continue
		}
		for key := range seen {
			if manifestKeys[key] {
				report.Add(key, e, control.StatusPassed, "")
			}
		}
	}

	// MISSING (in-scope, no allowlisted). Reconcile recorre los 6 motores; nos
	// quedamos sólo con los corridos para no marcar como gap un motor no pedido.
	for _, c := range m.Reconcile(inv, allow) {
		if selected[c.Engine] {
			report.Add(c.Method, c.Engine, c.Status, c.Detail)
		}
	}

	return report
}

// writeMatrix escribe la matriz método×motor renderizada por control.Report, con
// una cabecera que explica los estados y la semántica de los motores no corridos.
func writeMatrix(path string, m *control.Manifest, r *control.Report, engines []control.Engine, strict bool) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// El fichero es un artefacto de CI (S7): un Close fallido (p.ej. disco lleno
	// al flushear) no debe quedar silenciado o el diagnóstico es difícil.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	fmt.Fprintln(f, "# Superapp — matriz de cobertura método × motor")
	fmt.Fprintf(f, "# manifiesto: %d símbolos · motores corridos: %s · gate: %s\n", len(m.Symbols), joinEngines(engines), gateMode(strict))
	fmt.Fprintln(f, "# Estados: PASS=invocado · MISSING=in-scope sin invocar · FAIL=assert/fuga rojo.")
	fmt.Fprintln(f, "# Los motores fuera de -engines aparecen MISSING en todas las filas y NO entran en el gate.")
	fmt.Fprintln(f, "# La primera fila '!! engine-run' es la salud por motor, no un símbolo.")
	fmt.Fprintln(f)
	r.Render(f)
	return nil
}

// engineSummary es la vista máquina-legible por motor (para CI / S7). Cada
// símbolo del manifiesto cae en exactamente una de tres clases: Covered (invocado),
// Missing (no invocado y no allowlisted — lo que el gate estricto cuenta), o
// Allowlisted (no invocado pero justificado fuera de scope). Covered+Missing+
// Allowlisted == Total.
type engineSummary struct {
	Engine      control.Engine `json:"engine"`
	Covered     int            `json:"covered"`
	Total       int            `json:"total"`
	Missing     int            `json:"missing"`     // gating: no invocado y no allowlisted
	Allowlisted int            `json:"allowlisted"` // no invocado pero allowlisted
	Stray       int            `json:"stray"`       // claves invocadas fuera del manifiesto (typos de key-helper)
	LeakOK      bool           `json:"leak_ok"`
	Functo      string         `json:"functional_error,omitempty"`
	StrayKeys   []string       `json:"stray_keys,omitempty"`
}

// writeSummary vuelca el resumen por motor a JSON (cobertura, fugas, errores).
func writeSummary(path string, m *control.Manifest, allow control.Allowlist, inv control.Invoked, results map[control.Engine]exercise.EngineResult, engines []control.Engine, strict bool) error {
	out := struct {
		GeneratedAt string          `json:"generated_at"`
		Total       int             `json:"total_symbols"`
		Strict      bool            `json:"strict"`
		Engines     []engineSummary `json:"engines"`
	}{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Total:       len(m.Symbols),
		Strict:      strict,
		Engines:     make([]engineSummary, 0, len(engines)),
	}
	for _, e := range engines {
		out.Engines = append(out.Engines, perEngine(e, m, allow, inv[e], results[e]))
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// perEngine clasifica cada símbolo del manifiesto para un motor: covered /
// gating-missing / allowlisted (suman Total), más las claves stray (invocadas
// pero ausentes del manifiesto — typos de key-helper que no cuentan como
// cobertura). Coincide con lo que el gate cuenta: Missing excluye allowlisted.
func perEngine(e control.Engine, m *control.Manifest, allow control.Allowlist, seen map[string]bool, res exercise.EngineResult) engineSummary {
	es := engineSummary{Engine: e, Total: len(m.Symbols), LeakOK: res.Leak.OK()}
	inManifest := make(map[string]bool, len(m.Symbols))
	for _, s := range m.Symbols {
		key := s.Key()
		inManifest[key] = true
		switch {
		case seen[key]:
			es.Covered++
		case allow.Has(key):
			es.Allowlisted++
		default:
			es.Missing++
		}
	}
	for key := range seen {
		if !inManifest[key] {
			es.Stray++
			es.StrayKeys = append(es.StrayKeys, key)
		}
	}
	if res.Err != nil {
		es.Functo = firstLine(res.Err.Error())
	}
	return es
}

// printSummary imprime el resumen por motor en stdout (compacto; la matriz
// completa va al fichero).
func printSummary(m *control.Manifest, allow control.Allowlist, inv control.Invoked, results map[control.Engine]exercise.EngineResult, engines []control.Engine, matrixPath string) {
	total := len(m.Symbols)
	fmt.Printf("\n— resumen (%d símbolos en el manifiesto) —\n", total)
	for _, e := range engines {
		es := perEngine(e, m, allow, inv[e], results[e])
		verdict := "OK"
		switch {
		case es.Functo != "":
			verdict = "FALLO: " + es.Functo
		case !es.LeakOK:
			verdict = "FUGA: " + results[e].Leak.String()
		}
		fmt.Printf("  %-9s %d/%d cubiertos · %d sin cubrir · %d allowlisted · %s\n", e, es.Covered, total, es.Missing, es.Allowlisted, verdict)
		if es.Stray > 0 {
			fmt.Printf("            ⚠ %d clave(s) invocada(s) fuera del manifiesto (typo de key-helper): %s\n", es.Stray, preview(es.StrayKeys, 5))
		}
	}
	fmt.Printf("\n  matriz: %s\n", matrixPath)
}

// parseEngines resuelve el flag -engines: 'all' → los 6; o una lista por comas
// validada contra los motores conocidos (dedupe, orden de aparición).
func parseEngines(s string) ([]control.Engine, error) {
	if strings.EqualFold(strings.TrimSpace(s), "all") {
		return control.AllEngines(), nil
	}
	valid := make(map[control.Engine]bool, 6)
	for _, e := range control.AllEngines() {
		valid[e] = true
	}
	var out []control.Engine
	seen := map[control.Engine]bool{}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" {
			continue
		}
		e := control.Engine(tok)
		if !valid[e] {
			return nil, fmt.Errorf("motor desconocido %q (válidos: %s, o 'all')", tok, joinEngines(control.AllEngines()))
		}
		if !seen[e] {
			out = append(out, e)
			seen[e] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-engines vacío (usa p.ej. -engines=sqlite o -engines=all)")
	}
	return out, nil
}

func joinEngines(engines []control.Engine) string {
	parts := make([]string, len(engines))
	for i, e := range engines {
		parts[i] = string(e)
	}
	return strings.Join(parts, ",")
}

func gateMode(strict bool) string {
	if strict {
		return "estricto"
	}
	return "off"
}

// firstLine recorta un error multilínea a su primera línea para el resumen.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// preview muestra hasta n elementos de una lista, con "…+k" si hay más.
func preview(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return fmt.Sprintf("%s …+%d", strings.Join(xs[:n], ", "), len(xs)-n)
}
