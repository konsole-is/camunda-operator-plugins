#!/usr/bin/env bash
# Prints true when a change can affect the conformance run of a plugin, and
# false when it cannot. Only the docs, Markdown files, and the files of the
# other plugins cannot affect it.
#
# Usage: scope.sh <plugin> <base commit> <head commit>
set -euo pipefail

plugin=${1:?usage: scope.sh <plugin> <base> <head>}
base=${2:?usage: scope.sh <plugin> <base> <head>}
head=${3:?usage: scope.sh <plugin> <base> <head>}
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

plugins=$(make -s -C "$repo" list-plugins | jq -r '.[]')
others=()
for p in $plugins; do
  [ "$p" = "$plugin" ] && continue
  others+=("cmd/$p/" "internal/${p//-/}/" "charts/$p/" "test/conformance/$p/")
done

# Both paths of a rename count: the old path can be of this plugin.
files=$(git -C "$repo" diff --no-renames --name-only "$base...$head")
while IFS= read -r file; do
  [ -n "$file" ] || continue
  case "$file" in
    docs/* | *.md) continue ;;
  esac
  for prefix in "${others[@]}"; do
    [[ "$file" == "$prefix"* ]] && continue 2
  done
  echo true
  exit 0
done <<< "$files"
echo false
