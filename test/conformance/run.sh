#!/usr/bin/env bash
# Runs the conformance suite of the operator against one plugin on a new kind
# cluster. It creates the cluster with the operator, runs setup.sh of the
# plugin, installs the chart of the plugin with its values.yaml, and runs
# plugin-conformance with its config.yaml. CI only: the cluster stays after
# the run, for the logs.
#
# Usage: run.sh <plugin> (make conformance-kind runs it)
#
# Inputs:
#   KIND_CLUSTER     the name of the kind cluster (required)
#   OPERATOR_DIR     the checkout of the operator (required, see up.sh)
#   CHART_NAMESPACE  the namespace of the plugin release (required)
#   REPORT           the JUnit report to write (required)
# setup.sh of the plugin reads the other variables that it needs.
set -euo pipefail

plugin=${1:?usage: run.sh <plugin>}
: "${REPORT:?REPORT is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
dir=$repo/test/conformance/$plugin
for f in setup.sh values.yaml config.yaml; do
  [ -f "$dir/$f" ] || { echo "test/conformance/$plugin/$f is missing" >&2; exit 1; }
done

"$repo/test/kind/up.sh"
"$dir/setup.sh"
"$repo/test/kind/plugin.sh" "$plugin" "$dir/values.yaml"

echo "==> Running the conformance suite against $plugin"
mkdir -p "$(dirname "$REPORT")"
cd "$repo"
go run github.com/konsole-is/camunda-operator/conformance/cmd/plugin-conformance \
  --config "$dir/config.yaml" --report "$REPORT"
