#!/usr/bin/env bash
# Renders the chart of one plugin with each case of
# hack/chartcheck/testdata/<plugin>/ and checks the chart rule of
# CONTRIBUTING.md. make chart-verify runs it for each chart.
#
# A case whose first line is "# refuses: <text>" must fail to render, with
# <text> in the error. Every other case must pass helm lint and chartcheck,
# and two releases of it must share no object name and no leader election
# Lease: two releases in two namespaces whose names join to the same text,
# two releases whose long names are cut to the same text, and two releases
# in one namespace.
#
# Usage: verify.sh <chart dir> <namespace>
#
# Inputs:
#   HELM  the helm binary (default helm)
set -euo pipefail

chart=${1:?usage: verify.sh <chart dir> <namespace>}
namespace=${2:?usage: verify.sh <chart dir> <namespace>}
helm=${HELM:-helm}
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
plugin=$(basename "$chart")
cases=$repo/hack/chartcheck/testdata/$plugin
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

(cd "$repo" && go build -o "$work/chartcheck" ./hack/chartcheck)

# names_differ <values file> <namespace> <release> <namespace> <release>
# fails when the two releases render a common object name or Lease, or when
# the first renders no name.
names_differ() {
  local values=$1 a b common
  a=$("$helm" template "$3" "$chart" --namespace "$2" -f "$values" | "$work/chartcheck" -names -namespace "$2")
  b=$("$helm" template "$5" "$chart" --namespace "$4" -f "$values" | "$work/chartcheck" -names -namespace "$4")
  common=$(comm -12 <(sort <<< "$a") <(sort <<< "$b"))
  if [ -z "$a" ] || [ -n "$common" ]; then
    echo "$chart with $(basename "$values"): the releases '$2 $3' and '$4 $5' share names: ${common:-none rendered}" >&2
    return 1
  fi
  echo "  $chart with $(basename "$values"): '$2 $3' and '$4 $5' render different names"
}

[ -d "$cases" ] || { echo "$chart has no cases in hack/chartcheck/testdata/$plugin" >&2; exit 1; }
long=$(printf 'n%.0s' $(seq 60))
for values in "$cases"/*.yaml; do
  refuses=$(sed -n '1s/^# refuses: //p' "$values")
  if [ -n "$refuses" ]; then
    if err=$("$helm" template verify "$chart" --namespace "$namespace" -f "$values" 2>&1 > /dev/null); then
      echo "$chart renders with $(basename "$values"), want the error: $refuses" >&2
      exit 1
    fi
    if [[ $err != *"$refuses"* ]]; then
      echo "$chart refuses $(basename "$values") with '$err', want: $refuses" >&2
      exit 1
    fi
    echo "  $chart refuses $(basename "$values"): $refuses"
    continue
  fi

  "$helm" lint "$chart" -f "$values"
  "$helm" template verify "$chart" --namespace "$namespace" -f "$values" |
    "$work/chartcheck" -values "$chart/values.yaml" -values "$values" -namespace "$namespace"
  names_differ "$values" team-a plugin team a-plugin
  names_differ "$values" "${long}a" plugin "${long}b" plugin
  names_differ "$values" plugins one plugins two
done
