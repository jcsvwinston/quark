# Release notes — v1.15.1

A patch with no change to the library. The sibling modules — the CLI and the
five drivers — raise their `require` of the root to v1.15.0, the version they
are certified with, and are released again so the floor a user gets matches it.

Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>

## Changed

- **Module floors.** `cmd/quark` (v1.1.1) and `drivers/{postgres,mysql,sqlite,mssql,oracle}`
  (v0.2.3) require `github.com/jcsvwinston/quark v1.15.0` instead of v1.14.0
  ([#423](https://github.com/jcsvwinston/quark/pull/423)). Go resolves the
  highest version in the graph, so an application that already required v1.15.0
  builds the same code as before; what changes is that `go install` of the CLI
  and a driver added on its own no longer start from v1.14.0.
