# Release notes — v1.15.0

A minor from arc A8 of the suite plan, "Quark enterprise". What Quark can do as
an enterprise data layer is measured by the
[enterprise bench](https://github.com/jcsvwinston/quark/blob/main/docs/enterprise-bench.md):
69 controls, each a probe against the public API. 20 were present when it was
first run against v1.14.0; 47 are present at this release, and the 22 that are
not each say what is missing.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Added

**Escaped `LIKE`.** `WhereLike`, `WhereNotLike`, `WhereContains`,
`WhereStartsWith` and `WhereEndsWith` (and the typed `Contains`, `StartsWith`,
`EndsWith`, `LikeEscaped`, `NotLikeEscaped`) escape `%`, `_` and the escape
character inside the user's text and declare the escape per engine — `ESCAPE
'\'`, doubled on MySQL and MariaDB, with `[` also escaped on SQL Server. The
plain `Where(col, "LIKE", pattern)` is **unchanged** and still passes the
pattern through: changing what a backslash means on three engines is a
breaking change, and it waits for the next major with the others.

**The plan carries what the model declares.** `Migrate` and `PlanMigration`
emit foreign keys and `CHECK` constraints inline and the indexes alongside;
`Diff` orders parent tables first, matches foreign keys by what they reference
(SQLite does not keep their names) and indexes by shape. The model declares an
index with `quark:"index"` or `quark:"index=<name>"`, and a check with
`quark:"check=<expr>"` or `db:"col,enum=a|b"`. Constraints that exist in the
database and are not declared are never proposed for dropping.

**`ALTER COLUMN` on every engine.** Type, nullability, default and primary
key changes apply on the six engines, each in its dialect's form. SQLite has
no `ALTER COLUMN`, so it rebuilds the table, carrying the indexes, triggers,
checks and foreign keys it reads back from `sqlite_master`; a `CHECK` it cannot
read back is refused rather than silently dropped.

**Types.** A `[16]byte`-shaped value gets the engine's uuid type (`UUID` on
PostgreSQL and SQLite, `CHAR(36)`, `NCHAR(36)` or `VARCHAR2(36)` elsewhere),
and a key mapped to a custom column type keeps its `PRIMARY KEY`. Raw slices
and maps are stored as native arrays on PostgreSQL and as JSON elsewhere;
`quark.Range[T]` maps to the PostgreSQL range types and to JSON elsewhere;
`net.IP` to `INET`. The operators `@>`, `<@`, `&&`, `<<`, `>>`, `<<=` and
`>>=` are known to the builder and refused with `ErrUnsupportedFeature` off
PostgreSQL. The [type matrix](https://jcsvwinston.github.io/quantum/quark/reference/type-matrix/) is regenerated
from the mapper.

**Native row-level security that verifies.** Under `RowLevelSecurityNative`,
`GetClient` fails closed off PostgreSQL, and the router checks at first use
per table that the engine enforces RLS (`pg_class`, `pg_policy`), refusing
with `ErrRLSNotEnforced` when it cannot confirm it; `SkipPolicyVerification`
opts out. Native RLS stays PostgreSQL-only, with the reason in the
[guide](https://jcsvwinston.github.io/quantum/quark/advanced/row-level-native/).

**Keyset pagination.** `PaginateAfter(pageSize, token)` seeks to the last row
read through the query's `ORDER BY` — the tuple comparison written out,
because SQL Server and Oracle do not have it — with the primary key appended
so the order is total. One statement per page; it returns a `KeysetPage` with
`Items`, `HasMore` and an opaque `Next` token. A token from a query with a
different `ORDER BY` is refused with `ErrInvalidQuery`. `Paginate` keeps its
page-number contract and its total.

**The CLI.** `quark migrate diff | plan | verify --from-models <dir>` reads
the model structs from source, maps them with the runtime's own type mapping
and diffs them against the live schema; `verify` exits non-zero on drift, so
it works as a CI gate. `quark tenant install-rls-policies |
verify-rls-policies --from-models <dir>` install and verify the policies the
guide used to print, on PostgreSQL. The static reader learns the `default`,
`index`, `check` and `enum` tags, and `quark model` accepts `index`. See the
[CLI guide](https://jcsvwinston.github.io/quantum/quark/guides/cli/).

## Changed

`PlanMigration` with no models is **refused** with `ErrInvalidQuery`. The
desired schema of nothing is "drop every live table", and that is the plan a
binary compiled without the user's models used to be handed.

## Fixed

**Tenant confinement survives a transaction** (QK-26). `ForTx` takes the
tenant from the `*Tx`; a context that names a different tenant fails with
`ErrTenantMismatch`. The rule is that the transaction fixes the tenant: a
context without one inherits it, and one that names another is a mistake.
Writes had also been ignoring the query's deferred error, so a query that
should have been refused executed unconfined; they check it now.

**`precision` and `scale` refined every column type they were put on**
(QK-28). They now refine float columns only and warn elsewhere. The decimal
family — `numeric`, `decimal`, Oracle's `NUMBER(p,s)` — is one type to the
diff (QK-30), so a plan no longer proposes rewriting a column into itself.

