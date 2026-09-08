#!/usr/bin/env bash
# gen_release_notes_skeleton.sh — writes the release-notes skeletons the
# release checklist demands, out of what release-please already generated.
#
# Idempotent: if the section or the file for the manifest's version already
# exist, it leaves them alone. Meant to run ON the release PR's branch, where
# the manifest already points at the new version and the CHANGELOG already has
# its entry:
#
#   bash scripts/release/gen_release_notes_skeleton.sh
#
# It writes:
#   - the '## vX.Y.Z' section in website/docs/reference/release-notes.mdx
#     (a skeleton with the CHANGELOG bullets; the prose is edited by hand —
#     the anti-hype rule is not delegated to a script)
#   - docs/RELEASE_NOTES_vX.Y.Z.md (same skeleton)
#   - the rows of SECURITY.md's supported-versions table
#   - CLAUDE.md's marked line and the README's release-notes pointer
#
# The version MENTIONS (README/SECURITY/CLAUDE/site header) are bumped by
# release-please alone, via extra-files + x-release-please-version.
#
# SECURITY.md's TABLE is the exception: its rows are content, not a version
# mention, and release-please leaves them alone while
# check-version-coherence.sh demands they name exactly the minors of the
# manifest. They are written here — see write_supported_versions below.
# `--self-test` exercises that rewrite against synthetic tables; it runs in
# the Lint lane and in `make check`.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

# ---------------------------------------------------------------------------
# write_supported_versions <file> "<minors>" — SECURITY.md's supported-versions
# table.
#
# release-please rewrites the x-release-please-version marker line above the
# table and nothing else; the ✅ rows are content, and
# check-version-coherence.sh demands they name EXACTLY the minors the manifest
# resolves to. An equality nobody can satisfy by hand is a release train that
# stops at the guard on every minor, so the rows are written here — on the same
# branch, from the same manifest the guard reads.
#
# The set comes from the guard itself (`--supported-minors`), so there is one
# implementation of "which minors are supported" and not two that can drift.
#
# The rows are REWRITTEN, not appended: a fossil row, a missing row and a row
# demoted to ❌ are three ways of lying and a single edit. Rows that name no
# version (`main`, `Older tags`) keep their place and their text — the rule
# speaks about versions.
#
# The major boundary is the one thing this cannot derive: right after vX.0.0
# the second supported line is the last minor of the previous major, which the
# manifest does not record. ONE ✅ row of that major survives, the one naming
# its last minor — the policy covers two lines, and the guard accepts any minor
# of the previous one. If there is none, the script says so instead of
# inventing it.
# ---------------------------------------------------------------------------
write_supported_versions() {
  local file=$1 minors=$2
  # shellcheck disable=SC2086  # $minors is deliberately a list of arguments
  python3 - "$file" $minors <<'PY_SEC'
import re
import sys

path = sys.argv[1]
required = sys.argv[2:]  # e.g. ['v1.13', 'v1.12'], current one first
if not required:
    sys.exit("ERROR: write_supported_versions called with no minors")

cur_major, cur_minor = (int(x) for x in required[0][1:].split('.'))

lines = open(path).read().split('\n')

header = None
for i, line in enumerate(lines):
    if re.match(r'^\|\s*Version\s*\|', line, re.I):
        header = i
        break
if header is None:
    sys.exit("ERROR: %s has no '| Version | Supported |' table to write" % path)
if header + 1 >= len(lines) or not re.match(r'^\|[-: |]+\|\s*$', lines[header + 1]):
    sys.exit("ERROR: the table in %s has no separator row" % path)

end = header + 1
while end + 1 < len(lines) and lines[end + 1].startswith('|'):
    end += 1

table = lines[header:end + 1]
head, body = table[:2], table[2:]


def versions_in(row):
    return re.findall(r'v(\d+)\.(\d+)', row)


template = next((r for r in body if '✅' in r and versions_in(r)), None)


def row_for(minor_tag):
    if template is None:
        return '| `%s.x` | ✅ |' % minor_tag
    return re.sub(r'v\d+\.\d+', minor_tag, template, count=1)


# The version rows are replaced IN PLACE: what came before them (`main`) stays
# above, what came after (`Older tags`, ❌) stays below. A row with no version
# and no ✅ already opens the lower half, so a table that names no version yet
# does not get the new rows written under the ❌.
before, keep_prev_major, after = [], [], []
in_head_rows = True
for row in body:
    found = versions_in(row)
    if found:
        in_head_rows = False
        if (cur_minor == 0 and '✅' in row
                and all(int(maj) == cur_major - 1 for maj, _ in found)):
            keep_prev_major.append((max(int(mi) for _, mi in found), row))
        continue
    if in_head_rows and '✅' in row:
        before.append(row)
    else:
        in_head_rows = False
        after.append(row)

# The policy covers TWO lines, so a single row of the previous major survives:
# the one naming its last minor. The rest are fossils like any other.
prev_major_rows = [row for _, row in sorted(keep_prev_major, reverse=True)[:1]]

new_table = head + before + [row_for(m) for m in required] + prev_major_rows + after
if new_table == table:
    print('ok: SECURITY.md already lists the supported minors (%s)' % ' '.join(required))
else:
    lines[header:end + 1] = new_table
    open(path, 'w').write('\n'.join(lines))
    print('written: SECURITY.md supported-versions rows -> %s'
          % ', '.join(m + '.x' for m in required))

if cur_minor == 0 and not prev_major_rows:
    print('WARNING: %s names no minor of the previous major; the policy covers '
          'two lines - add that row by hand' % path, file=sys.stderr)
PY_SEC
}

# Self-test of the rewrite above. A writer of docs fails the same way a guard
# of docs does: quietly. These are the tables check-version-coherence.sh's own
# self-test rejects, seen from the side that fixes them.
if [ "${1:-}" = "--self-test" ]; then
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  st_fail=0

  st() {
    # $1 = case name, $2 = released version, $3 = the table under test,
    # $4 = the ✅ minors expected after the rewrite, in order.
    local minors got before_sum after_sum
    printf '%s\n' "$3" > "$tmp/SECURITY.md"
    minors=$(bash scripts/check-version-coherence.sh --supported-minors "$2")
    if ! write_supported_versions "$tmp/SECURITY.md" "$minors" >/dev/null 2>&1; then
      echo "SELF-TEST FAIL: $1 — the writer exited with an error" >&2
      st_fail=1
      return
    fi
    got=$(grep '✅' "$tmp/SECURITY.md" | grep -oE 'v[0-9]+\.[0-9]+' | tr '\n' ' ' | sed 's/ $//')
    if [ "$got" != "$4" ]; then
      echo "SELF-TEST FAIL: $1 — expected '$4', got '$got'" >&2
      st_fail=1
      return
    fi
    # Idempotence: the second pass must not touch the file.
    before_sum=$(cksum < "$tmp/SECURITY.md")
    write_supported_versions "$tmp/SECURITY.md" "$minors" >/dev/null 2>&1 || true
    after_sum=$(cksum < "$tmp/SECURITY.md")
    if [ "$before_sum" != "$after_sum" ]; then
      echo "SELF-TEST FAIL: $1 — the second pass changed the file" >&2
      st_fail=1
      return
    fi
    echo "self-test OK: $1"
  }

  st "an already exact table is left alone" 1.12.0 '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |
| Older tags | ❌ — please upgrade |' "v1.12 v1.11"
  st "fossil minors rewritten" 1.12.0 '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.2.x` | ✅ |
| `v1.1.x` | ✅ |
| Older tags | ❌ |' "v1.12 v1.11"
  st "one minor behind the manifest (the release train case)" 1.13.0 '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |
| Older tags | ❌ |' "v1.13 v1.12"
  st "the previous minor demoted to ❌" 1.12.0 '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ❌ |
| Older tags | ❌ |' "v1.12 v1.11"
  st "a table that only describes the policy" 1.12.0 '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| Older tags | ❌ |' "v1.12 v1.11"
  st "the first release of a major keeps the previous line" 2.0.0 '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |
| Older tags | ❌ |' "v2.0 v1.12"

  # A table with no version rows gets the new ones BETWEEN `main` and the ❌,
  # never under the ❌: the order matters as much as the set.
  printf '%s\n' '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| Older tags | ❌ — please upgrade |' > "$tmp/SECURITY.md"
  write_supported_versions "$tmp/SECURITY.md" "$(bash scripts/check-version-coherence.sh --supported-minors 1.12.0)" >/dev/null
  expected_empty='| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.12.x` | ✅ |
| `v1.11.x` | ✅ |
| Older tags | ❌ — please upgrade |'
  if [ "$(cat "$tmp/SECURITY.md")" != "$expected_empty" ]; then
    echo "SELF-TEST FAIL: the new rows did not land in their place:" >&2
    diff <(printf '%s\n' "$expected_empty") "$tmp/SECURITY.md" >&2 || true
    st_fail=1
  else
    echo "self-test OK: the new rows land between main and the ❌"
  fi

  # Rows that name no version survive with their text: the rule speaks about
  # versions, not about the whole table.
  printf '%s\n' '| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.11.x` | ✅ |
| `v1.10.x` | ✅ |
| Older tags | ❌ — please upgrade |' > "$tmp/SECURITY.md"
  write_supported_versions "$tmp/SECURITY.md" "$(bash scripts/check-version-coherence.sh --supported-minors 1.13.0)" >/dev/null
  expected='| Version | Supported |
|---------|-----------|
| `main` | ✅ |
| `v1.13.x` | ✅ |
| `v1.12.x` | ✅ |
| Older tags | ❌ — please upgrade |'
  if [ "$(cat "$tmp/SECURITY.md")" != "$expected" ]; then
    echo "SELF-TEST FAIL: the version-less rows did not survive intact:" >&2
    diff <(printf '%s\n' "$expected") "$tmp/SECURITY.md" >&2 || true
    st_fail=1
  else
    echo "self-test OK: version-less rows keep their place and text"
  fi

  if [ "$st_fail" -ne 0 ]; then
    exit 1
  fi
  echo "gen_release_notes_skeleton self-test OK"
  exit 0
fi

version=$(python3 -c "import json;print(json.load(open('.release-please-manifest.json'))['.'])")
tag="v${version}"
today=$(date +%Y-%m-%d)
site="website/docs/reference/release-notes.mdx"
notes="docs/RELEASE_NOTES_${tag}.md"

# Extrae la entrada del CHANGELOG para esta versión (entre su cabecera y la
# siguiente '## [').
changelog_body=$(awk -v ver="$version" '
  $0 ~ "^## \\[" ver "\\]" {inside=1; next}
  inside && /^## \[/ {exit}
  inside {print}
' CHANGELOG.md)

if [ -z "$changelog_body" ]; then
  echo "ERROR: CHANGELOG.md no tiene entrada para ${version} — corre esto en la rama del release PR." >&2
  exit 1
fi

if ! grep -q "^## ${tag}\$" "$site"; then
  python3 - "$site" "$tag" "$today" <<'PY'
import sys, re
site, tag, today = sys.argv[1], sys.argv[2], sys.argv[3]
s = open(site).read()
m = re.search(r'^## v\d', s, re.M)
skeleton = f"""## {tag}

<!-- TODO: una frase honesta de qué es este release (patch/minor y de qué). -->

### Changed

<!-- TODO: redacta desde el CHANGELOG (bullets abajo son el material crudo,
     no la prosa final): -->

"""
s = s[:m.start()] + skeleton + s[m.start():]
open(site, 'w').write(s)
PY
  # Añade los bullets del CHANGELOG como material crudo comentado
  python3 - "$site" "$tag" <<PY
import sys
site, tag = sys.argv[1], sys.argv[2]
raw = '''${changelog_body}'''
s = open(site).read()
s = s.replace('no la prosa final): -->\n', 'no la prosa final): -->\n\n<!--\n' + raw.strip() + '\n-->\n', 1)
open(site, 'w').write(s)
PY
  echo "escrito: sección ${tag} (esqueleto) en ${site}"
else
  echo "ok: ${site} ya tiene la sección ${tag}"
fi

if [ ! -f "$notes" ]; then
  {
    echo "# Release notes — ${tag}"
    echo
    echo "<!-- TODO: una frase honesta de qué es este release. -->"
    echo
    echo "Docs: <https://jcsvwinston.github.io/quantum/quark/intro/>"
    echo
    echo "## Changed"
    echo
    echo "<!-- Material crudo del CHANGELOG; redacta la prosa final: -->"
    echo "<!--"
    echo "$changelog_body" | sed 's/^/  /'
    echo "-->"
  } > "$notes"
  echo "escrito: ${notes} (esqueleto)"
else
  echo "ok: ${notes} ya existe"
fi

# CLAUDE.md queda FUERA de los extra-files de release-please a propósito: su
# updater genérico reemplaza todas las apariciones de la versión anterior en
# el fichero, y este cita versiones pasadas en su línea de historial — la
# reescribía, falseando el registro. Aquí se bumpa solo la línea marcada.
if grep -q 'x-release-please-version' CLAUDE.md 2>/dev/null; then
  python3 - "$version" <<'PY_BUMP'
import re, sys
version = sys.argv[1]
lines = open("CLAUDE.md").read().split("\n")
for i, line in enumerate(lines):
    if "x-release-please-version" in line:
        lines[i] = re.sub(r"v\d+\.\d+\.\d+", f"v{version}", line, count=1)
        break
open("CLAUDE.md", "w").write("\n".join(lines))
print(f"bumpeada la línea marcada de CLAUDE.md a v{version}")
PY_BUMP
fi

# SECURITY.md's supported-versions table, from the manifest and with the set
# the guard dictates (see write_supported_versions above).
write_supported_versions SECURITY.md "$(bash scripts/check-version-coherence.sh --supported-minors "$version")"

# El README apunta a las notas narrativas de LA MINOR ACTUAL («for the current
# line»); check-version-coherence.sh exige el enlace. En una minor nueva el
# puntero se mueve aquí; en un patch la minor no cambia y no hay nada que mover.
minor_notes="docs/RELEASE_NOTES_v${version%.*}.0.md"
if ! grep -qF "$minor_notes" README.md; then
  prev=$(grep -oE 'docs/RELEASE_NOTES_v[0-9]+\.[0-9]+\.0\.md' README.md | sort -uV | tail -1)
  if [ -n "$prev" ]; then
    sed -i.bak "s#${prev}#${minor_notes}#g" README.md && rm -f README.md.bak
    echo "README.md: puntero de notas ${prev} → ${minor_notes}"
  else
    echo "AVISO: README.md no enlaza ninguna docs/RELEASE_NOTES_vX.Y.0.md; enlaza ${minor_notes} a mano" >&2
  fi
fi

echo "Recuerda: la prosa final es tuya. El guard de coherencia valida menciones y sección."
