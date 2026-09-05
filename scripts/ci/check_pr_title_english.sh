#!/usr/bin/env bash
# check_pr_title_english.sh — the squash title of a PR is the changelog line
# and the release-note bullet of a product whose site and README are in
# English (QADR-0007). A Spanish title ships a Spanish release note (QM-18,
# maturity audit 2026-09-03). This rejects the obvious tells: accented
# vowels, ñ, inverted marks, and the Spanish function words no English title
# uses on their own.
#
# Usage: PR_TITLE="..." bash scripts/ci/check_pr_title_english.sh
#        bash scripts/ci/check_pr_title_english.sh "feat(router): ..."
set -uo pipefail
title="${1:-${PR_TITLE:-}}"
if [[ -z "$title" ]]; then
  echo "check_pr_title_english: no title (pass it as \$1 or PR_TITLE)" >&2
  exit 2
fi
lower=$(printf '%s' "$title" | tr '[:upper:]' '[:lower:]')
if printf '%s' "$lower" | grep -qE '[áéíóúñ¿¡]'; then
  echo "FAIL: the PR title carries Spanish characters — write it in English (QADR-0007): $title" >&2
  exit 1
fi
# Whole words only; the subject after the conventional prefix is what matters.
subject=$(printf '%s' "$lower" | sed -E 's/^[a-z]+(\([^)]*\))?!?:[[:space:]]*//')
for w in de la el los las del para con sin que por una y hacia según sólo también; do
  if printf ' %s ' "$subject" | grep -qE "[^a-z]$w[^a-z]"; then
    echo "FAIL: the PR title reads as Spanish (« $w ») — write it in English (QADR-0007): $title" >&2
    exit 1
  fi
done
echo "OK: PR title in English"
