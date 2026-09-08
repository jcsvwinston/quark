# Security Policy

## Supported Versions

Quark is **v1.12.0** <!-- x-release-please-version --> — stable under SemVer. Security fixes land on `main` and
on the latest two tagged minors; older tags are not patched. Upgrade to the
current tag for security updates.

| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |
| Older tags | ❌ — please upgrade |

The two minors above are the ones the sentence over the table resolves to
today. They are written out, not left as a description, so that a reader can
tell whether their tag is covered without knowing which minors exist — and so
that CI can check the claim: `scripts/check-version-coherence.sh` derives the
supported minors from `.release-please-manifest.json` and fails when this
table names a different set. The table is updated in the release pull request,
alongside the release notes.

---

## Reporting a Vulnerability

**Please do NOT open a public GitHub issue for security vulnerabilities.**

Privately report a vulnerability using one of the following methods:

1. **GitHub Private Security Advisory (preferred):**  
   Navigate to [Security → Report a vulnerability](https://github.com/jcsvwinston/quark/security/advisories/new) in this repository and fill in the form.

2. **E-mail:**  
   Send a description to **serrano.juan.carlos@gmail.com**.  
   Encrypt your message with the maintainer's GPG key if the content is sensitive.

Please include:
- A description of the vulnerability and its potential impact.
- Steps to reproduce or a proof-of-concept.
- Affected versions.
- Any suggested remediation, if known.

You will receive an acknowledgement within **72 hours** and a more detailed response within **7 days**.

---

## Security Design Principles

Quark was built with security as a core design constraint, not a layer bolted on afterward:

- **SQLGuard** validates every identifier (table name, column name, operator) against an allowlist before it touches the wire. This prevents identifier-based injection even when column names originate from user-controlled input.
- **Parameterized queries only** — Quark never interpolates user-supplied values directly into SQL strings.
- **`AllowRawQueries = false` by default** — raw sub-queries require an explicit opt-in via `quark.WithLimits(...)`.
- **Safe migrations by default** — `SafeMigrations: true` blocks destructive DDL (`DROP COLUMN`, `DROP TABLE`) unless explicitly disabled.
- **No credential storage** — Quark never stores or logs DSN credentials.

If you find a bypass for any of these mechanisms, it is considered a critical security vulnerability.

---

## Dependency and Toolchain Advisories

Quark does not maintain its own advisory list — the source of truth is the
[Go vulnerability database](https://vuln.go.dev/) as reported by
`govulncheck ./...`, which runs in CI on every PR and push to `main`. A
finding that is actually reachable from Quark's code fails the build.
Toolchain and dependency pins are bumped as advisories land; each bump is
recorded in the [CHANGELOG](CHANGELOG.md).

---

## Supply Chain

Every GitHub Action this repository runs is pinned to a commit SHA with its
tag in a trailing comment — `uses: actions/checkout@11d5960a… # v4.4.0`. A tag
is a pointer its owner can move under a job that already has this tree checked
out; a SHA is not. `scripts/ci/check_action_pins.sh` fails CI on a `uses:`
that names anything else, and the `github-actions` ecosystem in
[`.github/dependabot.yml`](.github/dependabot.yml) proposes the next SHA
weekly: an unattended pin is worse than a tag, because it holds an action at
the day someone wrote it down, security fixes included.

An [OpenSSF Scorecard](https://scorecard.dev) analysis runs weekly and on
demand ([`.github/workflows/scorecard.yml`](.github/workflows/scorecard.yml))
and files each finding as a code scanning alert. It reports; it does not gate.
Results are not published to the public OpenSSF dataset, so the score is read
from the run itself.

---

## Disclosure Policy

We follow a **90-day coordinated disclosure** timeline:

1. Vulnerability reported privately.
2. Maintainers acknowledge and begin investigation (≤72 h).
3. A fix is developed on a private branch.
4. A patched release is published.
5. A GitHub Security Advisory is published (simultaneously with the release or up to 7 days later).

We will credit reporters in the advisory unless anonymity is requested.
