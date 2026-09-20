// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
)

// A8 S10 (RLS-06). `quark tenant install-rls-policies` and
// `quark tenant verify-rls-policies` are the commands ADR-0012 and the guide
// print; until now they existed only as actions of the embeddable runner
// (quarktenant.Run), which rejects the `tenant` word in front of them, so
// whoever followed the documentation typed something nothing accepted. The
// models come from source (--from-models), like every other command of this
// binary; the DDL is the runner's, rendered here for tables the reader found.

var (
	tenantRLSFromModels string
	tenantRLSDryRun     bool
	tenantRLSTenantCol  string
	tenantRLSVar        string
	tenantRLSForce      bool
	tenantRLSTenantCast string
)

func init() {
	for _, c := range []*cobra.Command{tenantInstallRLSCmd, tenantVerifyRLSCmd} {
		c.Flags().StringVar(&tenantRLSFromModels, "from-models", "", "directory of the package whose model structs carry the tenant column (required)")
		c.Flags().StringVar(&tenantRLSTenantCol, "tenant-col", "tenant_id", "the tenant column a policy filters on")
		c.Flags().StringVar(&tenantRLSVar, "native-rls-var", "app.tenant_id", "the session variable the policy reads (TenantConfig.NativeRLSVar)")
		_ = c.MarkFlagRequired("from-models")
		tenantCmd.AddCommand(c)
	}
	tenantInstallRLSCmd.Flags().BoolVar(&tenantRLSDryRun, "dry-run", false, "print the DDL and change nothing")
	tenantInstallRLSCmd.Flags().BoolVar(&tenantRLSForce, "force", true, "also FORCE ROW LEVEL SECURITY, so the table owner is filtered too")
	tenantInstallRLSCmd.Flags().StringVar(&tenantRLSTenantCast, "tenant-col-cast", "", "cast applied to the session variable (default ::text)")
}

var tenantInstallRLSCmd = &cobra.Command{
	Use:           "install-rls-policies",
	Short:         "Enable row-level security and install the tenant-isolation policy on every model table that has the tenant column (PostgreSQL)",
	Example:       "  quark tenant install-rls-policies --from-models ./models --dry-run\n  quark tenant install-rls-policies --from-models ./models --tenant-col org_id --native-rls-var app.org_id",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTenantInstallRLS(cmd)
	},
}

var tenantVerifyRLSCmd = &cobra.Command{
	Use:           "verify-rls-policies",
	Short:         "Exit non-zero unless every model table that has the tenant column enforces its policy (PostgreSQL)",
	Example:       "  quark tenant verify-rls-policies --from-models ./models",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTenantVerifyRLS(cmd)
	},
}

// tenantTables are the model tables that carry the tenant column, in the
// order the reader found them.
func tenantTables(models []ddlModel, tenantCol string) []string {
	var out []string
	for _, m := range models {
		for _, f := range m.Fields {
			if f.Column == tenantCol {
				out = append(out, m.Table)
				break
			}
		}
	}
	return out
}

// rlsPolicyDDL renders the runner's DDL for one table: enable (and force)
// row-level security, drop the policy if it exists, create it over the
// session variable. Kept byte-for-byte in the shape quarktenant renders, so
// quarktenant.VerifyRLSPolicies reads it as its own.
func rlsPolicyDDL(table, tenantCol, rlsVar, cast string, force bool) []string {
	q := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	if cast == "" {
		cast = "::text"
	} else if !strings.HasPrefix(cast, "::") {
		cast = "::" + cast
	}
	policy := table + "_tenant_isolation"
	setting := "'" + strings.ReplaceAll(rlsVar, "'", "''") + "'"
	stmts := []string{fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", q(table))}
	if force {
		stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", q(table)))
	}
	stmts = append(stmts,
		fmt.Sprintf("DROP POLICY IF EXISTS %s ON %s", q(policy), q(table)),
		fmt.Sprintf(`CREATE POLICY %s ON %s USING (%s = current_setting(%s, true)%s) WITH CHECK (%s = current_setting(%s, true)%s)`,
			q(policy), q(table), q(tenantCol), setting, cast, q(tenantCol), setting, cast))
	return stmts
}

func requirePostgres(client *quark.Client, what string) error {
	if dn := client.Dialect().Name(); dn != "postgres" {
		return fmt.Errorf("%w: %s requires PostgreSQL, got dialect %q", quark.ErrUnsupportedFeature, what, dn)
	}
	return nil
}

func runTenantInstallRLS(cmd *cobra.Command) error {
	models, err := loadModelsForDDL(tenantRLSFromModels)
	if err != nil {
		return err
	}
	tables := tenantTables(models, tenantRLSTenantCol)
	if len(tables) == 0 {
		return fmt.Errorf("no model in %s declares the tenant column %q; nothing to install", tenantRLSFromModels, tenantRLSTenantCol)
	}
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()
	if err := requirePostgres(client, "install-rls-policies"); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	ctx := context.Background()
	for _, table := range tables {
		for _, stmt := range rlsPolicyDDL(table, tenantRLSTenantCol, tenantRLSVar, tenantRLSTenantCast, tenantRLSForce) {
			fmt.Fprintln(out, stmt+";")
			if tenantRLSDryRun {
				continue
			}
			if _, err := client.Raw().ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w", table, err)
			}
		}
	}
	if tenantRLSDryRun {
		fmt.Fprintln(out, color.YellowString("dry run: nothing was changed"))
	} else {
		fmt.Fprintln(out, color.GreenString("policies installed on %d table(s)", len(tables)))
	}
	return nil
}

func runTenantVerifyRLS(cmd *cobra.Command) error {
	models, err := loadModelsForDDL(tenantRLSFromModels)
	if err != nil {
		return err
	}
	tables := tenantTables(models, tenantRLSTenantCol)
	if len(tables) == 0 {
		return fmt.Errorf("no model in %s declares the tenant column %q; nothing to verify", tenantRLSFromModels, tenantRLSTenantCol)
	}
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()
	if err := requirePostgres(client, "verify-rls-policies"); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	ctx := context.Background()
	var gaps []string
	for _, table := range tables {
		var enabled, forced, present bool
		var qual sql.NullString
		err := client.Raw().QueryRowContext(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity, p.polname IS NOT NULL, pg_get_expr(p.polqual, p.polrelid)
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			  LEFT JOIN pg_policy p ON p.polrelid = c.oid AND p.polname = $2
			 WHERE c.relname = $1 AND c.relkind = 'r' AND n.nspname = current_schema()`,
			table, table+"_tenant_isolation").Scan(&enabled, &forced, &present, &qual)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			gaps = append(gaps, table+": table does not exist")
			continue
		case err != nil:
			return fmt.Errorf("%s: %w", table, err)
		}
		switch {
		case !enabled:
			gaps = append(gaps, table+": row-level security disabled")
		case !present:
			gaps = append(gaps, table+": policy "+table+"_tenant_isolation missing")
		case !strings.Contains(qual.String, tenantRLSVar):
			gaps = append(gaps, table+": the policy does not read "+tenantRLSVar+" ("+qual.String+")")
		case !forced:
			gaps = append(gaps, table+": FORCE ROW LEVEL SECURITY is off (the table owner bypasses the policy)")
		default:
			fmt.Fprintf(out, "%s: enforced\n", table)
		}
	}
	if len(gaps) > 0 {
		return fmt.Errorf("row-level security is not enforced:\n  %s\nRemedy: quark tenant install-rls-policies --from-models %s", strings.Join(gaps, "\n  "), tenantRLSFromModels)
	}
	fmt.Fprintln(out, color.GreenString("row-level security enforced on %d table(s)", len(tables)))
	return nil
}
