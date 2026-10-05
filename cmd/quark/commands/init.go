package commands

import (
	"bufio"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"golang.org/x/mod/module"
	"gopkg.in/yaml.v3"

	clidb "github.com/jcsvwinston/quark/cmd/quark/internal/db"
)

// moduleLineFrom parses the `module <path>` line out of a go.mod file's
// contents, or "" if there is none.
func moduleLineFrom(gomod []byte) string {
	scanner := bufio.NewScanner(strings.NewReader(string(gomod)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module")), `"`)
	}
	return ""
}

// resolveModule finds the module that <dir> belongs to by walking up looking
// for a go.mod, and returns the import path OF dir itself (the module path
// extended by dir's relative position under the module root) and the go.mod
// it found. found is false when dir is not inside any Go module — the caller
// then scaffolds a go.mod so the generated runner's imports resolve instead
// of pointing at a placeholder module that matches nothing (QC-1).
func resolveModule(dir string) (modulePath, projectName, goMod string, found bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", "", false
	}
	cur := abs
	for {
		if data, err := os.ReadFile(filepath.Join(cur, "go.mod")); err == nil {
			mod := moduleLineFrom(data)
			if mod == "" {
				return "", "", "", false
			}
			rel, err := filepath.Rel(cur, abs)
			if err != nil {
				return "", "", "", false
			}
			modulePath = mod
			if rel != "." {
				modulePath = mod + "/" + filepath.ToSlash(rel)
			}
			return modulePath, moduleBaseName(modulePath), filepath.Join(cur, "go.mod"), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", "", false // reached the filesystem root
		}
		cur = parent
	}
}

// moduleBaseName is the last path element of a module path — the conventional
// project/binary name.
func moduleBaseName(module string) string {
	if i := strings.LastIndex(module, "/"); i >= 0 {
		return module[i+1:]
	}
	return module
}

// moduleNameChars keeps a derived module name to a safe token; anything else
// collapses to the fallback below.
var moduleNameChars = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// deriveModuleName picks a module path for a scaffolded go.mod when the user
// did not pass --module: the sanitized base name of the target directory, or
// "myapp" when that yields nothing usable. It is deliberately a bare name (no
// domain) so the user can rename it before publishing; the scaffold stays
// coherent either way because the runner imports whatever this resolves to.
func deriveModuleName(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "myapp"
	}
	base := moduleNameChars.ReplaceAllString(filepath.Base(abs), "")
	base = strings.Trim(base, ".-")
	if base == "" {
		return "myapp"
	}
	return base
}

// ensureGoModule guarantees dir is inside a Go module, creating a minimal
// go.mod when it is not, and returns the import path of dir, the go.mod's
// path, and whether it scaffolded the file. Without a module the runner
// 'quark init' writes cannot build and `go run ./cmd/<app>` fails instantly —
// the QC-1 defect.
func ensureGoModule(dir string) (modulePath, projectName, goMod string, created bool, err error) {
	if mod, name, goMod, found := resolveModule(dir); found {
		return mod, name, goMod, false, nil
	}

	module := initModule
	if module == "" {
		module = deriveModuleName(dir)
	}
	goVersion := strings.TrimPrefix(runtime.Version(), "go")
	// runtime.Version() is like "go1.26.6"; go.mod wants "1.26".
	if parts := strings.SplitN(goVersion, ".", 3); len(parts) >= 2 {
		goVersion = parts[0] + "." + parts[1]
	}
	content := fmt.Sprintf("module %s\n\ngo %s\n", module, goVersion)
	goMod = filepath.Join(dir, "go.mod")
	if err := os.WriteFile(goMod, []byte(content), 0o644); err != nil {
		return "", "", "", false, fmt.Errorf("creating go.mod: %w", err)
	}
	fmt.Printf("  Created go.mod (module %s)\n", module)
	return module, moduleBaseName(module), goMod, true, nil
}

var (
	initDir     string
	initDialect string
	initModule  string
	initWith    []string
)

// initWithTargets lists what --with knows how to write, one per run: the
// package of the frameworks guide's section for that framework, copied from
// the fixture CI tests (see integrations in init_with.go), with the shared
// notes model, and a server main for chi, Echo, Gin and gRPC — Nucleus
// mounts its module from the host's main and gets the nucleus.yml it boots
// from instead. The extension bench reads this literal (INT-06).
var initWithTargets = []string{"chi", "echo", "gin", "grpc", "nucleus"}

func init() {
	initCmd.Flags().StringVar(&initDir, "dir", ".", "Base directory for initialization")
	initCmd.Flags().StringVar(&initDialect, "dialect", "postgresql", "Default database dialect (postgresql|postgres|mysql|mariadb|sqlite|mssql|sqlserver|oracle)")
	initCmd.Flags().StringVar(&initModule, "module", "", "Module path for a scaffolded go.mod when the directory is not already inside a Go module (default: the directory name)")
	initCmd.Flags().StringSliceVar(&initWith, "with", nil, "Also write the integration with one framework (chi|echo|gin|grpc|nucleus): internal/<app> serving a notes API on the Quark client, internal/notes, and a go.mod that requires the framework")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use: "init",
	Example: `  quark init --dialect postgresql
  quark init --dialect sqlite --with chi
  quark init --dialect sqlite --with nucleus`,
	Short:         "Initialize a new Quark project",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runInit()
	},
}

func runInit() error {
	// Validate the dialect BEFORE writing anything: `--dialect bogus` used to
	// write a config with an unknown driver and an empty DSN, exit 0, and
	// blow up later at `quark migrate up` (QK-P2-5). The accepted names are
	// the ones getDSNPlaceholder/DriverName understand.
	switch initDialect {
	case "postgresql", "postgres", "mysql", "mariadb", "sqlite", "mssql", "sqlserver", "oracle":
	default:
		return fmt.Errorf("unknown dialect %q: expected one of postgresql|postgres|mysql|mariadb|sqlite|mssql|sqlserver|oracle", initDialect)
	}
	// Same rule for --with: an unknown target fails before anything is
	// written, naming what is accepted. One framework per project: two
	// would write two packages into internal/<app> and two server mains.
	var with *integration
	for _, target := range initWith {
		in := integrationFor(strings.TrimSpace(target))
		if in == nil {
			return fmt.Errorf("unknown --with target %q: expected one of %s", target, strings.Join(initWithTargets, "|"))
		}
		if with != nil && with != in {
			return fmt.Errorf("--with takes one framework, got %s and %s", with.target, in.target)
		}
		with = in
	}

	fmt.Printf("Initializing Quark project in %s...\n", initDir)

	if err := os.MkdirAll(initDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", initDir, err)
	}

	// Guarantee a Go module BEFORE scaffolding the runner and config: without
	// one, the runner's imports point at a placeholder module that matches
	// nothing and `go run ./cmd/<app>` fails instantly (QC-1). This resolves
	// (or creates) the module path so everything downstream lines up.
	moduleName, projectName, goMod, createdGoMod, err := ensureGoModule(initDir)
	if err != nil {
		return err
	}

	// The integration is rendered before anything else is written, so a
	// template that fails leaves no half-scaffolded project behind.
	var scaffolded scaffold
	var integrationFiles []file
	if with != nil {
		scaffolded = scaffold{in: with, module: moduleName, project: projectName, pkg: packageIdent(projectName), dialect: initDialect}
		if integrationFiles, err = scaffolded.files(); err != nil {
			return err
		}
	}

	// Create directories
	dirs := []string{
		"models",
		"migrations",
		"seeders",
	}

	for _, d := range dirs {
		path := filepath.Join(initDir, d)
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", path, err)
		}
		fmt.Printf("  Created %s/\n", d)
	}

	// Create .quark.yml
	configPath := filepath.Join(initDir, ".quark.yml")
	if _, err := os.Stat(configPath); err == nil {
		color.Yellow("Warning: .quark.yml already exists. Skipping.")
	} else {
		config := map[string]interface{}{
			"project": map[string]string{
				"name":   projectName,
				"module": moduleName,
			},
			"database": map[string]interface{}{
				"default": map[string]string{
					"driver": initDialect,
					"dsn":    getDSNPlaceholder(initDialect),
				},
				"pool": map[string]interface{}{
					"max_open":     25,
					"max_idle":     5,
					"max_lifetime": "5m",
				},
			},
			"paths": map[string]string{
				"models":     "./models",
				"migrations": "./migrations",
				"seeders":    "./seeders",
			},
			"generation": map[string]interface{}{
				"dialect": initDialect,
				"package": "models",
				"naming": map[string]string{
					"table": "snake_case",
					"field": "snake_case",
				},
				"tags": []string{"json"},
				"features": map[string]bool{
					"soft_delete": true,
					"timestamps":  true,
					"json_tags":   true,
				},
			},
		}

		data, _ := yaml.Marshal(config)
		if err := os.WriteFile(configPath, data, 0644); err != nil {
			return fmt.Errorf("creating .quark.yml: %w", err)
		}
		fmt.Println("  Created .quark.yml")
	}

	// DX-10: close the CLI cycle. The standalone quark binary cannot see
	// project migrations/seeders (they register via init()), so every
	// project needs a small runner — the CLI used to dictate it in an error
	// message without ever writing it. init knows the module path (resolved
	// or scaffolded above), so it writes the runner plus the package stubs
	// that make it compile on day one (the migrations/ and seeders/ dirs
	// start empty).
	if err := writeRunnerScaffold(initDir, moduleName, projectName); err != nil {
		return err
	}

	// --with <framework>: the code of the frameworks guide's section for it,
	// copied from the fixture CI compiles and tests, and the go.mod
	// requirements it needs. Quark carries no framework dependency
	// (QADR-0001/0006), so the CLI writes the code as text; what it writes
	// is built, vetted and tested in a project of its own by
	// TestInitWithBuilds. For Nucleus, main is the host application's: init
	// writes the module and the nucleus.yml the printed mount line reads,
	// and the whole application around it is `nucleus new <app> --with quark`.
	var steps *scaffold
	if with != nil {
		if err := writeIntegration(initDir, integrationFiles); err != nil {
			return err
		}
		if with.target == "nucleus" {
			if err := writeNucleusConfig(initDir, initDialect); err != nil {
				return err
			}
		}
		pinned, unpinned := suiteRequirements()
		added, kept, err := requireModules(goMod, append(append([]module.Version{}, with.requires...), pinned...))
		if err != nil {
			return fmt.Errorf("adding the requirements of --with %s to %s: %w", with.target, goMod, err)
		}
		if len(added) > 0 {
			fmt.Printf("  Required in go.mod: %s\n", moduleList(added))
		}
		if len(kept) > 0 {
			fmt.Printf("  Kept in go.mod at the version it already required: %s\n", moduleList(kept))
		}
		if len(unpinned) > 0 {
			fmt.Printf("  Left to go mod tidy: %s (this quark is a development build, with no release of its own to pin)\n", strings.Join(unpinned, " and "))
		}
		steps = &scaffolded
	}

	color.Green("\nQuark project initialized.")
	printInitNextSteps(projectName, createdGoMod, steps)
	return nil
}

// printInitNextSteps prints the commands that actually work from here, in
// order. The old success message just said "initialized successfully!" and
// the runner's own banner told the user to `go run ./cmd/<app> migrate up` —
// a command that fails instantly until the quark dependency is fetched (and,
// before this change, until a go.mod even existed). QC-1: say the true next
// steps.
//
// with is the integration --with wrote (nil when there was none). Its
// go.mod requirements are already written, so the dependencies are one
// `go mod tidy`; the steps then say how to serve it — the server main for
// chi, Echo, Gin and gRPC, the mount line and the full-app path on the
// Nucleus side for Nucleus, so the reader knows which generator owns main.go.
func printInitNextSteps(projectName string, createdGoMod bool, with *scaffold) {
	fmt.Println("\nNext steps:")
	step := 1
	if createdGoMod {
		fmt.Printf("  %d. Edit go.mod if you want a different module path.\n", step)
		step++
	}
	if with == nil {
		fmt.Printf("  %d. go get github.com/jcsvwinston/quark@latest   # add the runtime dependency\n", step)
		step++
		// The scaffolded runner imports this CLI's command tree, which is a
		// module of its own since ADR-0024 — so it is a second `go get`, and
		// leaving it out makes the runner fail to build on the first try.
		fmt.Printf("  %d. go get github.com/jcsvwinston/quark/cmd/quark@latest # the command tree cmd/%s/main.go embeds\n", step, projectName)
		step++
	} else {
		fmt.Printf("  %d. go mod tidy   # fetches what go.mod requires, and the %s driver module\n", step, initDialect)
		step++
		in, pkg := with.in, with.pkg
		switch in.server {
		case serveHTTP, serveGRPC:
			fmt.Printf("  %d. go run ./cmd/%s-server   # serves internal/%s with %s on %s; QUARK_DATABASE_DEFAULT_DSN picks the database\n", step, projectName, pkg, in.framework, in.addr)
			step++
		default:
			fmt.Printf("  %d. In main: client, err := quark.New(%q, dsn) and nucleus.New().FromConfigFile(\"nucleus.yml\").Mount(%s.Module(client))\n", step, clidb.DriverName(initDialect), pkg)
			fmt.Printf("     nucleus.yml is written here and names the .quark.yml database; edit both if you change it.\n")
			step++
			fmt.Printf("     For the whole application generated around this module (config, policy file, admin panel):\n")
			fmt.Printf("       nucleus new %s --with quark\n", projectName)
		}
	}
	fmt.Printf("  %d. quark migrate create initial_schema --from-models ./models --dialect %s\n", step, initDialect)
	step++
	fmt.Printf("  %d. go run ./cmd/%s migrate up                   # run it through YOUR runner, not the standalone binary\n", step, projectName)
}

// packageIdentChars strips everything a Go package name cannot carry.
var packageIdentChars = regexp.MustCompile(`[^a-z0-9_]+`)

// takenPackageNames are names internal/<app> cannot take: a keyword-free
// identifier still cannot be main (that is a command), notes (the model's
// package, which it imports), or the name of a package the server main
// imports beside it.
var takenPackageNames = map[string]bool{
	"main": true, "notes": true, "notespb": true, "quark": true, "nucleus": true,
	"context": true, "errors": true, "log": true, "net": true, "http": true,
	"os": true, "signal": true, "syscall": true, "time": true,
}

// packageIdent turns a project name ("my-shop.api") into a Go package name
// ("myshopapi"): lowercase, identifier characters only, never empty, never
// starting with a digit, and never a keyword or a name the scaffold's own
// files need ("app" is appended: "quark" becomes "quarkapp").
func packageIdent(name string) string {
	id := packageIdentChars.ReplaceAllString(strings.ToLower(name), "")
	id = strings.TrimLeft(id, "0123456789_")
	if id == "" {
		return "app"
	}
	if token.IsKeyword(id) || takenPackageNames[id] {
		return id + "app"
	}
	return id
}

// driverModuleFor maps a dialect name `quark init` accepts to the driver
// module the emitted code blank-imports (drivers/<engine>).
func driverModuleFor(dialect string) string {
	switch dialect {
	case "postgresql", "postgres":
		return "drivers/postgres"
	case "mysql", "mariadb":
		return "drivers/mysql"
	case "mssql", "sqlserver":
		return "drivers/mssql"
	case "oracle":
		return "drivers/oracle"
	default:
		return "drivers/" + dialect
	}
}

// writeNucleusConfig writes nucleus.yml, the minimum
// nucleus.New().FromConfigFile("nucleus.yml") needs to boot, next to
// .quark.yml and pointing at the same database. The printed next step names
// the file, so init has to write it: before this the recipe compiled and
// died at boot with "open nucleus.yml: no such file or directory". An
// existing file is never overwritten (`nucleus new` writes a fuller one).
func writeNucleusConfig(dir, dialect string) error {
	path := filepath.Join(dir, "nucleus.yml")
	if _, err := os.Stat(path); err == nil {
		color.Yellow("Warning: nucleus.yml already exists. Skipping.")
		return nil
	}
	content := fmt.Sprintf(`# Written by 'quark init --with nucleus': the minimum
# nucleus.New().FromConfigFile("nucleus.yml") needs to boot. The database is
# the one .quark.yml names, so the host and the Quark client open the same
# one; change both together. 'nucleus new <app> --with quark' writes the
# full file (CORS, CSRF, RBAC policy file, rate limits) — see the Nucleus
# configuration reference for every key.
database_default: default
databases:
  default:
    url: %s
host: 127.0.0.1
port: 8080
env: development
log_level: info
`, nucleusDatabaseURL(dialect))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("creating nucleus.yml: %w", err)
	}
	fmt.Println("  Created nucleus.yml (the host configuration FromConfigFile reads; same database as .quark.yml)")
	return nil
}

// nucleusDatabaseURL maps a dialect `quark init` accepts to the database URL
// Nucleus reads from nucleus.yml (databases.<alias>.url). Nucleus takes a URL
// and derives the driver from its scheme (mysql:// becomes the tcp() DSN,
// sqlite:// a path), so the form differs from getDSNPlaceholder; the database
// it names is the same one, so the host and the Quark client main builds open
// one database.
func nucleusDatabaseURL(dialect string) string {
	switch dialect {
	case "postgresql", "postgres":
		return "postgres://user:pass@localhost/myapp?sslmode=disable"
	case "mysql", "mariadb":
		return "mysql://user:pass@localhost:3306/myapp"
	case "sqlite":
		return "sqlite://myapp.db"
	case "mssql", "sqlserver":
		return "sqlserver://user:pass@localhost:1433?database=myapp"
	case "oracle":
		return "oracle://user:pass@localhost:1521/xe"
	default:
		return ""
	}
}

func getDSNPlaceholder(dialect string) string {
	switch dialect {
	case "postgresql", "postgres":
		return "postgres://user:pass@localhost/myapp?sslmode=disable"
	case "mysql", "mariadb":
		return "user:pass@tcp(localhost:3306)/myapp?parseTime=true"
	case "sqlite":
		return "myapp.db"
	case "mssql", "sqlserver":
		return "sqlserver://user:pass@localhost:1433?database=myapp"
	case "oracle":
		// go-ora URL form; the legacy user/pass@host:port/service form is a
		// godror-ism and go-ora (the driver this CLI links) rejects it.
		return "oracle://user:pass@localhost:1521/xe"
	default:
		return ""
	}
}

// writeRunnerScaffold writes cmd/<name>/main.go — the project's embedded
// migration/seed runner — and the doc.go stubs for migrations/ and seeders/
// so the runner's blank imports compile before the first migrate create.
// Existing files are never overwritten.
func writeRunnerScaffold(dir, moduleName, projectName string) error {
	stub := func(pkg, purpose string) string {
		return fmt.Sprintf(`// Package %s registers this project's %s via init() side
// effects. Files scaffolded by 'quark migrate create' / 'quark seed create'
// land here; the runner in cmd/ imports this package so they compile into
// the binary that runs them.
package %s
`, pkg, purpose, pkg)
	}
	for pkg, purpose := range map[string]string{"migrations": "versioned migrations", "seeders": "seeders"} {
		path := filepath.Join(dir, pkg, "doc.go")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(stub(pkg, purpose)), 0o644); err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		fmt.Printf("  Created %s/doc.go\n", pkg)
	}

	runnerDir := filepath.Join(dir, "cmd", projectName)
	runnerPath := filepath.Join(runnerDir, "main.go")
	if _, err := os.Stat(runnerPath); err == nil {
		return nil
	}
	if err := os.MkdirAll(runnerDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", runnerDir, err)
	}
	runner := fmt.Sprintf(`// Command %s is this project's migration and seed runner, scaffolded by
// 'quark init'. The standalone quark binary cannot see the migrations and
// seeders registered below (they register through init() side effects), so
// migrate/seed/tenant commands run through THIS binary:
//
//	go run ./cmd/%s migrate up
//	go run ./cmd/%s seed run
//
// The command tree comes from github.com/jcsvwinston/quark/cmd/quark, which
// is its own module: add it with
// 'go get github.com/jcsvwinston/quark/cmd/quark@latest'.
//
// commands.Main prints errors to stderr and exits non-zero on failure.
package main

import (
	_ "%s/migrations" // side-effect: registers migrations
	_ "%s/seeders"    // side-effect: registers seeders

	"github.com/jcsvwinston/quark/cmd/quark/commands"
)

func main() { commands.Main() }
`, projectName, projectName, projectName, moduleName, moduleName)
	if err := os.WriteFile(runnerPath, []byte(runner), 0o644); err != nil {
		return fmt.Errorf("creating %s: %w", runnerPath, err)
	}
	fmt.Printf("  Created cmd/%s/main.go (migration/seed runner — run it with: go run ./cmd/%s migrate up)\n", projectName, projectName)
	return nil
}
