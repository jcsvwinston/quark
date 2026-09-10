#!/usr/bin/env bash
# link_workspace.sh [module dir...] — write the go.work that points the nested
# modules' version requirements at THIS tree.
#
# The CLI and the five driver modules require the library BY VERSION and
# cannot carry a `replace`: `go install` refuses a published module that has
# one (ADR-0024). A workspace is what points those requirements at the tree
# under review — otherwise a lane would test this PR's code against the
# PREVIOUS release of the library.
#
# `go work init` alone does not finish the job, and the gap only shows when
# the required version does not exist yet. Listing a module in the workspace
# makes it PROVIDE its packages locally, but the required version still enters
# the module graph, and Go reads its go.mod to build that graph. So a
# requirement naming a tag the proxy does not have fails the lane:
#
#   github.com/jcsvwinston/quark@v1.13.0: reading .../go.mod at revision
#   v1.13.0: unknown revision v1.13.0
#
# That is not a hypothetical. The train that moved the CLI out of the root
# module had to name the tag it was about to cut: every root tag up to
# v1.12.0 still contains cmd/quark, so a CLI module requiring one of them
# leaves its own package provided by two modules, and Go refuses with
# `ambiguous import`. The floor has to name the version being released, and
# that version exists only after the merge — which is precisely when the lane
# that must approve the merge runs.
#
# The fix is a VERSIONED replace in the workspace: `module@version => dir`.
# It resolves the requirement from the tree without ever asking the proxy,
# and it lives in go.work, which is generated, gitignored and never published
# — not in the go.mod that ships. An unversioned replace does not work here;
# Go rejects replacing a workspace module at all versions.
#
# Every requirement that names another module of this workspace gets one, at
# the exact version the go.mod names, so the file never goes stale: it is
# derived from the requirements it serves.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

modules=("$@")
[ ${#modules[@]} -gt 0 ] || modules=(. cmd/quark drivers/postgres drivers/mysql drivers/sqlite drivers/mssql drivers/oracle)

rm -f go.work go.work.sum
go work init "${modules[@]}"

# path<TAB>dir for every module of the workspace, read from its own go.mod.
members=$(for m in "${modules[@]}"; do
  printf '%s\t%s\n' "$( (cd "$m" && go mod edit -json) | python3 -c 'import json,sys;print(json.load(sys.stdin)["Module"]["Path"])')" "$m"
done)

for m in "${modules[@]}"; do
  while IFS=$'\t' read -r path version; do
    dir=$(printf '%s\n' "$members" | awk -F'\t' -v p="$path" '$1==p {print $2; exit}')
    [ -n "$dir" ] || continue
    [ "$dir" = "." ] || dir="./$dir"
    go work edit -replace "${path}@${version}=${dir}"
  done < <( (cd "$m" && go mod edit -json) | python3 -c '
import json, sys
for r in json.load(sys.stdin).get("Require") or []:
    print(r["Path"], r["Version"], sep="\t")
')
done

echo "go.work: ${#modules[@]} modules, $(grep -c '^	' go.work 2>/dev/null || echo 0) lines of use/replace"
