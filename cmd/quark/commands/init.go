package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
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
// extended by dir's relative position under the module root). found is false
// when dir is not inside any Go module — the caller then scaffolds a go.mod so
// the generated runner's imports resolve instead of pointing at a placeholder
// module that matches nothing (QC-1).
func resolveModule(dir string) (modulePath, projectName string, found bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", false
	}
	cur := abs
	for {
		if data, err := os.ReadFile(filepath.Join(cur, "go.mod")); err == nil {
			mod := moduleLineFrom(data)
			if mod == "" {
				return "", "", false
			}
			rel, err := filepath.Rel(cur, abs)
			if err != nil {
				return "", "", false
			}
			modulePath = mod
			if rel != "." {
				modulePath = mod + "/" + filepath.ToSlash(rel)
			}
			return modulePath, moduleBaseName(modulePath), true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", "", false // reached the filesystem root
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
// go.mod when it is not, and returns the import path of dir plus whether it
// scaffolded the file. Without a module the runner 'quark init' writes cannot
// build and `go run ./cmd/<app>` fails instantly — the QC-1 defect.
func ensureGoModule(dir string) (modulePath, projectName string, created bool, err error) {
	if mod, name, found := resolveModule(dir); found {
		return mod, name, false, nil
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
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		return "", "", false, fmt.Errorf("creating go.mod: %w", err)
	}
	fmt.Printf("  Created go.mod (module %s)\n", module)
	return module, moduleBaseName(module), true, nil
}

var (
	initDir     string
	initDialect string
	initModule  string
)

func init() {
	initCmd.Flags().StringVar(&initDir, "dir", ".", "Base directory for initialization")
	initCmd.Flags().StringVar(&initDialect, "dialect", "postgresql", "Default database dialect (postgresql|postgres|mysql|mariadb|sqlite|mssql|sqlserver|oracle)")
	initCmd.Flags().StringVar(&initModule, "module", "", "Module path for a scaffolded go.mod when the directory is not already inside a Go module (default: the directory name)")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:           "init",
	Example:       `  quark init --dialect postgresql`,
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

	fmt.Printf("Initializing Quark project in %s...\n", initDir)

	if err := os.MkdirAll(initDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", initDir, err)
	}

	// Guarantee a Go module BEFORE scaffolding the runner and config: without
	// one, the runner's imports point at a placeholder module that matches
	// nothing and `go run ./cmd/<app>` fails instantly (QC-1). This resolves
	// (or creates) the module path so everything downstream lines up.
	moduleName, projectName, createdGoMod, err := ensureGoModule(initDir)
	if err != nil {
		return err
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

	color.Green("\nQuark project initialized.")
	printInitNextSteps(projectName, createdGoMod)
	return nil
}

// printInitNextSteps prints the commands that actually work from here, in
// order. The old success message just said "initialized successfully!" and
// the runner's own banner told the user to `go run ./cmd/<app> migrate up` —
// a command that fails instantly until the quark dependency is fetched (and,
// before this change, until a go.mod even existed). QC-1: say the true next
// steps.
func printInitNextSteps(projectName string, createdGoMod bool) {
	fmt.Println("\nNext steps:")
	step := 1
	if createdGoMod {
		fmt.Printf("  %d. Edit go.mod if you want a different module path.\n", step)
		step++
	}
	fmt.Printf("  %d. go get github.com/jcsvwinston/quark@latest   # add the runtime dependency\n", step)
	step++
	fmt.Printf("  %d. quark migrate create initial_schema --from-models ./models --dialect %s\n", step, initDialect)
	step++
	fmt.Printf("  %d. go run ./cmd/%s migrate up                   # run it through YOUR runner, not the standalone binary\n", step, projectName)
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
