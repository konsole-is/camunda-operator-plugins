#!/usr/bin/env bash
# Runs the conformance suite of the operator against one plugin on a new kind
# cluster. It creates the cluster with the operator, runs setup.sh of the
# plugin, installs the chart of the plugin with its values.yaml, and runs
# plugin-conformance with config.yaml and then with each config-<name>.yaml.
# Last it runs check.sh of the plugin, when the plugin has one. CI only: the
# cluster stays after the run, for the logs.
#
# render.sh fills the variables of the run into values.yaml and each config
# first. It refuses a file that names a variable without a value.
#
# Usage: run.sh <plugin> (make conformance-kind runs it)
#
# Inputs:
#   KIND_CLUSTER     the name of the kind cluster (required)
#   OPERATOR_DIR     the checkout of the operator (required, see up.sh)
#   CHART_NAMESPACE  the namespace of the plugin release (required)
#   REPORT           the JUnit report of config.yaml (required). The report
#                    of config-<name>.yaml ends in -<name>.xml instead.
# setup.sh and check.sh of the plugin read the other variables that they
# need. It exits 1 when a suite or check.sh failed.
set -euo pipefail

plugin=${1:?usage: run.sh <plugin>}
: "${REPORT:?REPORT is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
dir=$repo/test/conformance/$plugin
for f in setup.sh values.yaml config.yaml; do
  [ -f "$dir/$f" ] || { echo "test/conformance/$plugin/$f is missing" >&2; exit 1; }
done
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

"$repo/test/kind/up.sh"
"$dir/setup.sh"
"$repo/test/conformance/render.sh" "$dir/values.yaml" > "$work/values.yaml"
"$repo/test/kind/plugin.sh" "$plugin" "$work/values.yaml"

mkdir -p "$(dirname "$REPORT")"
cd "$repo"
failed=0
for config in "$dir"/config.yaml "$dir"/config-*.yaml; do
  [ -f "$config" ] || continue
  name=$(basename "$config" .yaml)
  "$repo/test/conformance/render.sh" "$config" > "$work/$name.yaml"

  # The suite reads the registration once, and the plugin writes it after its
  # pod is Ready.
  registration=$(awk '$1 == "registration:" { print $2 }' "$work/$name.yaml")
  echo "==> Waiting for the PluginRegistration $registration"
  kubectl wait "pluginregistration/$registration" --for=create --timeout=120s
  kubectl wait "pluginregistration/$registration" --for=condition=Accepted --timeout=120s
  kubectl wait "pluginregistration/$registration" --for=condition=Ready --timeout=120s

  echo "==> Running the conformance suite against $registration of $plugin"
  go run github.com/konsole-is/camunda-operator/conformance/cmd/plugin-conformance \
    --config "$work/$name.yaml" --report "${REPORT%.xml}${name#config}.xml" || failed=1
done

if [ -x "$dir/check.sh" ]; then
  echo "==> Running check.sh of $plugin"
  "$dir/check.sh" || failed=1
fi
exit "$failed"
