#!/usr/bin/env bash
# check_action_pins.sh — every GitHub Action this repository runs is pinned to
# a commit SHA, with its tag written next to it.
#
# A tag is a moving pointer owned by someone else: `actions/checkout@v4` runs
# whatever the owner of that tag decides to publish under it, in a job that
# has this repository checked out. A 40-hex SHA is immutable, which is what
# OpenSSF Scorecard's Pinned-Dependencies check asks for and, more to the
# point, what makes "which code ran in that release" answerable after the
# fact.
#
# The trailing `# vX.Y.Z` comment is not decoration: it is what makes the pin
# readable and what Dependabot rewrites when it proposes the next SHA (the
# github-actions ecosystem is declared in .github/dependabot.yml). A pin
# without a bot behind it is worse than a tag — it freezes the action at the
# day it was written, security fixes included — so the comment and the
# Dependabot entry are part of the rule, not a nicety.
#
# Usage: bash scripts/ci/check_action_pins.sh
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

status=0
found=0

while IFS= read -r line; do
  file=${line%%:*}
  rest=${line#*:}
  lineno=${rest%%:*}
  text=${rest#*:}

  # `uses: ./path` (an action living in this tree) and `uses: docker://…` are
  # not tag references and have nothing to pin.
  ref=$(printf '%s' "$text" | sed -nE 's/.*uses:[[:space:]]*([^[:space:]#]+).*/\1/p')
  case "$ref" in
    ./*|docker://*|'') continue ;;
  esac

  found=$((found + 1))

  version=${ref##*@}
  action=${ref%@*}

  if [ "$version" = "$ref" ]; then
    echo "FAIL: $file:$lineno: $ref has no version at all — pin it to a commit SHA" >&2
    status=1
    continue
  fi

  if ! printf '%s' "$version" | grep -qE '^[0-9a-f]{40}$'; then
    echo "FAIL: $file:$lineno: $action is pinned to '$version', which is a moving tag — use the commit SHA it points at today, with the tag in a trailing comment" >&2
    status=1
    continue
  fi

  if ! printf '%s' "$text" | grep -qE '#[[:space:]]*v?[0-9]'; then
    echo "FAIL: $file:$lineno: $action@$version carries no '# <tag>' comment — without it nobody (Dependabot included) can tell which release this SHA is" >&2
    status=1
  fi
# Anchored: a `uses:` KEY, not the word inside a comment (this file's own
# rationale is written in those comments, and a first version of this check
# flagged them).
done < <(grep -rnE '^[[:space:]]*(-[[:space:]]+)?uses:[[:space:]]' .github/workflows/ 2>/dev/null)

if [ "$found" -eq 0 ]; then
  echo "FAIL: no 'uses:' reference found under .github/workflows/ — did the layout change?" >&2
  exit 1
fi

if [ "$status" -ne 0 ]; then
  echo >&2
  echo "Resolve the tag with: gh api repos/<owner>/<action>/commits/<tag> --jq .sha" >&2
  echo "and write: uses: <owner>/<action>@<40-hex-sha> # <tag>" >&2
  exit 1
fi

echo "action pins OK: $found 'uses:' references, all pinned by commit SHA with their tag written next to them"
