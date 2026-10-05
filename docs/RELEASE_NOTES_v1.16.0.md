# Release notes — v1.16.0

A minor that closes three cross-tenant defects of `RowLevelSecurityClient` —
by-key reads, upserts and map updates could reach another tenant's rows — and
opens the dialect contract to drivers written outside this repository. If
your application uses a tenant router with `RowLevelSecurityClient`, upgrade.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

**By-key reads and writes stay inside the tenant (QK-42, QK-40, QK-41;
[#435](https://github.com/jcsvwinston/quark/pull/435)).** Under
`RowLevelSecurityClient`, `Find(id)` read a row of another tenant, and
`Update(entity)`, `UpdateBatch`, `Delete(entity)`, `HardDelete(entity)`,
`DeleteBatch` and `Restore` wrote by primary key alone. Every by-key path now
carries the tenant predicate and the caller's own `Where` on top of the key;
a key of another tenant reads as `ErrNotFound`, and a write the conditions
exclude touches nothing, fires no `After*` hook, writes no audit entry and
returns `(0, nil)` — what `UpdateMap`, `DeleteBy` and `UpdateFields` already
returned. A real version conflict is still `ErrStaleEntity`. `Create`,
`CreateBatch` and the writes stamp the resolved tenant on the row and log
`quark.tenant.foreign_value_replaced` when the entity carried another. The
soft-delete and tenant scopes now AND with the caller's whole expression in
parentheses, so an `Or` group no longer escapes them: a trashed row no longer
comes back through `Or`, and a bare `Or(…)` no longer returns rows outside
the tenant. `Find` also stopped leaving its key on the query it was called
on, and `First` stopped leaving `LIMIT 1`.

**Upserts and map updates stay inside the tenant (QK-43, QK-44;
[#437](https://github.com/jcsvwinston/quark/pull/437)).** Under
`RowLevelSecurityClient`, the update branch of `Upsert` and `UpsertBatch`
updated whichever row held the conflicting key, in any tenant. It is now
guarded per engine (`ON CONFLICT … DO UPDATE … WHERE`, `ON DUPLICATE KEY
UPDATE … IF(…)`, `MERGE … WHEN MATCHED AND …`); a key held by another tenant
updates nothing and returns an error wrapping `ErrConstraintViolation`, as a
`Create` of that key does. `UpsertBatch` is all or nothing in that mode.
`UpdateMap` writes the resolved tenant when its map names the tenant column.

**`IN`, `NOT IN` and `BETWEEN` accept any slice (QK-33;
[#425](https://github.com/jcsvwinston/quark/pull/425)).** `Where(col, "IN", []string{…})`
panicked; any slice or array is now expanded, and a byte slice
(one value to `database/sql`) is refused with a hint. The dialect registry is
safe for concurrent use.

**Module floors.** `cmd/quark` (v1.2.0) and the five `drivers/*` modules
(v0.2.5) require `github.com/jcsvwinston/quark v1.15.2`
([#439](https://github.com/jcsvwinston/quark/pull/439)).

## Added

**The dialect contract lives in `quarkdriver`
([#436](https://github.com/jcsvwinston/quark/pull/436)).** `Dialect`,
its optional interfaces, the schema model, the lock types, the sentinels and
the registry (`RegisterDialect`, `LookupDialect`) are declared in the leaf
package; `quark` keeps every name as an alias of the same type, so code
written against v1.15 compiles unchanged. A driver can now implement and
register a dialect importing only `quarkdriver`.

**The schema path asks the dialect instead of reading its name
([#432](https://github.com/jcsvwinston/quark/pull/432),
[#433](https://github.com/jcsvwinston/quark/pull/433)).** Migrate,
PlanMigration, Sync, the bookkeeping tables and ApplyPlan decide column types,
auto-increment keys, `IF NOT EXISTS`, transactional DDL, `ALTER COLUMN`, drops
and SQLite's table rebuild through optional interfaces in `quarkdriver`
(`ColumnTyper`, `AutoIncrementer`, `IdempotentDDL`, `ColumnAlterer`,
`ObjectDropper`, `TableRebuilder`). The six built-in dialects emit the same
DDL as before; a dialect that implements none of them gets portable DDL or
`ErrUnsupportedFeature`, never a silently wrong statement. Oracle's dialect no
longer claims transactional DDL.

**Tested framework integrations
([#426](https://github.com/jcsvwinston/quark/pull/426),
[#427](https://github.com/jcsvwinston/quark/pull/427)).** net/http, chi, Echo,
Gin, gRPC and Nucleus fixtures serve the same API on a client and pass one
battery in CI; the frameworks guide is checked line by line against them.

**`quark init --with chi|echo|gin|grpc|nucleus`
([#438](https://github.com/jcsvwinston/quark/pull/438)).** The scaffold
writes the same wiring the tested fixtures run — request context, a
transaction per request, error-to-status mapping — and CI builds, vets and
tests its output for each framework. The `--with nucleus` output changes
shape: it is now the fixture's module.

**A performance bench on a real PostgreSQL and MySQL
([#431](https://github.com/jcsvwinston/quark/pull/431)).** `benchmarks.mdx`
now publishes Quark against `database/sql` and pgx per operation, with the
method and the open target.
