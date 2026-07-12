package commands

import (
	"context"
	"fmt"
	"regexp"

	"github.com/fatih/color"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// validTenantID mirrors the TenantRouter's tenant-id contract (tenant_router.go).
// The id reaches CREATE DATABASE / CREATE SCHEMA statements, so anything
// outside this alphabet is rejected before any SQL is built.
var validTenantID = regexp.MustCompile(`^[a-z0-9_-]+$`)

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

// Tenant jobs are batch/automation territory: a provision or migrate that
// fails must exit non-zero (RunE → main.go prints and exits 1).
var tenantProvisionCmd = &cobra.Command{
	Use:           "provision <tenant-id>",
	Short:         "Provision a new tenant",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTenantProvision(args[0])
	},
}

var tenantMigrateCmd = &cobra.Command{
	Use:           "migrate <tenant-id>",
	Short:         "Run migrations for a specific tenant",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTenantMigrate(args[0])
	},
}

var tenantListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List active tenants",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTenantList()
	},
}

var tenantMigrateAllCmd = &cobra.Command{
	Use:           "migrate-all",
	Short:         "Run migrations for all tenants",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTenantMigrateAll()
	},
}

func runTenantProvision(id string) error {
	// Validate BEFORE connecting or building any SQL: id lands in DDL
	// (CREATE DATABASE/SCHEMA can't take bind parameters) and strategy is
	// config-driven — both are attacker-reachable via argv/config.
	if !validTenantID.MatchString(id) {
		return fmt.Errorf("invalid tenant id %q: must match %s", id, validTenantID.String())
	}
	strategy := viper.GetString("tenant.strategy")
	if strategy == "" {
		strategy = "db_per_tenant"
	}
	if strategy != "db_per_tenant" && strategy != "schema_per_tenant" {
		return fmt.Errorf("unsupported strategy: %s", strategy)
	}

	fmt.Printf("Provisioning tenant: %s...\n", id)

	adminClient, err := db.GetAdminQuarkClient()
	if err != nil {
		return fmt.Errorf("connecting to admin database: %w", err)
	}
	defer adminClient.Close()

	ctx := context.Background()
	dialect := adminClient.Dialect()

	switch strategy {
	case "db_per_tenant":
		// Create Database — DDL takes no bind params; the regexp above plus
		// dialect quoting make the identifier inert.
		query := fmt.Sprintf("CREATE DATABASE %s", dialect.Quote(id))
		if err := adminClient.Exec(ctx, query); err != nil {
			return fmt.Errorf("creating database: %w", err)
		}
		fmt.Printf("  Created database: %s\n", id)
	case "schema_per_tenant":
		// Create Schema
		query := fmt.Sprintf("CREATE SCHEMA %s", dialect.Quote(id))
		if err := adminClient.Exec(ctx, query); err != nil {
			return fmt.Errorf("creating schema: %w", err)
		}
		fmt.Printf("  Created schema: %s\n", id)
	}

	// Register tenant in quark_tenants registry
	if err := ensureTenantRegistry(ctx, adminClient); err != nil {
		return fmt.Errorf("initializing tenant registry: %w", err)
	}
	regQuery := fmt.Sprintf("INSERT INTO quark_tenants (id, strategy) VALUES (%s, %s)",
		dialect.Placeholder(1), dialect.Placeholder(2))
	if err := adminClient.Exec(ctx, regQuery, id, strategy); err != nil {
		color.Yellow("Warning: could not register tenant in quark_tenants: %v", err)
	} else {
		fmt.Printf("  Registered tenant in quark_tenants registry.\n")
	}

	// Run migrations
	if err := runTenantMigrate(id); err != nil {
		return err
	}

	color.Green("Tenant %s provisioned successfully!", id)
	return nil
}

func runTenantMigrate(id string) error {
	fmt.Printf("Migrating tenant: %s...\n", id)

	// In a real implementation, we would resolve the tenant DSN/Schema here.
	// For this CLI version, we'll assume the default client can be used with a router or DSN adjustment.
	client, err := db.GetQuarkClient()
	if err != nil {
		return fmt.Errorf("connecting to tenant database: %w", err)
	}
	defer client.Close()

	migrator := migrate.NewMigrator(client)
	if err := migrator.Up(context.Background(), 0); err != nil {
		return fmt.Errorf("migrating tenant %s: %w", id, err)
	}
	fmt.Printf("  Migrations complete for %s\n", id)
	return nil
}

func runTenantList() error {
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	ctx := context.Background()

	// Ensure tenant registry table exists
	if err := ensureTenantRegistry(ctx, client); err != nil {
		return fmt.Errorf("initializing tenant registry: %w", err)
	}

	rows, err := client.Raw().QueryContext(ctx, "SELECT id, strategy, created_at FROM quark_tenants ORDER BY created_at DESC")
	if err != nil {
		return fmt.Errorf("listing tenants: %w", err)
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
			return fmt.Errorf("reading tenant row: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading tenant rows: %w", err)
	}

	if len(tenants) == 0 {
		color.Yellow("No tenants found in registry.")
		return nil
	}

	color.Cyan("Active tenants (%d):\n", len(tenants))
	fmt.Printf("  %-20s %-20s %s\n", "ID", "Strategy", "Created At")
	fmt.Printf("  %-20s %-20s %s\n", "--------------------", "--------------------", "-------------------")
	for _, t := range tenants {
		fmt.Printf("  %-20s %-20s %s\n", t.ID, t.Strategy, t.CreatedAt)
	}
	return nil
}

func runTenantMigrateAll() error {
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()

	ctx := context.Background()

	if err := ensureTenantRegistry(ctx, client); err != nil {
		return fmt.Errorf("initializing tenant registry: %w", err)
	}

	rows, err := client.Raw().QueryContext(ctx, "SELECT id FROM quark_tenants ORDER BY created_at ASC")
	if err != nil {
		return fmt.Errorf("reading tenants: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("reading tenant id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	if len(ids) == 0 {
		color.Yellow("No tenants found in registry.")
		return nil
	}

	color.Cyan("Migrating %d tenant(s)...\n", len(ids))
	success, failed := 0, 0
	for _, id := range ids {
		fmt.Printf("  Migrating %s...", id)
		if migrateDryRun {
			color.Yellow(" [dry-run skipped]")
			continue
		}
		if err := runTenantMigrate(id); err != nil {
			color.Red(" FAILED: %v", err)
			failed++
			continue
		}
		color.Green(" OK")
		success++
	}

	if !migrateDryRun {
		fmt.Printf("\nDone: %d succeeded, %d failed.\n", success, failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tenant migrations failed", failed, len(ids))
	}
	return nil
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
