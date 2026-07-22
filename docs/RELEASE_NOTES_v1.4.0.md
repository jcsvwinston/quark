# Release notes — v1.4.0

**Safer reads, clearer limits.** v1.4.0 is a feature minor: opt-in guards for
unbounded reads and the N+1 pattern, a counter that makes a stuck native-RLS
cleanup observable, and a `WithLimits` that no longer silently breaks on a
partial literal. No breaking changes — everything is additive or a
strictly-more-honest default.

Docs (1.4.0 is the current version): <https://jcsvwinston.github.io/quark/docs/>
(older versions under `/docs/1.3.3/` etc.)

## Added

- **Strict reads (`WithStrictReads`).** An opt-in mode that flags reads with no
  explicit bound:
  - `StrictReadsWarn` logs one structured WARN per unbounded `Iter()`/`Cursor()`;
    `StrictReadsReject` returns `ErrInvalidQuery` before the query runs.
  - `List()` is unchanged — it already caps at 100 with its own WARN.
  - `AllowUnbounded()` on the builder is the explicit escape hatch for exports
    and back-fills that are legitimately unbounded.
  - **N+1 detection (cheap, off by default):** `TrackReads(ctx)` installs a
    per-context counter; ≥10 point reads by primary key against the same table
    within that context emit one WARN suggesting the missing `Preload`. Zero
    allocation when strict reads are off (measured).

- **`Client.BlockedPanicCleanups()`.** The native-RLS panic-path cleanup (the
  detached goroutine that rolls back the implicit transaction and returns its
  pooled connection after a driver panic) can stall if `database/sql` never
  releases the locks the panic left taken. A watchdog now counts those stalls
  and logs one error per event, so a leaked transaction/connection pair is
  observable instead of only inferable from pool exhaustion. The cleanup
  itself is unchanged — still detached, still retrying; the counter only makes
  it visible. The watchdog deadline is the client's `QueryTimeout`.

## Fixed

- **`WithLimits` normalizes zero numeric fields.** A partial literal such as
  `Limits{MaxResults: 500}` used to leave `QueryTimeout` at zero, and every
  query then failed instantly with an already-expired context. `WithLimits`
  now fills the zero numeric fields from `DefaultLimits()`; negative values
  pass through unchanged (they keep their "no cap" meaning). Booleans are
  **not** normalized — a partial literal that leaves `SafeMigrations` false is
  indistinguishable from a deliberate `false`, so instead of guessing, the
  client emits one structured WARN pointing at `DefaultLimits()`.

- **Security — `golang.org/x/text` to v0.39.0.** Closes GO-2026-5970, an
  infinite loop in `x/text` reachable through `database/sql`.

## Upgrade

Drop-in from v1.3.x. The one behaviour to know about: if you constructed a
`Limits` literal by hand and depended on the previous all-zero semantics,
start from `DefaultLimits()` and override what you need — the new WARN will
tell you when a partial literal left `SafeMigrations` off.

## Known limitations (unchanged this minor)

Carried from the v1.x line and listed here for continuity: the generated
UPDATE/batch binder is still deferred (issue #265; INSERT is generated, the
rest uses reflect); Oracle primary-key back-fill via `RETURNING … INTO` is a
declared limitation.
