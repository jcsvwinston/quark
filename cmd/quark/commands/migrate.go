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
	migrateMessage string
)

// errNoMigrationsRegistered is returned by up/down when this binary's
// migration registry is empty. Migrations register through init() side
// effects, so the installed standalone `quark` binary can never see the
// project's migration files — pretending "No pending migrations" there was
// the QK-P1-1 lie. Fail loudly with the embed recipe instead.
func errNoMigrationsRegistered(verb string) error {
	return fmt.Errorf(`no migrations are registered in this binary — cannot %s.

Migration files register themselves via init(), which only runs when their
package is compiled into the executing binary. The standalone 'quark' binary
does not (and cannot) import your project's migrations package.

Build a small runner that does, and drive it from there:

    // cmd/quark/main.go (in YOUR project)
    import (
        _ "github.com/you/yourapp/migrations" // side-effect: registers migrations
        "github.com/jcsvwinston/quark/cmd/quark/commands"
    )
    func main() { commands.Execute() }

See the CLI guide ("Embedding the same operations in your own binary")`, verb)
}

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

// The migrate subcommands are exactly what CI pipelines gate on, so a failure
// must surface as a non-zero exit: RunE hands the error to main.go, which
// prints it and exits 1. Cobra's own usage/error dump is silenced so that
// single line is the only output.
var migrateCreateCmd = &cobra.Command{
	Use:           "create <name>",
	Short:         "Create a new migration file",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateCreate(args[0])
	},
}

var migrateUpCmd = &cobra.Command{
	Use:           "up",
	Short:         "Apply pending migrations",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateUp()
	},
}

var migrateDownCmd = &cobra.Command{
	Use:           "down",
	Short:         "Revert the last migration",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateDown()
	},
}

var migrateStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Show migration status",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateStatus()
	},
}

var migrateVersionCmd = &cobra.Command{
	Use:           "version",
	Short:         "Show current migration version",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateVersion()
	},
}

func runMigrateCreate(name string) error {
	timestamp := time.Now().Format("20060102150405")
	filename := fmt.Sprintf("%s_%s.go", timestamp, name)
	dir := viper.GetString("paths.migrations")
	if dir == "" {
		dir = "./migrations"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating migrations directory: %w", err)
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
		return fmt.Errorf("creating migration file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("executing template: %w", err)
	}

	fmt.Printf("Created migration: %s\n", path)
	return nil
}

func runMigrateUp() error {
	if migrate.RegisteredCount() == 0 {
		return errNoMigrationsRegistered("apply")
	}
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	migrator := migrate.NewMigrator(client)
	ctx := context.Background()

	if migrateDryRun {
		color.Yellow("Dry-run mode: no migrations will be applied.")
		return migrator.UpDryRun(ctx, migrateSteps)
	}

	return migrator.Up(ctx, migrateSteps)
}

func runMigrateDown() error {
	if migrate.RegisteredCount() == 0 {
		return errNoMigrationsRegistered("revert")
	}
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	migrator := migrate.NewMigrator(client)
	if err := migrator.Down(context.Background(), migrateSteps); err != nil {
		return fmt.Errorf("reverting migrations: %w", err)
	}
	return nil
}

func runMigrateStatus() error {
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	migrator := migrate.NewMigrator(client)
	applied, err := migrator.GetApplied(context.Background())
	if err != nil {
		return fmt.Errorf("getting migration status: %w", err)
	}

	fmt.Println("Applied migrations:")
	for id := range applied {
		fmt.Printf("  [x] %s\n", id)
	}
	return nil
}

func runMigrateVersion() error {
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	migrator := migrate.NewMigrator(client)
	applied, err := migrator.GetApplied(context.Background())
	if err != nil {
		return fmt.Errorf("getting version: %w", err)
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
	return nil
}
