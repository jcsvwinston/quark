# Release notes — v1.12.0

Quark's part of the suite starter (arc A2 of the suite plan): `quark init`
can write the Nucleus module that mounts the ORM, and the integration
examples cover the routers people actually use.

### Added

- **`quark init --with nucleus`** writes `internal/<app>/module.go`, a
  Nucleus module wrapping the `*quark.Client`: an example `Note` model
  registered and migrated on start, `GET`/`POST /api/notes` through
  `quark.For[Note]`, the module's own policy rows (reads open to anonymous,
  a `create` row as a documented development default) and CSRF exemption
  for the API path, and the driver module for the chosen dialect
  blank-imported so a duplicate title answers 409. It also writes the
  `nucleus.yml` the framework needs to boot — `database_default`,
  `databases.default.url` in the URL form Nucleus parses, `host`, `port`,
  `env: development`, `log_level` — naming the same database as the
  `.quark.yml` placeholder, and never overwrites either file. The module is
  emitted as source on purpose: Quark carries no framework dependency, so
  this repository cannot compile it against Nucleus. An unknown `--with`
  target fails before anything is written.
- **`examples/chi`, `examples/echo`, `examples/gin`**: modules of their own
  (built by the examples lane, not release units) with the same notes API on
  three routers, each with a `main_test.go`.

### Documentation

- `website/docs/guides/frameworks.mdx` (net/http, chi, Echo, Gin, Nucleus),
  linked from the installation page and from a new *With Nucleus* subsection
  of the CLI guide. The version history of `CLAUDE.md` moved to
  `.claude/HISTORIAL.md`; the release skeleton moves the README pointer to
  the current minor's notes.

### Modules

- `drivers/postgres` v0.1.3 registers its listener through the public
  `quarkdriver.ListenerFactory` contract instead of the deprecated
  `NewListenerFunc` shape, and requires Quark v1.11.0.
