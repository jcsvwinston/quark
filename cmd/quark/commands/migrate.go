package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// up and down need SEPARATE --steps variables: pflag writes each default into
// the bound variable at registration time, so sharing one variable let down's
// default (1) overwrite up's (0) and a bare `migrate up` applied only the
// first pending migration.
var (
	migrateUpSteps    int
	migrateDownSteps  int
	migrateDryRun     bool
	migrateMessage    string
	migrateFromModels string
	migrateDialect    string
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
    func main() { commands.Main() }

commands.Main prints errors to stderr and exits non-zero on failure. (A bare
commands.Execute() discards the returned error: every failure would exit 0
in silence.)

See the CLI guide ("Embedding the same operations in your own binary")`, verb)
}

func init() {
	migrateCmd.AddCommand(migrateCreateCmd)
	migrateCmd.AddCommand(migrateUpCmd)
	migrateCmd.AddCommand(migrateDownCmd)
	migrateCmd.AddCommand(migrateStatusCmd)
	migrateCmd.AddCommand(migrateVersionCmd)

	migrateCreateCmd.Flags().StringVar(&migrateMessage, "message", "", "Migration message")
	migrateCreateCmd.Flags().StringVar(&migrateFromModels, "from-models", "", "Render domain DDL from the model structs in this package (e.g. ./models)")
	migrateCreateCmd.Flags().StringVar(&migrateDialect, "dialect", "", "SQL dialect for --from-models (postgresql|postgres|mysql|mariadb|sqlite|mssql|oracle); defaults to the configured driver")
	migrateUpCmd.Flags().IntVar(&migrateUpSteps, "steps", 0, "Number of migrations to apply (0 = all pending)")
	migrateUpCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "Preview SQL without executing")
	migrateDownCmd.Flags().IntVar(&migrateDownSteps, "steps", 1, "Number of migrations to revert")

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
	Example:       `  quark migrate create add_users_table --message "users base table"`,
	Short:         "Create a new migration file",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateCreate(args[0])
	},
}

var migrateUpCmd = &cobra.Command{
	Use: "up",
	Example: `  quark migrate up --dry-run
  quark migrate up --steps 1`,
	Short:         "Apply pending migrations",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateUp()
	},
}

var migrateDownCmd = &cobra.Command{
	Use:           "down",
	Example:       `  quark migrate down --steps 1`,
	Short:         "Revert the last migration",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateDown()
	},
}

var migrateStatusCmd = &cobra.Command{
	Use:           "status",
	Example:       `  quark migrate status`,
	Short:         "Show migration status",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateStatus()
	},
}

var migrateVersionCmd = &cobra.Command{
	Use:           "version",
	Example:       `  quark migrate version`,
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

	// DX-18: with --from-models the Up/Down bodies carry rendered domain
	// DDL instead of the empty skeleton.
	upBody, downBody := "", ""
	if migrateFromModels != "" {
		dialect := normalizeDDLDialect(migrateDialect)
		if dialect == "" {
			dialect = normalizeDDLDialect(viper.GetString("database.default.driver"))
		}
		if dialect == "" {
			return fmt.Errorf("--from-models needs a dialect: pass --dialect or configure database.default.driver")
		}
		models, err := loadModelsForDDL(migrateFromModels)
		if err != nil {
			return err
		}
		up, down, err := buildDDLStatements(models, dialect)
		if err != nil {
			return err
		}
		upBody = renderExecBody(up)
		downBody = renderExecBody(down)
		fmt.Printf("Rendered DDL for %d model(s) from %s (dialect %s)\n", len(models), migrateFromModels, dialect)
	}

	data := struct {
		ID       string
		Name     string
		Message  string
		UpBody   string
		DownBody string
	}{
		ID:       timestamp,
		Name:     name,
		Message:  migrateMessage,
		UpBody:   upBody,
		DownBody: downBody,
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
		return migrator.UpDryRun(ctx, migrateUpSteps)
	}

	return migrator.Up(ctx, migrateUpSteps)
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
	if err := migrator.Down(context.Background(), migrateDownSteps); err != nil {
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
	ctx := context.Background()

	// Init is idempotent (dialect-aware IF NOT EXISTS); without it, status on
	// a fresh database surfaced a raw missing-table error instead of the
	// obvious truth: zero applied migrations.
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("initializing migration table: %w", err)
	}
	applied, err := migrator.GetApplied(ctx)
	if err != nil {
		return fmt.Errorf("getting migration status: %w", err)
	}

	appliedIDs := make([]string, 0, len(applied))
	for id := range applied {
		appliedIDs = append(appliedIDs, id)
	}
	sort.Strings(appliedIDs)

	fmt.Printf("Applied migrations (%d):\n", len(appliedIDs))
	for _, id := range appliedIDs {
		fmt.Printf("  [x] %s\n", id)
	}

	// Pending = registered in this binary but not applied. An empty registry
	// means this binary cannot know the pending set (standalone install) —
	// say so instead of implying "none pending".
	if migrate.RegisteredCount() == 0 {
		fmt.Println("Pending migrations: unknown — no migrations are registered in this binary (see 'quark migrate up' for the embed recipe).")
		return nil
	}
	var pending []string
	for _, id := range migrate.RegisteredIDs() {
		if !applied[id] {
			pending = append(pending, id)
		}
	}
	fmt.Printf("Pending migrations (%d):\n", len(pending))
	for _, id := range pending {
		fmt.Printf("  [ ] %s\n", id)
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
	ctx := context.Background()
	// Same fresh-database contract as `migrate status`: report "none applied"
	// rather than a missing-table error.
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("initializing migration table: %w", err)
	}
	applied, err := migrator.GetApplied(ctx)
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

// normalizeDDLDialect maps CLI/config dialect spellings onto the names
// internal/migrate.SQLTypeWithOpts understands.
func normalizeDDLDialect(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "postgresql", "postgres":
		return "postgres"
	case "mysql":
		return "mysql"
	case "mariadb":
		return "mariadb"
	case "sqlite":
		return "sqlite"
	case "mssql", "sqlserver":
		return "mssql"
	case "oracle":
		return "oracle"
	default:
		return ""
	}
}

// renderExecBody turns DDL statements into the client.Exec sequence the
// migration template embeds.
func renderExecBody(stmts []string) string {
	var b strings.Builder
	for _, stmt := range stmts {
		b.WriteString("\t\t\tif err := client.Exec(ctx, `" + stmt + "`); err != nil {\n")
		b.WriteString("\t\t\t\treturn err\n")
		b.WriteString("\t\t\t}\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
