package config

import (
	"time"
)

type Config struct {
	Project    ProjectConfig    `mapstructure:"project"`
	Database   DatabaseConfig   `mapstructure:"database"`
	Paths      PathsConfig      `mapstructure:"paths"`
	Generation GenerationConfig `mapstructure:"generation"`
	Tenant     TenantConfig     `mapstructure:"tenant"`
	Security   SecurityConfig   `mapstructure:"security"`
}

type ProjectConfig struct {
	Name   string `mapstructure:"name"`
	Module string `mapstructure:"module"`
}

type DatabaseConfig struct {
	Default DBConnConfig `mapstructure:"default"`
	Admin   DBConnConfig `mapstructure:"admin"`
	Pool    PoolConfig   `mapstructure:"pool"`
}

type DBConnConfig struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

type PoolConfig struct {
	MaxOpen     int           `mapstructure:"max_open"`
	MaxIdle     int           `mapstructure:"max_idle"`
	MaxLifetime time.Duration `mapstructure:"max_lifetime"`
}

type PathsConfig struct {
	Models     string `mapstructure:"models"`
	Migrations string `mapstructure:"migrations"`
	Seeders    string `mapstructure:"seeders"`
}

type GenerationConfig struct {
	Dialect  string        `mapstructure:"dialect"`
	Package  string        `mapstructure:"package"`
	Naming   NamingConfig  `mapstructure:"naming"`
	Tags     []string      `mapstructure:"tags"`
	Features FeatureConfig `mapstructure:"features"`
}

type NamingConfig struct {
	Table string `mapstructure:"table"`
	Field string `mapstructure:"field"`
}

type FeatureConfig struct {
	SoftDelete bool `mapstructure:"soft_delete"`
	Timestamps bool `mapstructure:"timestamps"`
	JSONTags   bool `mapstructure:"json_tags"`
}

type TenantConfig struct {
	Strategy         string        `mapstructure:"strategy"`
	TenantIDRegex    string        `mapstructure:"tenant_id_regex"`
	MaxCachedTenants int           `mapstructure:"max_cached_tenants"`
	ClientTTL        time.Duration `mapstructure:"client_ttl"`
}

type SecurityConfig struct {
	MaxRowsDefault int  `mapstructure:"max_rows_default"`
	GuardStrict    bool `mapstructure:"guard_strict"`
}
