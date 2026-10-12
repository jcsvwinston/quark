# Release notes — v1.17.0

A minor that fixes what several write paths did on some engines — an
`UpdateBatch` that left the caller's transaction, upserts that wrote another
row's key or overwrote a column, writes that left a cached read stale — and
adds two opt-ins: a statement cache, and version checks for `UpdateBatch`. It
also publishes the contract for code that extends Quark, with a conformance
kit and a template for drivers. If your code calls `UpdateBatch` on a `ForTx`
query, calls `Upsert`, or caches reads, upgrade.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

**`UpdateBatch` inside your transaction stays in it (QK-57;
[#455](https://github.com/jcsvwinston/quark/pull/455)).** On a `ForTx` query,
`UpdateBatch` opened a transaction of its own on another connection of the
pool. When you held no lock on its rows, the batch committed on its own and
your rollback did not undo it; when you did, it waited for your lock until the
query timeout, and your transaction failed with it. On SQLite, where the first
write of a transaction takes the only writer lock, an `UpdateBatch` after any
write in the same transaction hung until the query timeout. It now runs in a
savepoint of your transaction: a failing row undoes the batch and leaves your
transaction usable (on PostgreSQL, not aborted), and your commit or rollback
decides the rest. `UpsertBatch` under `RowLevelSecurityClient` takes the same
savepoint, so a batch that meets another tenant's row leaves nothing written
even if you go on and commit.

**`Upsert` writes the key of the row it wrote (QK-63, QK-64;
[#461](https://github.com/jcsvwinston/quark/pull/461)).** On MySQL, and on
MariaDB through the MySQL dialect, the key came from a separate
`SELECT LAST_INSERT_ID()` on whichever pooled connection answered, so an
upsert that updated an existing row could write another row's key into the
entity. On SQL Server and Oracle, an entity that carried a key kept it while
the row was inserted with the key the identity column assigned. The key now
comes from the statement that wrote the row — the `INSERT`'s own result on
MySQL, `OUTPUT` on SQL Server, `MERGE … RETURNING` on Oracle — and where the
statement cannot say which row it wrote, the entity keeps the key it had; it
never gets another row's. On SQL Server, a `MERGE` with no update branch that
met an existing row returned `sql.ErrNoRows`; it now returns `nil`, as the
other engines do. The [CRUD reference](https://jcsvwinston.github.io/quantum/quark/reference/api/crud/#the-key-written-into-the-entity)
has the key each engine leaves in the entity. On Oracle, `Upsert` and
`UpsertBatch` need Oracle 23ai, as they already did; the
[Dialects](https://jcsvwinston.github.io/quantum/quark/reference/dialects/#oracle-specific-behavior) page now says so.

**`Upsert` without `updateCols` (QK-48, QK-61, QK-62;
[#459](https://github.com/jcsvwinston/quark/pull/459),
[#460](https://github.com/jcsvwinston/quark/pull/460)).** Three defects, each
on some engines:

- On PostgreSQL and SQLite a conflict returned a raw `sql.ErrNoRows`. It now
  returns `nil`, as on the other four engines, and writes no key back. If you
  read that error as "the row already existed", you now get `nil`.
- On MySQL and MariaDB, when the duplicate was on a unique key other than the
  conflict column — the primary key, say — the existing row's conflict column
  was overwritten with the incoming value. The row is now left as it was.
- On SQL Server and Oracle, the `MERGE`'s update set included the primary key
  and `created_at`: a non-zero integer key made the engine refuse the whole
  statement (Msg 8102, ORA-32796), and `created_at` was overwritten. Both are
  left out, as `Update` leaves them out; `updated_at` is still refreshed.

The engines still disagree on what an empty `updateCols` means: PostgreSQL,
SQLite, MySQL and MariaDB leave the conflicting row as it was, and SQL Server
and Oracle update its other columns. Making them agree would change what
existing calls do on one side, so it waits for `v2`; the
[CRUD reference](https://jcsvwinston.github.io/quantum/quark/reference/api/crud/#upsert) says what each engine does.

**Every write drops the cache entries it made stale (QK-66;
[#462](https://github.com/jcsvwinston/quark/pull/462)).** `Upsert` on
PostgreSQL, SQLite and MariaDB dropped no cache tag, so a cached `List` kept
answering with its old rows after an upsert; `Restore` and `DeleteBatch` did
not drop the rows' tags; and a `CreateBatch` that failed partway on SQL Server
dropped nothing for the rows it left inserted. Each write path now drops its
table tag and the tags of the rows it changed. Inside a transaction the tags
are dropped when the statement runs and again when the transaction commits, so
a read from outside that cached the old rows before the commit no longer
outlives it. Raw SQL (`Client.Exec`, `RawQuery`, `Call`, `Client.Raw()`) still
drops nothing; the [caching reference](https://jcsvwinston.github.io/quantum/quark/reference/api/caching/#manual-invalidation) shows
how to drop tags yourself.

**`CreateBatch` on SQL Server returns the engine's error (QK-53;
[#453](https://github.com/jcsvwinston/quark/pull/453)).** When a row was
rejected — by a unique key, for instance — the caller got
`converting NULL to int64 is unsupported` instead of the engine's error, so
`IsUniqueViolation` answered false. It now gets the engine's error,
classified.

**`ApplyPlan` applies a plan again after the schema was reset (QK-51;
[#458](https://github.com/jcsvwinston/quark/pull/458)).** On MySQL, MariaDB
and Oracle, which have no transactional DDL, `ApplyPlan` keeps a checkpoint of
the operations it applied. The same plan applied again after the schema was
dropped found its checkpoint and changed nothing. When a checkpoint exists,
`ApplyPlan` now reads the live schema first and, if an operation it recorded
is no longer in effect, applies the plan from the start. Resuming after a
failure partway through a plan works as before.

**Foreign-key actions are written as the engine takes them (QK-49;
[#457](https://github.com/jcsvwinston/quark/pull/457)).** Quark copied a
foreign key's `ON DELETE` and `ON UPDATE` into the DDL as given. Oracle refuses
`NO ACTION` (its default), `RESTRICT`, `SET DEFAULT` and any `ON UPDATE`
clause, and SQL Server refuses `RESTRICT`, so `AddForeignKey` and `ApplyPlan`
failed on the engine — with part of the plan already applied where DDL commits
on its own. Oracle's `NO ACTION` is now left out, and an action the engine
lacks is refused with `ErrUnsupportedFeature` before any statement is sent;
`ApplyPlan` checks every foreign key of the plan before its first operation.
The dialect answers through a new optional interface,
`quarkdriver.ReferentialActioner`; a dialect that does not implement it writes
the actions as given, as before. MySQL and MariaDB accept `SET DEFAULT` without
doing it; that is documented, not refused, because refusing it would fail a
call that succeeds today.

**Two dialect fixes ([#441](https://github.com/jcsvwinston/quark/pull/441)).**
On MariaDB, `ApplyPlan`'s drop of a `CHECK` constraint always failed: the
dialect wrote MySQL's `DROP CHECK`, which MariaDB does not have, and now writes
`DROP CONSTRAINT`. On SQL Server, `ForShare().SkipLocked()` wrote a pair of
lock hints the engine refuses; it now returns `ErrUnsupportedFeature`, as
MariaDB's shared lock with modifiers does.

## Added

**A statement cache, off by default
([#452](https://github.com/jcsvwinston/quark/pull/452)).**
`WithStatementCache(size)` keeps up to `size` prepared statements per pool and
reuses them for the query builder's statements that carry arguments, instead
of having the driver prepare, execute and close one on every call; inside a
transaction each statement is prepared once on the transaction's connection.
On MySQL 8.4, `FindByPK` took 46–48 % less time with the cache than without it
in each of five runs on the CI runner (145 µs against 270 µs on an AMD
EPYC 7763), and 41–42 % less in two runs on a laptop (Apple M4 Pro, Docker
Desktop). It applies on MySQL, MariaDB,
SQL Server and SQLite. It is ignored on PostgreSQL, whose pgx driver already
keeps a statement cache per connection, and on Oracle: with go-ora v2.9.0, a
prepared count re-executed on the same connection after other statements
answered 0 for a row that existed, and Quark relies on such counts to tell a
stale entity from one the query excludes. Sizing it against
`max_prepared_stmt_count`, and how it compares with `interpolateParams=true`,
are on the
[Dialects](https://jcsvwinston.github.io/quantum/quark/reference/dialects/#mysql-statements-per-query) page.

**`CheckVersions()` for `UpdateBatch`
([#463](https://github.com/jcsvwinston/quark/pull/463)).** On a model with a
`quark:"version"` field, `Update`, `UpdateFields` and `Tracked.Save` return
`ErrStaleEntity` when the row's version moved. `UpdateBatch` sent the same
version check and ignored its result: a stale row was skipped, the rest were
written, the call returned `nil`, and the written rows' versions moved in the
database but not in memory, so the next `Update` of the same struct failed
with a conflict nobody caused. Under `Query[T].CheckVersions()`, `UpdateBatch`
is all or nothing: if any row is stale or its key does not exist, the whole
batch is rolled back and the error joins one `ErrStaleEntity` per stale row,
naming its key; otherwise the in-memory versions are bumped. A row the query's
conditions exclude is skipped, as `Update` skips it. Without
`CheckVersions()` nothing changes in `v1`.

**In 2.0, `UpdateBatch` checks versions by default.** A batch that returns
`nil` today with a stale row in it will return an error, and
`CheckVersions()` will keep compiling with nothing left to switch on. The
change comes no earlier than 2027-01-04, and it is listed in the
[register of deprecations and behaviour changes](https://github.com/jcsvwinston/quark/blob/main/docs/deprecations/README.md).
To get the 2.0 behaviour now, add `CheckVersions()` to your batches. `Upsert`,
`Delete`, `HardDelete` and `Restore` still carry no version check; the
[modeling guide](https://jcsvwinston.github.io/quantum/quark/guides/modeling/) has the table of which writes do.

**`CreateBatch` keys in one round trip per chunk on SQL Server and MySQL
([#452](https://github.com/jcsvwinston/quark/pull/452)).** These engines have
no `RETURNING` for a multi-row `INSERT`, so `CreateBatch` with generated keys
sent one statement per row. On SQL Server each chunk is now one `MERGE` whose
`OUTPUT` returns every key next to its row's position, on tables whose key is
an `IDENTITY` column without an `INSTEAD OF` trigger. On MySQL each chunk is
one multi-row `INSERT`, and the keys are computed from the first one, only when
the server runs `innodb_autoinc_lock_mode` 0 or 1 — the settings under which
MySQL documents that a statement's keys are consecutive. MySQL 8's default is
2, so a default server keeps one `INSERT` per row. With lock mode 1 on
MySQL 8.4, 1000 rows took 9.6 ms on the CI runner, against 241 ms sent one
`INSERT` per row. If a row of a chunk fails, the chunk runs again row by row,
so the rows left inserted and the error are what they were. Observers see the
new statements: a `MERGE` on SQL Server; a savepoint, the multi-row `INSERT`
and a read of `auto_increment_increment` on MySQL; and one catalog probe per
client and table. The conditions are in the
[batch operations guide](https://jcsvwinston.github.io/quantum/quark/guides/batch-operations/#createbatch).

## Performance

**Less work per list, preload and batch
([#444](https://github.com/jcsvwinston/quark/pull/444)).** `List` no longer
serializes its result to JSON when no cache is configured. On PostgreSQL a
preload selects each chunk of keys with `= ANY($1)` and one array parameter
instead of a list of placeholders, and assembles the children onto their
parents with one growth per parent. Numbered placeholders are formatted without
`fmt`, and `Validate` skips a model that declares no rule. On the engine bench
against PostgreSQL 16 on the CI runner, Quark's time over hand-written
`database/sql`, in the recorded medians before and after, went from 1.98 to
1.25 for 100 parents with their 500 children, from 1.35 to 1.17 for a 100-row
`List`, and from 1.33 to 1.09 for a 1000-row `CreateBatch`. The preload's SQL
changes shape on PostgreSQL, which observers and the slow-query log show. The
bench now checks times only on the CPU models it has a record for, and keeps
one ratio per CPU model where the same code measures up to 20 % apart between
models ([#454](https://github.com/jcsvwinston/quark/pull/454)); the
[Benchmarks](https://jcsvwinston.github.io/quantum/quark/reference/benchmarks/) page has the method and every figure.

## Extension contract

**What you can implement, and what a `v1` minor may change
([#443](https://github.com/jcsvwinston/quark/pull/443),
[#464](https://github.com/jcsvwinston/quark/pull/464)).** The new
[extension contract](https://jcsvwinston.github.io/quantum/quark/reference/extension-contract/) page lists the 53 types of `quark`
and `quarkdriver` that code outside them can implement and hand to Quark. Each
is marked as an extension point — a type you implement and Quark calls, such as
a dialect, a hook, a cache store, a middleware or an observer — or as plumbing,
a type Quark implements and hands to you, such as `Executor`, `Event` or
`Expr`, and each has its stability. The rule is now recorded: within `v1`, an
extension point gains no method and loses or changes none, and a new
capability arrives as a separate optional interface with a documented default.
A plumbing interface may gain a method in a minor release — never in a patch —
and the release notes name it. Code that only calls plumbing is unaffected;
your own implementation of a plumbing interface is not covered. This narrows
the [versioning promise](https://jcsvwinston.github.io/quantum/quark/operations/upgrade/#the-versioning-promise) in that
one place; the
[compatibility decision record](https://github.com/jcsvwinston/quark/blob/main/docs/adr/0029-extension-points-do-not-grow-in-v1.md)
has the reasoning. No plumbing interface gains a method in this release, and a
test holds every extension point to its recorded shape. Three conventions get
exported interfaces — `quark.TableNamer`, `quark.Validator` and
`quarkdriver.SQLStater` — which a type that already has the method satisfies.

**Writing a driver ([#441](https://github.com/jcsvwinston/quark/pull/441),
[#446](https://github.com/jcsvwinston/quark/pull/446)).**
`drivertest.VerifyDialect` runs a dialect against a live database of its
engine and checks every method of `quarkdriver.Dialect`, and of each optional
interface the dialect implements, by what the engine then holds or refuses.
`quarkdriver/drivertest/suite` runs 43 engine-generic subtests of Quark's own
engine suite on any client. The five driver modules run the kit against
their engines. The
[Writing a driver](https://jcsvwinston.github.io/quantum/quark/guides/writing-a-driver/) guide goes from an empty module
to a driver that passes the kit; its code is a template module tested in CI,
and the guide is checked line by line against it. It also says
[what the contract does not express yet](https://jcsvwinston.github.io/quantum/quark/guides/writing-a-driver/#what-the-contract-does-not-express-yet):
the places where Quark still branches on an engine's name.

**Every statement reaches the middleware and the observers
([#447](https://github.com/jcsvwinston/quark/pull/447)).** Schema work, catalog
reads, savepoints and raw SQL skipped the middleware chain, the observers or
both, so neither `quark/otel` nor your own observer saw them. Every statement
Quark sends — `Migrate`, `Sync`, `ApplyPlan`, the migration lock, savepoints,
`Client.Exec`, `RawQuery`, audit rows, the tenant checks of
`RowLevelSecurityNative` — now passes the middleware chain and reaches the
observers, in the order the engine receives them. `QueryEvent.Kind` says what
each statement is for (`query`, `exec`, `ddl`, `introspection`, `savepoint`,
`raw`), `StatementKindFromContext` gives a middleware the same, and
`quark/otel` tags spans and metrics with `quark.statement.kind`. Expect more
spans and observer events after upgrading. Still not observed: transaction
control, which the driver sends; what your code sends on `Client.Raw()` or on
the `*sql.Tx` an `UpTx` migration receives; and the `LISTEN` connection.

## Build and modules

**Built and tested with Go 1.26.9
([#466](https://github.com/jcsvwinston/quark/pull/466)).** The toolchain that
builds and tests Quark, and builds the CLI archives attached to this release,
moves from Go 1.26.6 to 1.26.9. That release fixes GO-2026-6617, a race in the
HTTP/2 server of `net/http` that can crash a process serving HTTP/2, and five
other vulnerabilities in `net/http` and `crypto/tls` that govulncheck reported
under 1.26.6. The library still requires Go 1.25.7 or later and builds with
the Go your application uses; if your application serves HTTP/2, build it with
a Go release that has the fix.

**Module floors.** `cmd/quark` (v1.2.1) and the five `drivers/*` modules
(v0.3.0) require `github.com/jcsvwinston/quark v1.16.0`
([#465](https://github.com/jcsvwinston/quark/pull/465)). The driver modules
take a minor because they gained the conformance-kit tests, and the
requirements those tests bring
([#441](https://github.com/jcsvwinston/quark/pull/441)); no driver code
changed.
