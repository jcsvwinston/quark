package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// detectGoModule reads <dir>/go.mod and derives project.module and
// project.name for the generated config. Falling back to the old
// github.com/user/myapp placeholder only when there is no readable module
// line — an initialized Go project should never see its own config point at
// somebody else's module path.
func detectGoModule(dir string) (module, name string) {
	module, name = "github.com/user/myapp", "myapp"

	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return module, name
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		mod := strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "module")), `"`)
		if mod == "" {
			return module, name
		}
		module = mod
		if i := strings.LastIndex(mod, "/"); i >= 0 {
			name = mod[i+1:]
		} else {
			name = mod
		}
		return module, name
	}
	return module, name
}

var (
	initDir     string
	initDialect string
)

func init() {
	initCmd.Flags().StringVar(&initDir, "dir", ".", "Base directory for initialization")
	initCmd.Flags().StringVar(&initDialect, "dialect", "postgresql", "Default database dialect (postgresql|postgres|mysql|mariadb|sqlite|mssql|sqlserver|oracle)")
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
		moduleName, projectName := detectGoModule(initDir)
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
	// message without ever writing it. init knows the module path, so it
	// scaffolds the runner plus the package stubs that make it compile on
	// day one (the migrations/ and seeders/ dirs start empty).
	moduleName, projectName := detectGoModule(initDir)
	if err := writeRunnerScaffold(initDir, moduleName, projectName); err != nil {
		return err
	}

	color.Green("\nQuark project initialized successfully!")
	return nil
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
