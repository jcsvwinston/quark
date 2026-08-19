# Release notes — v1.5.1

**Writes are durable when the call returns.** v1.5.1 is a single-fix patch
for the `RowLevelSecurityNative` strategy, found by an external end-to-end
suite as a rare flake (one failure in ~27 clean rebuilds) and reproduced by
the new regression test on iteration 2 of 300.

Docs (1.5.1 is the current version): <https://jcsvwinston.github.io/quark/docs/>

## Fixed

- **The implicit-transaction commit for `QueryRow` operations is now
  synchronous.** Under native RLS every operation runs inside an implicit
  transaction (required for `set_config(..., is_local=true)`). Single-row
  operations — `Create` and `Upsert`, which execute `INSERT … RETURNING`,
  included — used to leave that transaction open and commit it from a
  `context.AfterFunc` when the operation's context ended. That callback runs
  in a separate goroutine: there was **no happens-before between the call
  returning and the commit executing**, so a handler could answer success
  while the write was not yet durable, and an immediate reader on another
  connection missed the row. v1.3.1 fixed a first variant of this class
  (the deferred commit racing the automatic rollback); this closes the
  remaining one. The executor now reads the returned row generically,
  commits before returning, and re-serves the row through an internal
  row-minting driver — the same mechanism error rows already used.

  Consequences:
  - a write is committed when its call returns — always;
  - a commit failure surfaces through `Scan` as the call's own error,
    never as a success followed by a silent rollback;
  - single-row reads release their pooled connection immediately;
  - the deferred commit remains only on the multi-row read path
    (`*sql.Rows`), where it affects when the connection is released —
    never durability. `Client.DeferredCommitFailures` now covers exactly
    that path.

## Upgrade notes

Drop-in. No API changes. The native-RLS guide's write-semantics section is
rewritten: the documented "brief window" after a write no longer exists.
