package commands

import (
	"fmt"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(syncCmd)
}

// Schema sync needs your compiled Go model types, which a standalone CLI
// binary cannot load — so this command does NOT diff or apply anything.
// It verifies the configured connection and prints how to wire
// client.Sync(...) in your application. It used to advertise --dry-run /
// --safe / --no-transaction / --models while ignoring all of them; those
// flags are gone (H-Q3) — the real knobs live on quark.SyncOptions.
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Check the DB connection and explain how to run schema sync",
	Long: `Schema sync (auto-migration) compares Go model structs against the live
database schema and applies CREATE TABLE / ADD COLUMN / RENAME COLUMN /
DROP COLUMN as needed. Because it needs your compiled model types, sync runs
programmatically via client.Sync(ctx, opts, &Model{}, ...) inside your
application — not from this CLI.

This command only verifies that the configured database is reachable and
prints the exact call to add to your code, including the SyncOptions that
control dry-run, safe mode and transactional DDL.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSync()
	},
}

func runSync() error {
	client, err := db.GetQuarkClient()
	if err != nil {
		return fmt.Errorf("connecting to database: %w", err)
	}
	defer client.Close()

	printSyncGuidance(client)
	return nil
}

func printSyncGuidance(client *quark.Client) {
	fmt.Println()
	color.Green("Database connection OK.")
	fmt.Println()
	fmt.Println("Schema sync runs inside your application, where your model types live:")
	fmt.Println()
	color.Cyan(`  err := client.Sync(ctx, quark.SyncOptions{DryRun: false}, &User{}, &Order{})`)
	fmt.Println()
	fmt.Println("Sync capabilities:")
	fmt.Printf("  %-40s %s\n", "CREATE TABLE IF NOT EXISTS", "creates missing tables")
	fmt.Printf("  %-40s %s\n", "ALTER TABLE ... ADD COLUMN", "adds new fields")
	fmt.Printf("  %-40s %s\n", `quark:"rename:old_name"`, "renames columns non-destructively")
	fmt.Printf("  %-40s %s\n", "ALTER TABLE ... DROP COLUMN", "drops removed fields (Limits.SafeMigrations: false)")
	fmt.Printf("  %-40s %s\n", "Transactional DDL", "wraps in TX on supported dialects (SyncOptions.NoTransaction)")
	fmt.Println()
	fmt.Printf("  Connected dialect: %s\n", client.Dialect().Name())
	fmt.Println()
	color.Yellow("Tip: SyncOptions{DryRun: true} previews the SQL without applying it.")
}
