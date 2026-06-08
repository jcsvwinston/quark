//go:build superapp_cli

// Smoke del binario cmd/quark contra SQLite real. Build-tagged `superapp_cli`
// (no corre en `go test` normal). Prueba el MECANISMO de cobertura del CLI:
// compila el binario una vez y ejerce un subconjunto representativo de comandos,
// asertando exit-code + salida. No necesita contenedores (SQLite en fichero).
//
//	go test -tags=superapp_cli -run TestCLISmoke -v ./examples/superapp/cli/
package cli

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// allTopLevel es el inventario de comandos top-level del CLI (denominador del
// smoke). El manifiesto-de-comandos real (enumerado de cobra) es S9 propio.
var allTopLevel = []string{
	"help", "init", "gen", "model", "migrate", "sync", "seed", "inspect", "tenant", "validate",
}

// buildCLI compila cmd/quark una vez y devuelve la ruta del binario.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "quark-cli")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/jcsvwinston/quark/cmd/quark").CombinedOutput()
	if err != nil {
		t.Fatalf("build cmd/quark: %v\n%s", err, out)
	}
	return bin
}

// seedSQLite crea un fichero SQLite con una tabla poblada, para que inspect/
// validate tengan algo real que introspeccionar.
func seedSQLite(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE cli_widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score INTEGER)`,
		`INSERT INTO cli_widgets (name, score) VALUES ('alpha', 10)`,
		`INSERT INTO cli_widgets (name, score) VALUES ('beta', 20)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed exec %q: %v", s, err)
		}
	}
}

// runCLI ejecuta el binario y devuelve (salida combinada, exit-code).
func runCLI(t *testing.T, bin, workdir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("run %v: %v\n%s", args, err, out)
	return "", -1
}

func TestCLISmoke(t *testing.T) {
	bin := buildCLI(t)
	work := t.TempDir()
	dbPath := filepath.Join(work, "app.db")
	seedSQLite(t, dbPath)

	// Config del CLI por env (viper AutomaticEnv, prefijo QUARK, '.'→'_').
	dbEnv := []string{
		"QUARK_DATABASE_DEFAULT_DRIVER=sqlite",
		"QUARK_DATABASE_DEFAULT_DSN=" + dbPath,
	}

	covered := map[string]bool{}

	// 1. `quark --help` — cobra, sin BD. Debe listar los comandos.
	out, code := runCLI(t, bin, work, nil, "--help")
	if code != 0 {
		t.Errorf("--help exit=%d\n%s", code, out)
	}
	for _, w := range []string{"migrate", "inspect", "validate", "tenant", "seed"} {
		if !strings.Contains(out, w) {
			t.Errorf("--help no menciona el comando %q\n%s", w, out)
		}
	}
	covered["help"] = true

	// 2. `quark migrate --help` — subcomandos.
	out, code = runCLI(t, bin, work, nil, "migrate", "--help")
	if code != 0 {
		t.Errorf("migrate --help exit=%d\n%s", code, out)
	}
	for _, w := range []string{"up", "down", "status", "create"} {
		if !strings.Contains(out, w) {
			t.Errorf("migrate --help no menciona el subcomando %q\n%s", w, out)
		}
	}

	// 3. `quark inspect schema` (BD poblada) → lista cli_widgets.
	out, code = runCLI(t, bin, work, dbEnv, "inspect", "schema")
	if code != 0 {
		t.Errorf("inspect schema exit=%d\n%s", code, out)
	}
	if !strings.Contains(out, "cli_widgets") {
		t.Errorf("inspect schema no listó la tabla cli_widgets\n%s", out)
	}
	covered["inspect"] = true

	// 4. `quark validate cli_widgets` → muestra las columnas.
	out, code = runCLI(t, bin, work, dbEnv, "validate", "cli_widgets")
	if code != 0 {
		t.Errorf("validate cli_widgets exit=%d\n%s", code, out)
	}
	if !strings.Contains(out, "name") {
		t.Errorf("validate no mostró la columna 'name'\n%s", out)
	}
	covered["validate"] = true

	// 5. Camino negativo: `quark validate <tabla inexistente>` → exit != 0.
	if out, code = runCLI(t, bin, work, dbEnv, "validate", "ghost_table_xyz"); code == 0 {
		t.Errorf("validate de tabla inexistente debía salir !=0; salió 0\n%s", out)
	}

	// 6. `quark migrate status` (BD fresca, sin tabla de migraciones) → exit 0.
	if out, code = runCLI(t, bin, work, dbEnv, "migrate", "status"); code != 0 {
		t.Errorf("migrate status exit=%d\n%s", code, out)
	}

	// 7. `quark migrate create` — generador de ficheros (escribe en ./migrations
	// del cwd; no necesita BD).
	if out, code = runCLI(t, bin, work, dbEnv, "migrate", "create", "cli_smoke_initial"); code != 0 {
		t.Errorf("migrate create exit=%d\n%s", code, out)
	}
	entries, _ := os.ReadDir(filepath.Join(work, "migrations"))
	if len(entries) == 0 {
		t.Errorf("migrate create no generó ningún fichero en %s/migrations", work)
	}
	covered["migrate"] = true

	// Anti "silent cap": loguea explícitamente qué top-level commands NO cubre
	// este smoke. Cerrarlos es S9 propio (manifiesto de comandos + golden).
	var got, missing []string
	for c := range covered {
		got = append(got, c)
	}
	for _, c := range allTopLevel {
		if !covered[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(got)
	sort.Strings(missing)
	t.Logf("CLI smoke cubre top-level: %v", got)
	t.Logf("CLI NO cubierto aún (→ S9 full: manifiesto de comandos + golden output): %v", missing)
}
