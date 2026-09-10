# Security Policy

## Supported Versions

Quark is **v1.13.0** <!-- x-release-please-version --> — stable under SemVer. Security fixes land on `main` and
on the latest two tagged minors; older tags are not patched. Upgrade to the
current tag for security updates.

| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.13.x` | ✅ |
| `v1.12.x` | ✅ |
| Older tags | ❌ — please upgrade |

The two minors above are the ones the sentence over the table resolves to
today. They are written out, not left as a description, so that a reader can
tell whether their tag is covered without knowing which minors exist — and so
that CI can check the claim: `scripts/check-version-coherence.sh` derives the
supported minors from `.release-please-manifest.json` and fails when this
table names a different set. The rows are written in the release pull request
by `scripts/release/gen_release_notes_skeleton.sh`, which reads the same
manifest and asks the check itself which minors it demands — the equality has
an author, not only a judge.

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

## Static Analysis (SAST)

`govulncheck` reads Quark's dependencies; CodeQL reads Quark. It follows a
value from where it enters the program to where it is used, which is the only
way to see a caller-supplied string reaching a query, a path or a command
several calls later — the layer below SQLGuard, which checks identifiers at
the door.

It is enabled as GitHub's **code scanning default setup**: a repository
setting rather than a workflow file, which is why there is no
`.github/workflows/codeql.yml` in this tree. It runs on every pull request and
weekly on `main`, and it analyses three languages — Go, the GitHub Actions
workflows, and the TypeScript under `website/`.

Its Go pass covers **every module** of this repository: the library, the CLI,
the five drivers, the three local harnesses and the ten runnable examples —
twenty-one of them since the CLI, the acceptance harness and the engine suites
became modules of their own. The extractor discovers every `go.mod` in the
tree and says so in the run log ("extraction succeeded for all N discovered
projects"), which is more than a hand-written workflow analysing the root
module would see. Measured on 8
September 2026: 3 min 07 s for Go, 1 min 18 s for TypeScript, 41 s for
Actions, run in parallel.

The two configurations are exclusive. GitHub documents that enabling default
setup "will disable the existing workflow file and block any CodeQL analysis
API uploads", so committing a `codeql.yml` here without first turning default
setup off in **Settings → Code security** would add a lane that runs and then
cannot publish what it found. If that setting is ever turned off, this section
is the reminder that the repository then needs a workflow — and that the
workflow has to cover Actions and TypeScript as well, or the move is a
downgrade.

It reports; it does not gate. Alerts appear in the Security tab and in a pull
request's "Files changed"; no check fails because of one.

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

## Verifying a Release

Releases cut from the first signed tag onward publish six `quark` CLI archives
(Linux, macOS and Windows on amd64 and arm64), one SPDX SBOM per archive, a
`checksums.txt` covering all of them, a keyless cosign signature over that
checksum file (`checksums.txt.sig` and `checksums.txt.pem`), and a build
provenance attestation.

Earlier releases publish no assets at all — every tag cut before this shipped
has an empty asset list — because signing cannot be applied retroactively: a release is signed by the run that builds it, with a
certificate minted for that run and valid for minutes. For those tags
`go install github.com/jcsvwinston/quark/cmd/quark@vX.Y.Z` is the way in, and
the Go module proxy's checksum database is what stands behind it. The release
page tells you which kind you are looking at: a signed release lists
`checksums.txt.sig`.

The CLI is its own Go module and has its own tag series (`cmd/quark/vX.Y.Z`),
so that `@vX.Y.Z` selector only resolves for library tags cut before the
split; from then on it is `@latest` or a `cmd/quark` version. The archives are
still named after the library release they ship with, and the binary reports
both numbers.

There is no long-lived signing key, and none is published: what you verify is
which workflow, in which repository, at which tag produced the release. The
release workflow is dispatched at the **tag** ref, so the certificate identity
ends in `.../release.yml@refs/tags/vX.Y.Z` — not `@refs/heads/main`, which is
what every cosign example shows and what verifies nothing here.

The two commands, with the exact identity string and the failure modes, are on
the public site's
[Operations → Verifying a release](https://jcsvwinston.github.io/quantum/quark/operations/verifying-releases)
page (source:
[`website/docs/operations/verifying-releases.mdx`](website/docs/operations/verifying-releases.mdx)).

---

## Disclosure Policy

We follow a **90-day coordinated disclosure** timeline:

1. Vulnerability reported privately.
2. Maintainers acknowledge and begin investigation (≤72 h).
3. A fix is developed on a private branch.
4. A patched release is published.
5. A GitHub Security Advisory is published (simultaneously with the release or up to 7 days later).

We will credit reporters in the advisory unless anonymity is requested.
