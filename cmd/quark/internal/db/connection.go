package db

import (
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark"
	"github.com/spf13/viper"
)

func GetQuarkClient() (*quark.Client, error) {
	driver := viper.GetString("database.default.driver")
	dsn := viper.GetString("database.default.dsn")

	if driver == "" || dsn == "" {
		return nil, fmt.Errorf("database configuration missing")
	}

	return quark.New(DriverName(driver), dsn, quark.WithLimits(quark.Limits{AllowRawQueries: true}))
}

func GetAdminQuarkClient() (*quark.Client, error) {
	driver := viper.GetString("database.admin.driver")
	dsn := viper.GetString("database.admin.dsn")

	if driver == "" || dsn == "" {
		return nil, fmt.Errorf("admin database configuration missing")
	}

	return quark.New(DriverName(driver), dsn, quark.WithLimits(quark.Limits{AllowRawQueries: true}))
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
			return nil, fmt.Errorf("database configuration missing")
		}
		dsn := strings.ReplaceAll(tmpl, "{tenant}", tenantID)
		return quark.New(DriverName(driver), dsn, quark.WithLimits(quark.Limits{AllowRawQueries: true}))
	case "schema_per_tenant":
		return nil, fmt.Errorf("schema_per_tenant migrations are not supported by the standalone CLI: the migrator would run against the connection's default schema, not the tenant's. Run migrations from your own binary with a TenantRouter (see the multi-tenant guide)")
	default:
		return nil, fmt.Errorf("unsupported strategy: %s", strategy)
	}
}
