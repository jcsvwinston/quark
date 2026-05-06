# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-05-06

### Breaking Changes

- **Client Creation API**: Changed `quark.New()` signature from `New(db *sql.DB, opts ...Option)` to `New(driverName, dataSource string, opts ...any)`
  - The function now accepts a driver name and data source string instead of a `*sql.DB` instance
  - `sql.Open()` is now called internally by `New()`
  - Dialect is now auto-detected from the driver name, removing the need for explicit `WithDialect()` option
  - Connection pool options (`WithMaxOpenConns`, `WithMaxIdleConns`, `WithConnMaxLifetime`, `WithConnMaxIdleTime`) are now applied during client creation

- **Removed Options**: 
  - `WithDialect()` option is no longer needed as dialect is auto-detected from driver name
  - Passing `*sql.DB` directly to `New()` is no longer supported

### Added

- **New Client Method**: Added `WithOptions(opts ...any) (*Client, error)` method to `Client` for recreating clients with different options without exposing the underlying `*sql.DB`
- **Connection Pool Options**: Added pool configuration options:
  - `WithMaxOpenConns(maxOpenConns int)` - Sets maximum number of open connections
  - `WithMaxIdleConns(maxIdleConns int)` - Sets maximum number of idle connections
  - `WithConnMaxLifetime(d time.Duration)` - Sets maximum connection lifetime
  - `WithConnMaxIdleTime(d time.Duration)` - Sets maximum idle connection time

### Migration Guide

**Old API:**
```go
db, err := sql.Open("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
```

**New API:**
```go
client, err := quark.New("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

**Recreating client with different options:**

**Old API:**
```go
newClient, err := quark.New(client.Raw(), quark.WithLimits(newLimits))
```

**New API:**
```go
newClient, err := client.WithOptions(quark.WithLimits(newLimits))
```

### Supported Drivers for Auto-Detection

- `"sqlite"`, `"sqlite3"`, `"modernc"` → SQLite
- `"postgres"`, `"pgx"`, `"pgx/v5"`, `"pq"` → PostgreSQL
- `"mysql"` → MySQL
- `"mariadb"` → MariaDB
- `"mssql"`, `"sqlserver"`, `"azuresql"` → MSSQL
- `"oracle"`, `"godror"`, `"oci8"` → Oracle

## [0.1.0] - Previous Release

Initial release
