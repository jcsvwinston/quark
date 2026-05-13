#!/usr/bin/env bash
# Copyright 2026 jcsvwinston
# SPDX-License-Identifier: Apache-2.0
#
# F0-10. Docs linter. Catches the drift patterns that have bitten Quark
# before — outdated version markers, marketing language, and broken
# relative links inside the docs tree. Runs from CI on every PR.
#
# Checks:
#   1. Anti-marketing — `production-ready`, `enterprise-grade`,
#      `battle-tested` may not appear in user-facing docs unless
#      explicitly negated. Quark is alpha-late until v1.0.
#   2. `RELEASE_NOTES_V1` references must not appear in user-facing
#      docs (the file was deleted in F0-1; lingering pointers are
#      drift).
#   3. Broken relative links — `(./...)` and `(../...)` references in
#      .md/.mdx files must resolve to a file that exists.
#
# Meta files that EXPLAIN the rules (CLAUDE.md, TASKS.md, ADRs,
# ANALISIS_MADUREZ.md, .claude/) are exempt — they discuss the
# forbidden words on purpose.
#
# Exit codes: 0 clean, 1 any check failed.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Output collector. Each failure appends a line; the final non-zero
# exit reflects whether the collector is non-empty.
FAIL=0
report() {
  FAIL=1
  printf '%s\n' "$@"
}

# Meta files exempt from the marketing and V1 checks — they describe
# the rules, so they MUST mention the forbidden strings. Also exempt
# historical artefacts: blog posts and versioned_docs/ snapshots are
# frozen records of "what we said at the time", not the current
# user-facing surface — drift checks apply to `next` (the live tree),
# not to history.
EXEMPT_PATHS=(
  './CLAUDE.md'
  './TASKS.md'
  './docs/ANALISIS_MADUREZ.md'
  './docs/adr/'
  './.claude/'
  './scripts/lint-docs.sh'
  './website/blog/'
  './website/versioned_docs/'
)

is_exempt() {
  local f="$1"
  for ex in "${EXEMPT_PATHS[@]}"; do
    case "$f" in "$ex"*|"./$ex"*) return 0 ;; esac
  done
  return 1
}

# Files to scan: every .md / .mdx outside node_modules and build/.
# mapfile needs bash 4+; on macOS the system bash is 3.2 so we read
# into a newline-separated string and iterate with read -r.
ALL_DOCS=$(find . \
  -path './node_modules' -prune -o \
  -path './website/node_modules' -prune -o \
  -path './website/build' -prune -o \
  -path './website/.docusaurus' -prune -o \
  \( -name '*.md' -o -name '*.mdx' \) -print)

count=$(printf '%s\n' "$ALL_DOCS" | grep -c .)
echo "→ scanning $count doc files"

# --- Check 1: anti-marketing -------------------------------------------------
echo
echo "→ check 1: anti-marketing language"
MARKETING_PATTERN='production-ready|enterprise-grade|battle-tested'
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  if is_exempt "$f"; then continue; fi
  # Capture lines matching any forbidden phrase. Strip the ones that are
  # explicitly negated ("not ... production-ready"). The negation check
  # accepts English ("not yet"/"isn't") and Spanish ("no es"/"todavía no").
  matches=$(grep -niE "$MARKETING_PATTERN" "$f" 2>/dev/null || true)
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    # Negation patterns that flag the phrase as a "not this" disclaimer
    # rather than a marketing claim. Covers English ("not yet",
    # "Not v1", "isn't"), Spanish ("no es", "todavía no", "tampoco",
    # "no v1"), and the explicit warning shape ("not yet v1.0").
    if grep -qiE "(not (yet|a|v[0-9])|not v[0-9]|isn't|no es|todavía no|tampoco|never|no v[0-9])" <<<"$line"; then
      continue
    fi
    report "  marketing-language: $f:$line"
  done <<<"$matches"
done <<<"$ALL_DOCS"

# --- Check 2: RELEASE_NOTES_V1 leak ------------------------------------------
echo
echo "→ check 2: RELEASE_NOTES_V1 leak"
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  if is_exempt "$f"; then continue; fi
  matches=$(grep -nE 'RELEASE_NOTES_V1' "$f" 2>/dev/null || true)
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    report "  release-notes-v1-leak: $f:$line"
  done <<<"$matches"
done <<<"$ALL_DOCS"

# --- Check 3: broken relative links ------------------------------------------
echo
echo "→ check 3: broken relative links"
# Matches markdown link `[text](./path)` or `[text](../path)` or
# `[text](path)` where path doesn't start with http(s):// or # or mailto.
# We extract the path, strip the `#anchor` suffix, and check file
# existence relative to the source file's directory.
while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  # versioned_docs are historical snapshots — broken links there don't
  # block merges (older versions might reference paths that have since
  # moved). Skip.
  case "$f" in *'/versioned_docs/'*) continue ;; esac

  dir=$(dirname "$f")
  # The python helper is the most robust way to tokenize markdown link
  # syntax. Fall back to perl if python3 is missing.
  python3 - "$f" "$dir" <<'PY'
import re, sys, os
src, base = sys.argv[1], sys.argv[2]
with open(src, 'r', encoding='utf-8', errors='replace') as fh:
    content = fh.read()
# Strip fenced code blocks so embedded paths inside ``` don't trigger
# false positives.
content = re.sub(r'```.*?```', '', content, flags=re.S)
content = re.sub(r'`[^`]*`', '', content)

# Docusaurus aware: a link target like `guides/querying` (no extension)
# is auto-resolved by Docusaurus to one of `guides/querying.mdx`,
# `guides/querying.md`, `guides/querying/index.mdx`, etc. A link like
# `/docs/reference/api/errors` is a baseUrl-rooted Docusaurus path that
# corresponds to `website/docs/reference/api/errors.{md,mdx}` on disk.
# We try all those forms in order; pass if any resolves.
DOC_EXTS = ['', '.md', '.mdx', '/index.md', '/index.mdx']
def resolve(target_path):
    if target_path.startswith('/docs/'):
        # Docusaurus baseUrl + /docs/ → website/docs/...
        rel = target_path[len('/docs/'):]
        candidates = [os.path.join('website/docs', rel + ext) for ext in DOC_EXTS]
    elif target_path.startswith('/'):
        # Repo-absolute path.
        candidates = ['.' + target_path + ext for ext in DOC_EXTS]
    else:
        # Relative to source dir.
        joined = os.path.normpath(os.path.join(base, target_path))
        candidates = [joined + ext for ext in DOC_EXTS]
    return any(os.path.exists(c) for c in candidates)

pat = re.compile(r'\]\(([^)]+)\)')
fail = []
for m in pat.finditer(content):
    target = m.group(1).strip()
    if target.startswith(('http://', 'https://', 'mailto:', '#', 'data:')):
        continue
    # Strip query / fragment.
    path = target.split('#', 1)[0].split('?', 1)[0]
    if not path:
        continue
    if not resolve(path):
        fail.append(f"  broken-link: {src} -> {target}")
for line in fail:
    print(line)
PY
done <<<"$ALL_DOCS" | while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  printf '%s\n' "$line"
done > /tmp/lint-docs-broken-links.$$ 2>&1
if [[ -s /tmp/lint-docs-broken-links.$$ ]]; then
  while IFS= read -r line; do
    report "$line"
  done < /tmp/lint-docs-broken-links.$$
fi
rm -f /tmp/lint-docs-broken-links.$$

# ----------------------------------------------------------------------------

echo
if [[ "$FAIL" -ne 0 ]]; then
  echo "✗ lint-docs found issues. Fix them or update the exemption list with intent."
  exit 1
fi
echo "✓ lint-docs clean."
