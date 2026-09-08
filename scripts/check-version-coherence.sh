#!/usr/bin/env bash
# Version-coherence guard (H-Q6). At v1.2.0 the release went out while
# README/SECURITY/CLAUDE.md/release-notes still said v1.1.5/v1.1.2/v1.1.0 —
# nothing forced the docs to move with the tag. This check makes the
# release-please PR (which bumps .release-please-manifest.json) fail until
# the user-facing version mentions are bumped in the same PR, and it keeps
# them from drifting afterwards.
set -euo pipefail

cd "$(dirname "$0")/.."

# ---------------------------------------------------------------------------
# SECURITY.md's supported-versions table (DI-2, tightened by the A3 audit).
#
# The table is the promise a consumer reads before deciding whether their tag
# still gets security fixes. release-please bumps the "Quark is vX.Y.Z" marker
# line above it and nothing else: the rows are content, not a version mention.
# The first version of this check only rejected FOSSIL rows (a minor older
# than the policy still marked supported), which left the two ways the table
# can lie uncovered: saying nothing verifiable at all ("Latest two tagged
# minors", true forever and useless to a reader holding v1.4.2), and falling
# one minor behind without naming an old one.
#
# So the rule is now an equality, not a lower bound: the ✅ rows must name
# exactly the minors the manifest resolves to — the current one and the one
# before it, which is what the sentence above the table promises.
#
# An equality nobody can satisfy by hand would stop the release train, so the
# rows are written on the release branch by
# scripts/release/gen_release_notes_skeleton.sh, which asks THIS file for the
# set (--supported-minors) instead of reimplementing the rule.
#
# Kept as a function because the self-test below feeds it tables that do not
# exist in the tree; a guard that cannot be seen failing is a guard nobody
# knows is broken.
# ---------------------------------------------------------------------------

# The minors the policy covers for a released version: the current one and the
# one before it. Right after a major bump (X.0.0) only the current one can be
# derived — the manifest does not record the last minor of the previous major.
supported_minors() {
  local released=$1 major rest minor out
  major=${released%%.*}
  rest=${released#*.}
  minor=${rest%%.*}
  out="v${major}.${minor}"
  if [ "$minor" -gt 0 ]; then
    out="$out v${major}.$((minor - 1))"
  fi
  printf '%s\n' "$out"
}

if [ "${1:-}" = "--supported-minors" ]; then
  if [ -z "${2:-}" ]; then
    echo "usage: $0 --supported-minors X.Y.Z" >&2
    exit 1
  fi
  supported_minors "$2"
  exit 0
fi

check_supported_versions() {
  local file=$1
  local released=$2
  local major rest minor listed required v v_major v_minor ok local_status
  major=${released%%.*}
  rest=${released#*.}
  minor=${rest%%.*}
  local_status=0

  required=$(supported_minors "$released")

  # Every version named in a row marked supported. `v1.12.x` and `v1.12.0`
  # both reduce to the minor, which is the granularity the policy speaks in.
  listed=$(grep '✅' "$file" | grep -oE 'v[0-9]+\.[0-9]+' | sort -u | tr '\n' ' ' || true)

  for v in $listed; do
    v_major=${v#v}; v_major=${v_major%%.*}
    v_minor=${v##*.}
    ok=0
    if [ "$v_major" -eq "$major" ]; then
      if [ "$v_minor" -eq "$minor" ]; then
        ok=1
      elif [ "$minor" -gt 0 ] && [ "$v_minor" -eq $((minor - 1)) ]; then
        ok=1
      fi
    elif [ "$minor" -eq 0 ] && [ "$v_major" -eq $((major - 1)) ]; then
      # Right after a major bump the second supported line is the last minor
      # of the previous major, and the manifest does not record which one that
      # was. Any minor of that line is accepted here; the current one is still
      # demanded below.
      ok=1
    fi
    if [ "$ok" -ne 1 ]; then
      echo "ERROR: ${file} marks ${v}.x as supported, but the released version is v${released} — the policy covers the current minor and the one before it" >&2
      local_status=1
    fi
  done

  for v in $required; do
    case " $listed " in
      *" $v "*) ;;
      *)
        echo "ERROR: ${file} does not list ${v}.x as supported (the version in .release-please-manifest.json is ${released}) — write one table row per supported minor" >&2
        local_status=1
        ;;
    esac
  done

  return $local_status
}

# Self-test: proves the check above rejects the four ways the table has drifted
# or could drift. Runs in CI next to the guard itself (`--self-test`), because
# the failure mode of a docs guard is passing quietly.
if [ "${1:-}" = "--self-test" ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  st_fail=0

  expect() {
    # $1 = expected exit status, $2 = case name, $3 = released version,
    # $4 = the table under test.
    printf '%s\n' "$4" > "$tmp/SECURITY.md"
    if check_supported_versions "$tmp/SECURITY.md" "$3" >/dev/null 2>&1; then
      got=0
    else
      got=1
    fi
    if [ "$got" -ne "$1" ]; then
      echo "SELF-TEST FAIL: $2 — expected exit $1, got $got" >&2
      st_fail=1
    else
      echo "self-test OK: $2"
    fi
  }

  expect 0 "the current and previous minors, written out" 1.12.0 '| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |
| Older tags | ❌ |'
  expect 1 "a table that only describes the policy (no version to check)" 1.12.0 '| `main` | ✅ |
| Latest two tagged minors | ✅ |
| Older tags | ❌ |'
  expect 1 "fossil minors left as supported" 1.12.0 '| `main` | ✅ |
| `v1.2.x` | ✅ |
| `v1.1.x` | ✅ |'
  expect 1 "one minor behind the manifest" 1.12.0 '| `main` | ✅ |
| `v1.11.x` | ✅ |
| `v1.10.x` | ✅ |'
  expect 1 "the previous minor demoted to unsupported" 1.12.0 '| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ❌ |'
  expect 1 "a minor the manifest has not released yet" 1.12.0 '| `main` | ✅ |
| `v1.13.x` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |'
  expect 0 "the first release of a new major keeps the previous line" 2.0.0 '| `main` | ✅ |
| `v2.0.x` | ✅ |
| `v1.12.x` | ✅ |'

  if [ "$st_fail" -ne 0 ]; then
    exit 1
  fi
  echo "SECURITY.md supported-versions self-test OK"
  exit 0
fi

version=$(sed -nE 's/.*"\.": *"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' .release-please-manifest.json)
if [ -z "$version" ]; then
  echo "could not read the version from .release-please-manifest.json" >&2
  exit 1
fi

fail=0

require_mention() {
  local file=$1
  if ! grep -q "v${version}" "$file"; then
    echo "ERROR: ${file} does not mention v${version} (the version in .release-please-manifest.json)" >&2
    fail=1
  fi
}

# The user-facing files that state "Quark is vX.Y.Z".
require_mention README.md
require_mention SECURITY.md
require_mention CLAUDE.md
require_mention website/docs/reference/release-notes.mdx

# Guard de heading en release-notes (NU7-1 aplicado a quark). La página usa
# el patrón «versión actual arriba + una sección '## vX.Y.Z' por release»;
# el require_mention de arriba solo exige que la cadena v${version} aparezca
# EN ALGUNA PARTE, así que el estado «release-please subió el manifest pero
# nadie escribió la sección» pasaba el guard con la mención del índice.
# Exigimos el heading de la versión del manifest (con o sin título detrás).
release_notes="website/docs/reference/release-notes.mdx"
if ! grep -qE "^## v${version//./\\.}( |$)" "$release_notes"; then
  echo "ERROR: ${release_notes} no tiene una sección '## v${version}' (la versión del manifest necesita sus release notes)" >&2
  fail=1
fi

# Narrative release notes must exist for the current minor.
minor_notes="docs/RELEASE_NOTES_v${version%.*}.0.md"
if [ ! -f "$minor_notes" ]; then
  echo "ERROR: ${minor_notes} does not exist (narrative notes for the current minor)" >&2
  fail=1
fi

# El README enlaza las notas de LA MINOR ACTUAL. El require_mention de arriba
# solo exige que la cadena v${version} aparezca en alguna parte del README, y
# la línea del marcador x-release-please-version — que release-please bumpa
# sola — ya la satisface: todo lo que hay debajo puede envejecer sin que nada
# proteste. Pasó con el puntero «for the current line», que se quedó en
# v1.6.1 dos minors después (y con los párrafos narrativos que colgaban de
# él). El fichero ya se exige arriba; aquí se exige que el README APUNTE a él.
if ! grep -qF "$minor_notes" README.md; then
  echo "ERROR: README.md no enlaza ${minor_notes} — el puntero de la línea actual quedó en una minor anterior" >&2
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo >&2
  echo "Release checklist: bump the version mentions above and add the minor's RELEASE_NOTES file." >&2
  exit 1
fi

echo "version coherence OK: v${version} mentioned in README/SECURITY/CLAUDE/release-notes, '## v${version}' section present, ${minor_notes} present and linked from README"

# ---------------------------------------------------------------------------
# Roadmap sin versiones (QK6-5). QK5-2 quitó la versión hardcodeada del
# roadmap pero no dejó guard de reincidencia: si alguien vuelve a escribir
# «Quark is vX.Y.Z» ahí, nada lo cazaría hasta la siguiente auditoría.
# ---------------------------------------------------------------------------
if grep -nE 'v[0-9]+\.[0-9]+\.[0-9]+' website/docs/reference/roadmap.mdx; then
  echo "ERROR: website/docs/reference/roadmap.mdx contiene una versión hardcodeada (las versiones viven en release-notes, que sí tiene guard)" >&2
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "roadmap OK: sin versiones hardcodeadas"

# ---------------------------------------------------------------------------
# SECURITY.md's supported-versions table matches the manifest. With v1.7.1 out,
# the table still said v1.2.x/v1.1.x — the policy sentence was right, the
# numbers were five minors old, and the require_mention above cannot see it
# (it only demands that the CURRENT version appears somewhere). The rule now
# lives in check_supported_versions() at the top of this file, together with
# the self-test that proves it fails.
# ---------------------------------------------------------------------------
if ! check_supported_versions SECURITY.md "$version"; then
  echo >&2
  echo "Write the rows from the manifest with 'bash scripts/release/gen_release_notes_skeleton.sh'" >&2
  echo "(it runs on the release branch, and the release train runs it there for you)." >&2
  exit 1
fi
echo "SECURITY.md OK: the supported-versions table names exactly the minors v${version} resolves to"
