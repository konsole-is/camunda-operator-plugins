#!/usr/bin/env bash
# Checks rule 2 of CONTRIBUTING.md section 3: .github/CODEOWNERS has an owner
# line for each of the five paths of every plugin. make test-scripts runs it.
set -uo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
owned=$(awk 'NF >= 2 && $1 !~ /^#/ { print $1 }' "$repo/.github/CODEOWNERS")

failed=0
for p in $(make -s -C "$repo" list-plugins | jq -r '.[]'); do
  for path in "/cmd/$p/" "/internal/${p//-/}/" "/charts/$p/" "/test/conformance/$p/" "/docs/plugins/$p.md"; do
    if ! grep -qxF -- "$path" <<< "$owned"; then
      echo "FAIL .github/CODEOWNERS has no owner for $path" >&2
      failed=1
    fi
  done
done
exit "$failed"
