package workload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Artifacts son las rutas de los ficheros que produce una corrida.
type Artifacts struct {
	ReportPath  string
	MetricsPath string
	LogPath     string
}

// WriteArtifacts vuelca metrics.json y el informe ejecutivo (executive-report.md)
// a outDir. logPath es la ruta del log que el caller ya escribió (se enlaza en el
// informe). Devuelve las rutas resultantes.
func (r *Result) WriteArtifacts(outDir, logPath string) (Artifacts, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return Artifacts{}, fmt.Errorf("mkdir %q: %w", outDir, err)
	}
	art := Artifacts{
		ReportPath:  filepath.Join(outDir, "executive-report.md"),
		MetricsPath: filepath.Join(outDir, "metrics.json"),
		LogPath:     logPath,
	}

	mj, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return art, fmt.Errorf("marshal metrics: %w", err)
	}
	if err := os.WriteFile(art.MetricsPath, mj, 0o644); err != nil {
		return art, fmt.Errorf("write metrics.json: %w", err)
	}
	if err := os.WriteFile(art.ReportPath, []byte(r.ExecutiveReport(art)), 0o644); err != nil {
		return art, fmt.Errorf("write report: %w", err)
	}
	return art, nil
}

// ExecutiveReport renderiza el informe ejecutivo en Markdown.
func (r *Result) ExecutiveReport(art Artifacts) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("# Informe ejecutivo — Quark superapp · carga de alto volumen\n\n")
	w("> Arnés `examples/superapp/workload` ejerciendo el dominio (accounts → projects → tasks,\n")
	w("> tags m2m, memberships con PK compuesta, attachments binarios) a volumen, con caché\n")
	w("> integrada y captura de cada statement vía el recorder. Cifras de una sola corrida.\n\n")

	// --- Resumen ---
	w("## Resumen\n\n")
	w("| Métrica | Valor |\n|---|---|\n")
	w("| Motor | `%s` |\n", r.Engine)
	w("| Escala | ×%d |\n", r.Scale)
	w("| Inicio | %s |\n", r.StartedAt.Format(time.RFC3339))
	w("| Filas totales (activas) | **%s** |\n", commas(r.TotalRows))
	w("| Statements SQL | **%s** |\n", commas(int64(r.TotalStmts)))
	w("| Transacciones | %s |\n", commas(int64(r.Transactions)))
	w("| Errores | %d |\n", r.Errors)
	w("| Wall-clock total | %s |\n", dur(r.TotalDuration))
	w("| · seed | %s |\n", dur(r.SeedDuration))
	w("| · carga | %s |\n", dur(r.WorkDuration))
	w("| Throughput total (filas activas / wall-clock) | %s filas/s |\n", f0(r.RowsPerSec()))
	w("| Throughput SQL | %s queries/s |\n", f0(r.QueriesPerSec()))
	w("| Cache hit-rate | **%.2f%%** (%d/%d; 1er ciclo siempre miss) |\n", r.CacheHitRate()*100, r.CacheHits, r.CacheableReads)
	w("\n")

	// --- Volumen de datos ---
	w("## Volumen de datos relacionados\n\n")
	w("| Entidad | Filas activas |\n|---|---:|\n")
	for _, name := range sortedKeys(int64Keys(r.Volume)) {
		w("| %s | %s |\n", name, commas(r.Volume[name]))
	}
	w("| **Total** | **%s** |\n\n", commas(r.TotalRows))

	// --- Carga ejecutada ---
	w("## Carga ejecutada (operaciones de servicio)\n\n")
	w("| Categoría | Operaciones |\n|---|---:|\n")
	for _, cat := range sortedKeys(intKeys(r.OpsByCategory)) {
		w("| %s | %s |\n", cat, commas(int64(r.OpsByCategory[cat])))
	}
	w("\n")

	// --- Perfil SQL ---
	w("## Perfil SQL\n\n")
	w("| Vía | Statements |\n|---|---:|\n")
	for _, op := range sortedKeys(intKeys(r.StmtsByOp)) {
		w("| `%s` | %s |\n", op, commas(int64(r.StmtsByOp[op])))
	}
	w("| **Total** | **%s** |\n", commas(int64(r.TotalStmts)))
	w("\nFilas devueltas por **SELECT**: **%s**. Filas devueltas por `… RETURNING` "+
		"(INSERT/UPDATE/DELETE — p.ej. `CreateBatch` en SQLite/PG/MariaDB, que usa esa vía): "+
		"**%s**.\n\n", commas(r.RowsSelected), commas(r.RowsReturned))

	// --- Latencia ---
	w("## Latencia por vía de ejecución (ms)\n\n")
	w("| Vía | n | p50 | p95 | p99 | max |\n|---|---:|---:|---:|---:|---:|\n")
	order := []string{"exec", "query", "query_row", "overall"}
	seen := map[string]bool{}
	for _, op := range order {
		if s, ok := r.Latency[op]; ok {
			w("| `%s` | %s | %s | %s | %s | %s |\n", op, commas(int64(s.Count)), f2(s.P50), f2(s.P95), f2(s.P99), f2(s.Max))
			seen[op] = true
		}
	}
	for _, op := range sortedKeys(latKeys(r.Latency)) {
		if !seen[op] {
			s := r.Latency[op]
			w("| `%s` | %s | %s | %s | %s | %s |\n", op, commas(int64(s.Count)), f2(s.P50), f2(s.P95), f2(s.P99), f2(s.Max))
		}
	}
	w("\n")

	// --- Lectura ejecutiva ---
	w("## Lectura\n\n")
	w("- Se sembraron **%s filas relacionadas** y se ejercieron **%s statements SQL** en **%s** "+
		"(seed %s + carga %s), con **%d errores**.\n",
		commas(r.TotalRows), commas(int64(r.TotalStmts)), dur(r.TotalDuration), dur(r.SeedDuration), dur(r.WorkDuration), r.Errors)
	w("- La caché integrada resolvió **%.2f%%** de las lecturas cacheables sin tocar la BD "+
		"(%d de %d hits; el 1er ciclo es siempre un miss en frío) — esas operaciones emitieron 0 SQL.\n",
		r.CacheHitRate()*100, r.CacheHits, r.CacheableReads)
	if s, ok := r.Latency["overall"]; ok {
		w("- Latencia global por statement: p50 **%s ms**, p99 **%s ms**, max **%s ms** sobre %s statements.\n",
			f2(s.P50), f2(s.P99), f2(s.Max), commas(int64(s.Count)))
	}
	w("- Las **%d transacciones** multi-entidad confirmaron de forma atómica (account + projects).\n", r.Transactions)
	w("\n")

	// --- Artefactos ---
	w("## Artefactos para contrastar\n\n")
	w("- Métricas máquina-legibles: `%s`\n", filepath.Base(art.MetricsPath))
	if art.LogPath != "" {
		w("- Log de la aplicación (slog JSON, slow-query + fases): `%s`\n", filepath.Base(art.LogPath))
	}
	w("- Este informe: `%s`\n", filepath.Base(art.ReportPath))
	w("\n---\n_Generado por `examples/superapp/workload`. Las cifras provienen del recorder " +
		"(cada statement con su duración) y de `Count()` por entidad tras la corrida._\n")

	return b.String()
}

// --- helpers de formato ---

func dur(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func f0(v float64) string { return commas(int64(v + 0.5)) }
func f2(v float64) string { return fmt.Sprintf("%.2f", v) }

// commas formatea un entero con separadores de millar.
func commas(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		return "-" + out
	}
	return out
}

func sortedKeys(ks []string) []string { sort.Strings(ks); return ks }

func int64Keys(m map[string]int64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
func intKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
func latKeys(m map[string]LatencyStat) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
