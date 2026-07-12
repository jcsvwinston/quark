package commands

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

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
		config := map[string]interface{}{
			"project": map[string]string{
				"name":   "myapp",
				"module": "github.com/user/myapp",
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
