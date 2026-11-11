# Release notes — v1.19.0

A minor for the people around Quark rather than for the queries: every
exported `Err*` sentinel now says what to do, the CLI prints the DDL it
derives from models as JSON for another tool to write, and the
documentation site gains a query playground that renders the SQL of the six
dialects in the browser. One CLI fix. The library's behaviour does not
change; the text of some error messages grows, and a test that compares an
error's text exactly will notice.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Fixed

**`migrate create --from-models` loads the models package inside its own
module (QK-100; [#487](https://github.com/jcsvwinston/quark/pull/487)).**
The command loaded the models package from the directory it ran in, so a
package that lived in another Go module — the case of a tool that calls the
CLI from outside the project — failed with «no Go package found» even when
the path was absolute. The loader now runs inside the directory the flag
names: an absolute path works from anywhere, and a relative one is still
relative to the current directory, which the flag's help now says.

## Added

**Every exported sentinel says what to do (QK-78;
[#481](https://github.com/jcsvwinston/quark/pull/481)).** All 30 exported
`Err*` sentinels of the published packages (root, `quarkdriver`,
`quarktenant`) carry a godoc sentence that begins `What to do:` and names
the step, and `TestEveryExportedSentinelNamesWhatToDo` counts them and
holds the [errors reference](https://jcsvwinston.github.io/quantum/quark/reference/api/errors/) to the same list. The sentinels
Quark returns bare — `ErrNotFound`, `ErrTimeout`, `ErrConstraintViolation`,
the listener and lock sentinels, `ErrNoRegisteredModels` — carry the hint
in their message after a colon, so their text is longer than in v1.18.0.
`errors.Is` is unaffected and a prefix match on the old text still
matches; a test that compares `err.Error()` with the exact old string
breaks. The wrap sites that only said what was wrong now say what to do
where there is a step: 165 of 185 name one, from 48 of 181.

**`migrate create --from-models --emit json` prints the rendered DDL
([#486](https://github.com/jcsvwinston/quark/pull/486)).** With `--emit
json` the command writes nothing — not even the `migrations/` directory —
and prints one JSON document on stdout: `{"schema":1,"tool":"quark",
"version":…,"dialect":…,"models":[{"name","table"}],"up":[…],"down":[…]}`.
`up` and `down` are exactly the statements `--emit go` embeds in the
migration file, without trailing semicolons, and `dialect` comes back
normalised (`postgresql` → `postgres`, `sqlserver` → `mssql`). `schema` is
the contract version and moves only on an incompatible change. `--emit go`
is the default and is byte for byte what the command did before. Nucleus's
`makemigrations` is the first caller. The [CLI guide](https://jcsvwinston.github.io/quantum/quark/guides/cli/) has
the paragraph.

**A query playground on the site
([#485](https://github.com/jcsvwinston/quark/pull/485)).** The
[playground](https://jcsvwinston.github.io/quantum/quark/playground/) page describes a query with a form — model,
columns, filters, order, limit, offset, soft-deleted rows — and prints the
SQL each of the six dialects would receive, the bound arguments and the Go
chain that produces it; seven presets cover join, group with `HAVING`,
`Count()`, correlated `EXISTS`, window, `UNION` and a CTE. It runs
nothing: Quark is compiled to WebAssembly and pointed at a driver that
records the statement. The module is not loaded with the page; a button
states its compressed size (3.3 MB) and fetches it on request, and a static
example is visible without JavaScript. A test holds the static example to
what the module renders, and CI fails the build when the module exceeds
3.5 MB compressed or the size written on the page drifts from the measured
one.

## Documentation

**Three guides for production
([#484](https://github.com/jcsvwinston/quark/pull/484)):**
[caching](https://jcsvwinston.github.io/quantum/quark/guides/caching-in-production/),
[read replicas](https://jcsvwinston.github.io/quantum/quark/guides/read-replicas-in-production/) and
[sharding](https://jcsvwinston.github.io/quantum/quark/guides/sharding-in-production/), each written for the person
operating the feature, each saying what it does not cover, and each a copy
of a fixture package CI compiles and runs. Writing them corrected two
claims: an entry cached under the row tag and the table tag is dropped by
every write to the table, so the by-key recipe now uses the row tag alone;
and `ShardRouter` and `TenantRouter` do not nest, so the sharding page
points at one tenant router per shard. The deprecation notices cite the
files that exist, the acceptance harness publishes its measured figures
under a test, and the benchmark pages say the 15 % target was adopted on
2026-10-06 ([#482](https://github.com/jcsvwinston/quark/pull/482)).

## Build and modules

**Built and tested with Go 1.26.11
([#491](https://github.com/jcsvwinston/quark/pull/491)).** The toolchain that
builds and tests Quark, and builds the CLI archives attached to this release,
moves from Go 1.26.10 to 1.26.11, which fixes GO-2026-6859 in `net/http`.
The library still requires Go 1.25.7 or later and builds with the Go your
application uses.

**Module floors.** `cmd/quark` (v1.3.0, which also carries `--emit json`
and the `--from-models` fix) and the five `drivers/*` modules (v0.3.2)
require `github.com/jcsvwinston/quark v1.18.0`
([#490](https://github.com/jcsvwinston/quark/pull/490)); no driver code
changed.
