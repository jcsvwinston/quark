//go:build superapp_cli

// Exerciser del binario cmd/quark contra SQLite real (build tag `superapp_cli`,
// no corre en `go test` normal). Cubre TODOS los comandos top-level y sus
// subcomandos, con un gate de reconciliación al final: si algún comando del
// inventario queda sin ejercer y no está en la allowlist, el test falla.
//
// El binario se compila una vez en TestMain. La config del CLI va por env
// (viper AutomaticEnv, prefijo QUARK): QUARK_DATABASE_DEFAULT_{DRIVER,DSN}.
//
//	go test -tags=superapp_cli -run TestCLICoverage -v ./examples/superapp/cli/
package cli

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// testBin es la ruta del binario cmd/quark compilado una vez por TestMain.
var testBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "quark-cli-*")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(dir, "quark")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/jcsvwinston/quark/cmd/quark").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build cmd/quark: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	testBin = bin
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// runCLI ejecuta el binario y devuelve (salida combinada, exit-code).
func runCLI(t *testing.T, workdir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(testBin, args...)
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

// seedRichSchema crea un SQLite con dos tablas pobladas; la segunda ejerce la
// amplitud del mapeo SQL→Go del generador de modelos (int PK, text not-null,
// int/real/bool/timestamp nullable, json).
func seedRichSchema(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE TABLE cli_widgets (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score INTEGER)`,
		`INSERT INTO cli_widgets (name, score) VALUES ('alpha', 10), ('beta', 20)`,
		`CREATE TABLE cli_accounts (
			id         INTEGER PRIMARY KEY,
			email      TEXT NOT NULL,
			age        INTEGER,
			balance    REAL,
			active     BOOLEAN,
			created_at TIMESTAMP,
			meta       JSON
		)`,
		`INSERT INTO cli_accounts (email, age) VALUES ('a@x.io', 30)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed exec %q: %v", s, err)
		}
	}
}

// assertGoBuilds escribe un go.mod stdlib-only en dir y compila ./... — prueba
// que el código generado (modelos database-first) es Go válido.
func assertGoBuilds(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tmpgen\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("el paquete generado NO compila: %v\n%s", err, out)
	}
}

func TestCLICoverage(t *testing.T) {
	work := t.TempDir()
	dbPath := filepath.Join(work, "app.db")
	seedRichSchema(t, dbPath)
	dbEnv := []string{
		"QUARK_DATABASE_DEFAULT_DRIVER=sqlite",
		"QUARK_DATABASE_DEFAULT_DSN=" + dbPath,
	}

	covered := map[string]bool{}
	mark := func(cmds ...string) {
		for _, c := range cmds {
			covered[c] = true
		}
	}
	// okExit aserta exit 0 y devuelve la salida para checks adicionales.
	okExit := func(t *testing.T, out string, code int, label string) {
		t.Helper()
		if code != 0 {
			t.Errorf("%s: exit=%d\n%s", label, code, out)
		}
	}

	t.Run("help", func(t *testing.T) {
		out, code := runCLI(t, work, nil, "--help")
		okExit(t, out, code, "--help")
		for _, w := range []string{"init", "gen", "model", "migrate", "sync", "seed", "inspect", "tenant", "validate"} {
			if !strings.Contains(out, w) {
				t.Errorf("--help no lista el comando %q\n%s", w, out)
			}
		}
		mark("help")
	})

	t.Run("init", func(t *testing.T) {
		dir := t.TempDir()
		out, code := runCLI(t, dir, nil, "init", "--dir", dir, "--dialect", "sqlite")
		okExit(t, out, code, "init")
		for _, sub := range []string{"models", "migrations", "seeders", ".quark.yml"} {
			if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
				t.Errorf("init no creó %q: %v", sub, err)
			}
		}
		cfg, _ := os.ReadFile(filepath.Join(dir, ".quark.yml"))
		if !strings.Contains(string(cfg), "sqlite") {
			t.Errorf(".quark.yml no refleja el dialect sqlite:\n%s", cfg)
		}
		mark("init")
	})

	t.Run("inspect", func(t *testing.T) {
		out, code := runCLI(t, work, dbEnv, "inspect", "schema")
		okExit(t, out, code, "inspect schema")
		for _, tbl := range []string{"cli_widgets", "cli_accounts"} {
			if !strings.Contains(out, tbl) {
				t.Errorf("inspect schema no listó %q\n%s", tbl, out)
			}
		}
		mark("inspect schema")

		out, code = runCLI(t, work, dbEnv, "inspect", "table", "cli_accounts")
		okExit(t, out, code, "inspect table")
		if !strings.Contains(out, "email") {
			t.Errorf("inspect table no mostró la columna email\n%s", out)
		}
		mark("inspect table")

		out, code = runCLI(t, work, dbEnv, "inspect", "sql", "--model", "cli_widgets")
		okExit(t, out, code, "inspect sql")
		if !strings.Contains(out, "CREATE TABLE") {
			t.Errorf("inspect sql no emitió un CREATE TABLE\n%s", out)
		}
		mark("inspect sql")
	})

	t.Run("validate", func(t *testing.T) {
		out, code := runCLI(t, work, dbEnv, "validate", "cli_accounts")
		okExit(t, out, code, "validate (existente)")
		if !strings.Contains(out, "email") {
			t.Errorf("validate no mostró columnas\n%s", out)
		}
		// camino negativo: tabla inexistente → exit != 0
		if out, code = runCLI(t, work, dbEnv, "validate", "ghost_xyz"); code == 0 {
			t.Errorf("validate de tabla inexistente debía salir !=0\n%s", out)
		}
		mark("validate")
	})

	t.Run("sync", func(t *testing.T) {
		out, code := runCLI(t, work, dbEnv, "sync")
		okExit(t, out, code, "sync")
		if !strings.Contains(strings.ToLower(out), "sync") {
			t.Errorf("sync no produjo la guía esperada\n%s", out)
		}
		// dry-run también
		_, code = runCLI(t, work, dbEnv, "sync", "--dry-run")
		okExit(t, "", code, "sync --dry-run")
		mark("sync")
	})

	t.Run("migrate", func(t *testing.T) {
		mwork := t.TempDir()
		out, code := runCLI(t, mwork, dbEnv, "migrate", "create", "cli_initial")
		okExit(t, out, code, "migrate create")
		entries, _ := os.ReadDir(filepath.Join(mwork, "migrations"))
		if len(entries) == 0 {
			t.Errorf("migrate create no generó fichero en %s/migrations", mwork)
		}
		mark("migrate create")

		// up/down/status/version contra una BD fresca. up/down son inertes para
		// migraciones creadas por el CLI (son ficheros Go, no compilados en el
		// binario), pero deben terminar con exit 0 y la tabla de tracking creada.
		freshDB := filepath.Join(t.TempDir(), "m.db")
		mEnv := []string{"QUARK_DATABASE_DEFAULT_DRIVER=sqlite", "QUARK_DATABASE_DEFAULT_DSN=" + freshDB}

		_, code = runCLI(t, mwork, mEnv, "migrate", "up")
		okExit(t, "", code, "migrate up")
		mark("migrate up")

		out, code = runCLI(t, mwork, mEnv, "migrate", "up", "--dry-run")
		okExit(t, out, code, "migrate up --dry-run")

		_, code = runCLI(t, mwork, mEnv, "migrate", "down")
		okExit(t, "", code, "migrate down")
		mark("migrate down")

		out, code = runCLI(t, mwork, mEnv, "migrate", "status")
		okExit(t, out, code, "migrate status")
		mark("migrate status")

		out, code = runCLI(t, mwork, mEnv, "migrate", "version")
		okExit(t, out, code, "migrate version")
		mark("migrate version")
	})

	t.Run("seed", func(t *testing.T) {
		swork := t.TempDir()
		out, code := runCLI(t, swork, dbEnv, "seed", "create", "users")
		okExit(t, out, code, "seed create")
		if _, err := os.Stat(filepath.Join(swork, "seeders", "users_seeder.go")); err != nil {
			t.Errorf("seed create no generó el fichero: %v", err)
		}
		mark("seed create")

		// El binario standalone no registra seeders → list/run informan y salen 0.
		out, code = runCLI(t, swork, dbEnv, "seed", "list")
		okExit(t, out, code, "seed list")
		mark("seed list")

		out, code = runCLI(t, swork, dbEnv, "seed", "run")
		okExit(t, out, code, "seed run")
		mark("seed run")
	})

	t.Run("tenant", func(t *testing.T) {
		// list sobre BD fresca: crea la tabla quark_tenants y reporta vacío.
		out, code := runCLI(t, work, dbEnv, "tenant", "list")
		okExit(t, out, code, "tenant list")
		mark("tenant list")

		out, code = runCLI(t, work, dbEnv, "tenant", "migrate", "acme")
		okExit(t, out, code, "tenant migrate")
		mark("tenant migrate")

		out, code = runCLI(t, work, dbEnv, "tenant", "migrate-all")
		okExit(t, out, code, "tenant migrate-all")
		mark("tenant migrate-all")
		// `tenant provision` queda en allowlist: requiere CREATE DATABASE/SCHEMA
		// y DSN admin — no aplica a SQLite (ver reconciliación).
	})

	t.Run("model generate --fields", func(t *testing.T) {
		outDir := filepath.Join(t.TempDir(), "models")
		// NB: a diferencia de --from-table, `model generate <Name> --fields` NO
		// hace MkdirAll del --out (generateFromDefinition lo omite); por eso lo
		// creamos aquí, replicando el flujo real (tras `quark init` el dir existe).
		// El bug del CLI (silent-fail + exit 0 si el dir falta) queda flagueado
		// aparte; el exerciser cubre la generación, no lo gatea todavía.
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Fatalf("mkdir outDir: %v", err)
		}
		out, code := runCLI(t, work, nil, "model", "generate", "Product",
			"--fields", "id:int64,name:string,price:float64", "--out", outDir, "--package", "models")
		okExit(t, out, code, "model generate --fields")
		if _, err := os.Stat(filepath.Join(outDir, "product.go")); err != nil {
			t.Errorf("model generate --fields no creó product.go: %v", err)
		}
		assertGoBuilds(t, outDir)
		mark("model generate")
	})

	// === EL FLUJO ESTELAR: database-first ===
	// Introspecciona la BD y genera modelos Go, que deben COMPILAR; luego corre
	// el codegen forward (`gen --dry-run`) sobre ellos.
	t.Run("database-first: model generate --from-table → compile → gen", func(t *testing.T) {
		outDir := filepath.Join(t.TempDir(), "models")
		out, code := runCLI(t, work, dbEnv, "model", "generate",
			"--from-table", "cli_widgets,cli_accounts",
			"--out", outDir, "--package", "models")
		okExit(t, out, code, "model generate --from-table")
		for _, tbl := range []string{"cli_widgets", "cli_accounts"} {
			if !strings.Contains(out, tbl) {
				t.Errorf("model gen no reportó la tabla %q\n%s", tbl, out)
			}
		}

		// Ficheros generados.
		for _, f := range []string{"cliwidgets.go", "cliaccounts.go"} {
			if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
				t.Errorf("no se generó %q: %v", f, err)
			}
		}

		// Contenido: el mapeo SQL→Go del esquema rico.
		acc, _ := os.ReadFile(filepath.Join(outDir, "cliaccounts.go"))
		accS := string(acc)
		for _, want := range []string{
			"package models",
			"type CliAccounts struct",
			`pk:"true"`,       // id es PK
			"*time.Time",      // created_at nullable → puntero a tiempo
			"json.RawMessage", // meta JSON
			`func (CliAccounts) TableName() string`,
			`return "cli_accounts"`,
		} {
			if !strings.Contains(accS, want) {
				t.Errorf("cliaccounts.go no contiene %q\n%s", want, accS)
			}
		}

		// PRUEBA FUERTE: los modelos generados son Go válido y compilan.
		assertGoBuilds(t, outDir)

		// Codegen forward sobre los modelos generados (db: tags presentes).
		genOut, code := runCLI(t, outDir, nil, "gen", "--dry-run", "./...")
		okExit(t, genOut, code, "gen --dry-run")
		if !strings.Contains(genOut, "quark_gen.go") {
			t.Errorf("gen --dry-run no emitió codegen sobre los modelos generados\n%s", genOut)
		}
		mark("model generate", "gen")
	})

	// === Reconciliación: gate de cobertura del CLI ===
	inventory := []string{
		"help", "init", "gen", "sync", "validate",
		"model generate",
		"migrate create", "migrate up", "migrate down", "migrate status", "migrate version",
		"seed create", "seed run", "seed list",
		"inspect schema", "inspect table", "inspect sql",
		"tenant provision", "tenant migrate", "tenant list", "tenant migrate-all",
	}
	allow := map[string]string{
		"tenant provision": "requiere CREATE DATABASE/SCHEMA + DSN admin (PG/MySQL); SQLite no lo soporta",
	}
	var missing, got []string
	for c := range covered {
		got = append(got, c)
	}
	for _, c := range inventory {
		if !covered[c] && allow[c] == "" {
			missing = append(missing, c)
		}
	}
	sort.Strings(got)
	sort.Strings(missing)
	t.Logf("CLI cubierto (%d): %v", len(got), got)
	t.Logf("CLI allowlist: %v", allow)
	if len(missing) > 0 {
		t.Errorf("comandos del inventario SIN cubrir y fuera de allowlist: %v", missing)
	}
}
