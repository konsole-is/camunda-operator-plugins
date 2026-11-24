#!/usr/bin/env bash
# Prints a file with the variables of the run filled in, as
# test/conformance/render.sh does. A journey runs no setup.sh of
# test/conformance/, so no RUN_VARS file adds a variable. A file that names
# such a variable fails here, instead of reaching helm or IAM with the
# variable as text.
#
# Usage: render.sh <file>
set -euo pipefail

file=${1:?usage: render.sh <file>}
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

out=$(RUN_VARS='' "$repo/test/conformance/render.sh" "$file")
names=$(grep -oE '\$\{[A-Z][A-Z0-9_]*\}' <<< "$out" | sort -u | paste -sd ' ' - || true)
if [ -n "$names" ]; then
  echo "$file names variables that the journey does not set: $names" >&2
  exit 1
fi
printf '%s\n' "$out"
