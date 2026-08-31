package db

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jcsvwinston/quark"
	"github.com/spf13/viper"
)

// missingConfigError builds an actionable error for an absent connection: it
// names the concrete config keys / env vars and the one command that writes
// them, instead of the old bare "database configuration missing" that left the
// user guessing (QC-5). keyPrefix is e.g. "database.default"; envPrefix is its
// QUARK_ env spelling.
func missingConfigError(role, keyPrefix, envPrefix string) error {
	label := "database"
	if role != "" {
		label = role + " database"
	}
	return fmt.Errorf(`%s configuration missing: set %s.driver and %s.dsn.

Fix it one of these ways:
  - run 'quark init' to scaffold a .quark.yml, then edit the dsn, or
  - set %s_DRIVER and %s_DSN in the environment, or
  - point the CLI at a config file with --config <path>`,
		label, keyPrefix, keyPrefix, envPrefix, envPrefix)
}

// cliLogger returns the logger the CLI installs on every client. It is quiet
// by default — the per-command "quark client initialized" INFO line that used
// to print on every invocation is suppressed (AQ-16) — while WARN/ERROR still
// surface. Pass --debug to restore INFO.
func cliLogger() *slog.Logger {
	level := slog.LevelWarn
	if viper.GetBool("debug") {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// clientOptions are the options every CLI-opened client shares: the tuned
// limits and the quiet logger. quark.New takes ...any, so this returns []any.
func clientOptions() []any {
	return []any{quark.WithLimits(cliLimits()), quark.WithLogger(cliLogger())}
}

func GetQuarkClient() (*quark.Client, error) {
	driver := viper.GetString("database.default.driver")
	dsn := viper.GetString("database.default.dsn")

	if driver == "" || dsn == "" {
		return nil, missingConfigError("", "database.default", "QUARK_DATABASE_DEFAULT")
	}

	return quark.New(DriverName(driver), dsn, clientOptions()...)
}

func GetAdminQuarkClient() (*quark.Client, error) {
	driver := viper.GetString("database.admin.driver")
	dsn := viper.GetString("database.admin.dsn")

	if driver == "" || dsn == "" {
		return nil, missingConfigError("admin", "database.admin", "QUARK_DATABASE_ADMIN")
	}

	return quark.New(DriverName(driver), dsn, clientOptions()...)
}

// GetTenantQuarkClient opens a client connected to ONE tenant's database.
// It exists so `quark tenant migrate`/`migrate-all` operate on the tenant the
// caller named instead of silently migrating the default database (QK-P1-3).
//
// Only the db_per_tenant strategy is resolvable from static CLI config: the
// DSN comes from `tenant.dsn_template`, with the literal `{tenant}` replaced
// by the (already validated) tenant id. schema_per_tenant migrations need a
// TenantRouter wired to your models, which a standalone binary cannot build —
// that path returns an explicit error rather than a wrong-database migration.
func GetTenantQuarkClient(tenantID string) (*quark.Client, error) {
	strategy := viper.GetString("tenant.strategy")
	if strategy == "" {
		strategy = "db_per_tenant"
	}

	switch strategy {
	case "db_per_tenant":
		tmpl := viper.GetString("tenant.dsn_template")
		if tmpl == "" {
			return nil, fmt.Errorf("tenant.dsn_template is not configured: set it in .quark.yml to the tenant DSN with a {tenant} placeholder, e.g. postgres://user:pass@localhost/{tenant}?sslmode=disable")
		}
		if !strings.Contains(tmpl, "{tenant}") {
			return nil, fmt.Errorf("tenant.dsn_template %q has no {tenant} placeholder — every tenant would resolve to the same database", tmpl)
		}
		driver := viper.GetString("database.default.driver")
		if driver == "" {
			return nil, missingConfigError("", "database.default", "QUARK_DATABASE_DEFAULT")
		}
		dsn := strings.ReplaceAll(tmpl, "{tenant}", tenantID)
		return quark.New(DriverName(driver), dsn, clientOptions()...)
	case "schema_per_tenant":
		return nil, fmt.Errorf("schema_per_tenant migrations are not supported by the standalone CLI: the migrator would run against the connection's default schema, not the tenant's. Run migrations from your own binary with a TenantRouter (see the multi-tenant guide)")
	default:
		return nil, fmt.Errorf("unsupported strategy: %s", strategy)
	}
}

// cliLimits starts from DefaultLimits and enables only raw queries — the
// CLI's migrate/seed/tenant paths need them. Starting from the defaults
// (instead of a partial literal) keeps SafeMigrations=true and silences the
// partial-literal WARN that used to fire on EVERY CLI command, telling the
// user their brand-new project risked dropped tables because of the CLI's
// own wiring (DX-6).
func cliLimits() quark.Limits {
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	return limits
}
