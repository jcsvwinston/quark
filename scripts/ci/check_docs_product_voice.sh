#!/usr/bin/env bash
# check_docs_product_voice.sh — keeps internal engineering vocabulary out of
# the published documentation.
#
# The website is the product's shop window. A reader evaluating Quark does not
# know what ADR-0021 is, what "P0" means, that SPEC.md or CLAUDE.md exist, or
# what a "v1 gate" gates. Those are artefacts of how we build the thing, not of
# how anyone uses it. When they leak into website/docs/** they make the docs
# read like an internal ticket queue — the reader has to decode our process
# before they can learn our product.
#
# This check fails the build when a banned term appears under website/docs/**.
# The rule is about the published surface only: docs/adr/, SPEC.md, CLAUDE.md
# and the rest of the internal tree are untouched — write whatever you like
# there, just do not point the public at it.
#
# Escape hatch: some hits are legitimate prose ("the CI drift gate", a version
# heading like "P1 support"). Put
#
#     <!-- docs-lint-allow -->
#
# on the line immediately before, and that single line is exempt. Use it for
# genuine false positives, not to smuggle jargon back in.
set -uo pipefail

cd "$(dirname "$0")/../.."

DOCS_DIR="${1:-website/docs}"

if [[ ! -d "$DOCS_DIR" ]]; then
  echo "check_docs_product_voice: $DOCS_DIR does not exist; nothing to check"
  exit 0
fi

# Each rule is "regex<TAB>what to write instead".
RULES=$(cat <<'EOF'
ADR-[0-9]+	Explain the decision in prose. The reader cannot open our ADRs and should not need to.
\bP[0-3]\b	Internal priority label. Drop it, or say plainly how important this is.
\bV1_GATE\b	Internal release-gate document. Say what is or is not supported instead.
\bSPEC\.md\b	Internal document. Link a public page, or restate the rule here.
\bCLAUDE\.md\b	Internal document. Never reference it from published docs.
\bTASKS\.md\b	Internal document. Never reference it from published docs.
\bPROFILING\.md\b	Internal document. Publish the numbers instead of pointing at the file.
\bANALISIS_MADUREZ\.md\b	Internal document. Never reference it from published docs.
\baudit backlog\b	Internal process vocabulary. Say what changed, not where it was tracked.
\(#[0-9]+\)	Bare issue number. Readers cannot resolve it; describe the change instead.
EOF
)

status=0
found=0

while IFS=$'\t' read -r pattern advice; do
  [[ -z "$pattern" ]] && continue

  # -n for line numbers so we can look at the preceding line for the allow marker.
  while IFS= read -r hit; do
    [[ -z "$hit" ]] && continue
    file="${hit%%:*}"
    rest="${hit#*:}"
    line="${rest%%:*}"
    text="${rest#*:}"

    # Allow marker on the immediately preceding line exempts this hit.
    if [[ "$line" -gt 1 ]]; then
      prev=$(sed -n "$((line - 1))p" "$file")
      if [[ "$prev" == *"docs-lint-allow"* ]]; then
        continue
      fi
    fi

    if [[ $found -eq 0 ]]; then
      echo "Internal vocabulary found in the published docs:" >&2
      echo >&2
      found=1
    fi
    status=1
    printf '  %s:%s\n    %s\n    → %s\n\n' \
      "$file" "$line" "$(echo "$text" | sed 's/^[[:space:]]*//' | cut -c1-100)" "$advice" >&2
  done < <(grep -rnE "$pattern" "$DOCS_DIR" --include='*.md' --include='*.mdx' 2>/dev/null)
done <<< "$RULES"

if [[ $status -ne 0 ]]; then
  echo "Fix the lines above, or mark a genuine false positive with an HTML comment" >&2
  echo "<!-- docs-lint-allow --> on the line before it." >&2
  exit 1
fi

echo "OK: no internal vocabulary in $DOCS_DIR"
