# Release notes — v1.11.0

The minor that pays Quark's share of the maturity audit of 2026-09-03: the
migrator stops being the one place where two processes could run the same
migration at once, and the listener contract stops naming a type nobody
outside the repository can import.

### Added

- **The migrator takes the schema lock by default.** `Migrator.Up` and
  `Down` acquire the engine's cluster-wide migration lock (`quark:schema`,
  30s) before touching the schema — `pg_advisory_lock` on PostgreSQL,
  `GET_LOCK` on MySQL and MariaDB, `sp_getapplock` on SQL Server,
  `DBMS_LOCK` on Oracle. Two replicas running `migrate up` at the same time
  used to both apply the same pending migration. SQLite, which has no
  distributed lock and a single writer, proceeds without one. Options:
  `WithoutLock()`, `WithLockTimeout(d)`, `WithLockName(name)` and
  `WithLogger(l)`.
- **Transactional migrations.** A migration written as `UpTx` / `DownTx`
  receives a `*sql.Tx`; on engines that roll DDL back (PostgreSQL, SQLite,
  SQL Server) the migration and its ledger row commit together, so a
  migration that fails halfway leaves neither behind. Where DDL commits
  itself (MySQL, MariaDB, Oracle) only the ledger row is atomic with the
  last statement, and the migrator says so at debug level.
- **`Client.Logger()`** returns the logger the client was built with; the
  migrator reports through it instead of writing to stdout. The `quark
  migrate` commands keep printing their plain sentences.
- **A public listener contract.** `quarkdriver.ListenerFactory` takes an
  `IdentifierValidator` interface — the one thing a listener needs from the
  ORM's SQL guard — instead of the internal guard type the previous
  `NewListenerFunc` named, which no module outside this repository could
  implement. `RegisterListenerFactory`, `MustRegisterListenerFactory` and
  `LookupListenerFactory` are the registry; the previous names stay,
  deprecated and adapted, so `drivers/postgres` v0.1.1 keeps working and
  adopts the public contract in its next release.

### Documentation

- The README says what `go.mod` lists versus what a binary links: the root
  module requires the five engine drivers because the CLI links every
  engine, while an application links one. Reading them out of the root
  means moving the CLI to a module of its own, a decision left to the
  suite's plan.
- Commit messages and pull-request titles are in English, and CI checks
  the title.

### Modules

- The five `drivers/*` modules require Quark v1.10.1 (their floor moves as
  the first commit of every release train from now on).
