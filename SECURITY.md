# Security Policy

## Supported Versions

Quark is in **v0.x** — late-alpha. Security fixes land on `main` and on the
latest tagged minor release; older tags are not patched. Upgrade to the
current tag for security updates.

| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v0.9.x` | ✅ |
| `v0.8.x` and earlier | ❌ — please upgrade |

---

## Reporting a Vulnerability

**Please do NOT open a public GitHub issue for security vulnerabilities.**

Privately report a vulnerability using one of the following methods:

1. **GitHub Private Security Advisory (preferred):**  
   Navigate to [Security → Report a vulnerability](https://github.com/jcsvwinston/quark/security/advisories/new) in this repository and fill in the form.

2. **E-mail:**  
   Send a description to **security@[maintainer-domain]** (replace with the maintainer's actual address).  
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

## Disclosure Policy

We follow a **90-day coordinated disclosure** timeline:

1. Vulnerability reported privately.
2. Maintainers acknowledge and begin investigation (≤72 h).
3. A fix is developed on a private branch.
4. A patched release is published.
5. A GitHub Security Advisory is published (simultaneously with the release or up to 7 days later).

We will credit reporters in the advisory unless anonymity is requested.
