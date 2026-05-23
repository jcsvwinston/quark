package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	migrateSteps   int
	migrateDryRun  bool
	migrateForce   bool
	migrateMessage string
)

func init() {
	migrateCmd.AddCommand(migrateCreateCmd)
	migrateCmd.AddCommand(migrateUpCmd)
	migrateCmd.AddCommand(migrateDownCmd)
	migrateCmd.AddCommand(migrateStatusCmd)
	migrateCmd.AddCommand(migrateVersionCmd)

	migrateCreateCmd.Flags().StringVar(&migrateMessage, "message", "", "Migration message")
	migrateUpCmd.Flags().IntVar(&migrateSteps, "steps", 0, "Number of migrations to apply")
	migrateUpCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "Preview SQL without executing")
	migrateDownCmd.Flags().IntVar(&migrateSteps, "steps", 1, "Number of migrations to revert")

	rootCmd.AddCommand(migrateCmd)
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Manage database migrations",
}

var migrateCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new migration file",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runMigrateCreate(args[0])
	},
}

var migrateUpCmd = &cobra.Command{
	Use:   "up",
	Short: "Apply pending migrations",
	Run: func(cmd *cobra.Command, args []string) {
		runMigrateUp()
	},
}

var migrateDownCmd = &cobra.Command{
	Use:   "down",
	Short: "Revert the last migration",
	Run: func(cmd *cobra.Command, args []string) {
		runMigrateDown()
	},
}

var migrateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show migration status",
	Run: func(cmd *cobra.Command, args []string) {
		runMigrateStatus()
	},
}

var migrateVersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show current migration version",
	Run: func(cmd *cobra.Command, args []string) {
		runMigrateVersion()
	},
}

func runMigrateCreate(name string) {
	timestamp := time.Now().Format("20060102150405")
	filename := fmt.Sprintf("%s_%s.go", timestamp, name)
	dir := viper.GetString("paths.migrations")
	if dir == "" {
		dir = "./migrations"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		color.Red("Error creating migrations directory: %v", err)
		return
	}

	path := filepath.Join(dir, filename)

	data := struct {
		ID      string
		Name    string
		Message string
	}{
		ID:      timestamp,
		Name:    name,
		Message: migrateMessage,
	}

	tmpl, _ := template.New("migration").Parse(migrationTemplate)
	file, err := os.Create(path)
	if err != nil {
		color.Red("Error creating migration file: %v", err)
		return
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		color.Red("Error executing template: %v", err)
		return
	}

	fmt.Printf("Created migration: %s\n", path)
}

func runMigrateUp() {
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}

	migrator := migrate.NewMigrator(client)
	ctx := context.Background()

	if migrateDryRun {
		color.Yellow("Dry-run mode: no migrations will be applied.")
		if err := migrator.UpDryRun(ctx, migrateSteps); err != nil {
			color.Red("Error: %v", err)
		}
		return
	}

	if err := migrator.Up(ctx, migrateSteps); err != nil {
		color.Red("Error applying migrations: %v", err)
		return
	}
}

func runMigrateDown() {
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}

	migrator := migrate.NewMigrator(client)
	if err := migrator.Down(context.Background(), migrateSteps); err != nil {
		color.Red("Error reverting migrations: %v", err)
		return
	}
}

func runMigrateStatus() {
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}

	migrator := migrate.NewMigrator(client)
	applied, err := migrator.GetApplied(context.Background())
	if err != nil {
		color.Red("Error getting migration status: %v", err)
		return
	}

	fmt.Println("Applied migrations:")
	for id := range applied {
		fmt.Printf("  [x] %s\n", id)
	}
}

func runMigrateVersion() {
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error: %v", err)
		return
	}

	migrator := migrate.NewMigrator(client)
	applied, err := migrator.GetApplied(context.Background())
	if err != nil {
		color.Red("Error getting version: %v", err)
		return
	}

	var latest string
	for id := range applied {
		if id > latest {
			latest = id
		}
	}

	if latest == "" {
		fmt.Println("No migrations applied.")
	} else {
		fmt.Printf("Current version: %s\n", latest)
	}
}
