# Release notes — v1.9.0

A minor release that takes the database drivers out of the library. Quark
never registered a driver — it uses the `*sql.DB` you hand it, and the
installation guide always asked for the driver's blank import — but it did
carry the driver **error types**, because classifying a failure means
recognising it: a MySQL deadlock is a `*mysql.MySQLError` with number 1213,
and naming that type imports MySQL. Every Quark binary carried all five
engines, whichever one it talked to.

Measured on a program that links the root package and nothing else:

| | Binary | Packages | Modules |
| --- | --- | --- | --- |
| v1.8.0 | 24 MB | 304 | 171 |
| v1.9.0 | **6 MB** | **159** | **129** |

### Added

- **A module per engine**, each registering the driver *and* what Quark needs
  to know about its errors:

  ```go
  import _ "github.com/jcsvwinston/quark/drivers/postgres"
  ```

  Available: `drivers/postgres`, `drivers/mysql`, `drivers/sqlite`,
  `drivers/mssql`, `drivers/oracle`. Nothing else changes — the DSN, the
  dialect names and every call you already write stay as they are.

- **`quarkdriver`, the contract a driver module implements.** It is a leaf
  package, so writing a driver for an engine Quark does not publish costs
  nothing but the standard library. `quarkdriver/drivertest` is the
  conformance kit that checks the properties which make it safe to consult
  classifiers in turn — above all that a classifier answers only for its own
  driver's errors.

### Changed

- **The three predicates travel together.** `quarkdriver.Classifier` asks for
  unique-violation, deadlock and transient-connection in one struct,
  deliberately, so that supplying two of the three is not expressible. None of
  them *fails* when it does not recognise an error: it answers `false`, and
  Quark acts on that answer. A `false` from the unique predicate turns a 409
  into a 500; a `false` from the deadlock predicate is a transaction that
  quietly stops being retried, under load, months later; a `false` from the
  connection predicate keeps sending reads to a replica that is down. None of
  the three produces an error anyone sees.

- **PostgreSQL registers no classifier, on purpose.** Every PostgreSQL driver
  exposes its SQLSTATE through a `SQLState() string` method, so Quark reads it
  without naming a driver type — which is what lets the same code cover
  `lib/pq`, `pq` and `pgx`, the three the dialect layer accepts. A classifier
  typed to one of them would quietly stop covering the others.

- **The LISTEN/NOTIFY listener moved to `drivers/postgres`**, because it
  reaches the pgx connection underneath `database/sql` and cannot be built
  without it. `EventPayload` and `EventListener` are now aliases of the
  contract's types: code that already writes `quark.EventListener` does not
  change. Calling `CreateListener` without the module returns an error naming
  the import instead of a nil listener.

- **`mattn/go-sqlite3` is no longer classified in the tree.** It sat behind a
  build tag so that a `CGO_ENABLED=0` build kept compiling. It reports the
  same numeric extended codes as the pure-Go driver, so an application using
  it registers its own classifier with the same three lines as
  `drivers/sqlite`.

### Upgrading

Add the module for the engine you use, and remove the raw driver import if you
had one:

```diff
-import _ "github.com/jackc/pgx/v5/stdlib"
+import _ "github.com/jcsvwinston/quark/drivers/postgres"
```

The `quark` CLI links every engine, as before: it is a tool you install once
and point at whatever database you have.
