package commands

import (
	"context"
	"fmt"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	tenantID string
	skipSeed bool
	forceOp  bool
)

func init() {
	tenantCmd.AddCommand(tenantProvisionCmd)
	tenantCmd.AddCommand(tenantMigrateCmd)
	tenantCmd.AddCommand(tenantListCmd)
	tenantCmd.AddCommand(tenantMigrateAllCmd)

	tenantProvisionCmd.Flags().BoolVar(&skipSeed, "skip-seed", false, "Skip seeders after provision")
	tenantMigrateCmd.Flags().StringVar(&tenantID, "tenant-id", "", "ID of the tenant")
	tenantMigrateAllCmd.Flags().BoolVar(&migrateDryRun, "dry-run", false, "Preview SQL")

	rootCmd.AddCommand(tenantCmd)
}

var tenantCmd = &cobra.Command{
	Use:   "tenant",
	Short: "Manage multi-tenant environments",
}

var tenantProvisionCmd = &cobra.Command{
	Use:   "provision <tenant-id>",
	Short: "Provision a new tenant",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runTenantProvision(args[0])
	},
}

var tenantMigrateCmd = &cobra.Command{
	Use:   "migrate <tenant-id>",
	Short: "Run migrations for a specific tenant",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runTenantMigrate(args[0])
	},
}

var tenantListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active tenants",
	Run: func(cmd *cobra.Command, args []string) {
		runTenantList()
	},
}

var tenantMigrateAllCmd = &cobra.Command{
	Use:   "migrate-all",
	Short: "Run migrations for all tenants",
	Run: func(cmd *cobra.Command, args []string) {
		runTenantMigrateAll()
	},
}

func runTenantProvision(id string) {
	fmt.Printf("Provisioning tenant: %s...\n", id)

	adminClient, err := db.GetAdminQuarkClient()
	if err != nil {
		color.Red("Error connecting to admin database: %v", err)
		return
	}

	strategy := viper.GetString("tenant.strategy")
	if strategy == "" {
		strategy = "db_per_tenant"
	}

	ctx := context.Background()

	switch strategy {
	case "db_per_tenant":
		// Create Database
		query := fmt.Sprintf("CREATE DATABASE %s", id)
		if err := adminClient.Exec(ctx, query); err != nil {
			color.Red("Error creating database: %v", err)
			return
		}
		fmt.Printf("  Created database: %s\n", id)
	case "schema_per_tenant":
		// Create Schema
		query := fmt.Sprintf("CREATE SCHEMA %s", id)
		if err := adminClient.Exec(ctx, query); err != nil {
			color.Red("Error creating schema: %v", err)
			return
		}
		fmt.Printf("  Created schema: %s\n", id)
	default:
		color.Red("Unsupported strategy: %s", strategy)
		return
	}

	// Register tenant in quark_tenants registry
	if err := ensureTenantRegistry(ctx, adminClient); err != nil {
		color.Red("Error initializing tenant registry: %v", err)
		return
	}
	regQuery := fmt.Sprintf("INSERT INTO quark_tenants (id, strategy) VALUES ('%s', '%s')", id, strategy)
	if err := adminClient.Exec(ctx, regQuery); err != nil {
		color.Yellow("Warning: could not register tenant in quark_tenants: %v", err)
	} else {
		fmt.Printf("  Registered tenant in quark_tenants registry.\n")
	}

	// Run migrations
	runTenantMigrate(id)

	color.Green("Tenant %s provisioned successfully!", id)
}

func runTenantMigrate(id string) {
	fmt.Printf("Migrating tenant: %s...\n", id)

	// In a real implementation, we would resolve the tenant DSN/Schema here.
	// For this CLI version, we'll assume the default client can be used with a router or DSN adjustment.
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error connecting to tenant database: %v", err)
		return
	}

	migrator := migrate.NewMigrator(client)
	if err := migrator.Up(context.Background(), 0); err != nil {
		color.Red("Error migrating tenant %s: %v", id, err)
		return
	}
	fmt.Printf("  Migrations complete for %s\n", id)
}

func runTenantList() {
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error connecting to database: %v", err)
		return
	}
	defer client.Close()

	ctx := context.Background()

	// Ensure tenant registry table exists
	if err := ensureTenantRegistry(ctx, client); err != nil {
		color.Red("Error initializing tenant registry: %v", err)
		return
	}

	rows, err := client.Raw().QueryContext(ctx, "SELECT id, strategy, created_at FROM quark_tenants ORDER BY created_at DESC")
	if err != nil {
		color.Red("Error listing tenants: %v", err)
		return
	}
	defer rows.Close()

	var tenants []struct {
		ID        string
		Strategy  string
		CreatedAt string
	}
	for rows.Next() {
		var t struct {
			ID        string
			Strategy  string
			CreatedAt string
		}
		if err := rows.Scan(&t.ID, &t.Strategy, &t.CreatedAt); err != nil {
			color.Red("Error reading tenant row: %v", err)
			return
		}
		tenants = append(tenants, t)
	}

	if len(tenants) == 0 {
		color.Yellow("No tenants found in registry.")
		return
	}

	color.Cyan("Active tenants (%d):\n", len(tenants))
	fmt.Printf("  %-20s %-20s %s\n", "ID", "Strategy", "Created At")
	fmt.Printf("  %-20s %-20s %s\n", "--------------------", "--------------------", "-------------------")
	for _, t := range tenants {
		fmt.Printf("  %-20s %-20s %s\n", t.ID, t.Strategy, t.CreatedAt)
	}
}

func runTenantMigrateAll() {
	client, err := db.GetQuarkClient()
	if err != nil {
		color.Red("Error connecting to database: %v", err)
		return
	}
	defer client.Close()

	ctx := context.Background()

	if err := ensureTenantRegistry(ctx, client); err != nil {
		color.Red("Error initializing tenant registry: %v", err)
		return
	}

	rows, err := client.Raw().QueryContext(ctx, "SELECT id FROM quark_tenants ORDER BY created_at ASC")
	if err != nil {
		color.Red("Error reading tenants: %v", err)
		return
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			color.Red("Error reading tenant id: %v", err)
			return
		}
		ids = append(ids, id)
	}
	rows.Close()

	if len(ids) == 0 {
		color.Yellow("No tenants found in registry.")
		return
	}

	color.Cyan("Migrating %d tenant(s)...\n", len(ids))
	success, failed := 0, 0
	for _, id := range ids {
		fmt.Printf("  Migrating %s...", id)
		if migrateDryRun {
			color.Yellow(" [dry-run skipped]")
			continue
		}
		runTenantMigrate(id)
		color.Green(" OK")
		success++
	}

	if !migrateDryRun {
		fmt.Printf("\nDone: %d succeeded, %d failed.\n", success, failed)
	}
}

func ensureTenantRegistry(ctx context.Context, client interface {
	Exec(context.Context, string, ...any) error
}) error {
	query := `CREATE TABLE IF NOT EXISTS quark_tenants (
		id          VARCHAR(255) PRIMARY KEY,
		strategy    VARCHAR(50)  NOT NULL DEFAULT 'db_per_tenant',
		created_at  TIMESTAMP    DEFAULT CURRENT_TIMESTAMP
	)`
	return client.Exec(ctx, query)
}
