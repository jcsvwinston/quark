# Release notes — v1.10.0

A small minor that finishes what v1.9.0 started: when a driver module is
missing, the error now names it.

### Added

- **The missing-driver error names the module.** v1.9.0 moved each engine's
  driver into its own module, but opening a database without one still failed
  with Go's own message:

  ```
  database connection error: sql: unknown driver "sqlite" (forgotten import?)
  ```

  which names neither the module nor the line to add. It now prints the
  `go get` and the `import _`, and it resolves the names people actually
  type — `postgresql`, `postgres` and `pq` all point at `drivers/postgres`,
  `mariadb` at `drivers/mysql`, `mssql` at `drivers/mssql`.

  For a driver Quark does not publish it says nothing extra and lets the
  original error through: inventing a `go get` for someone using another
  engine would send them somewhere that does not exist.

### Documentation

- **The v1.9.0 archive.** v1.9.0 shipped without its documentation snapshot,
  so the site had no archived docs for that minor. Cut and published here,
  along with the narrative notes and the version mentions that release missed.
