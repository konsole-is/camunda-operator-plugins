#!/usr/bin/env bash
# Runs the conformance suite of the operator against one plugin on a new kind
# cluster. It creates the cluster with the operator, runs setup.sh of the
# plugin, installs the chart of the plugin with its values.yaml as the
# release <plugin>, and then with each values-<release>.yaml as the release
# <release>. It runs plugin-conformance with config.yaml and then with each
# config-<name>.yaml. Last it runs check.sh, when there is one. CI only: the
# cluster stays after the run, for the logs.
#
# The files come from test/conformance/<plugin>/, or from
# test/conformance/<plugin>/<set>/ when a set is given. A plugin with sets
# runs each set as one job.
#
# render.sh fills the variables of the run into each values file and each
# config first. It refuses a file that names a variable without a value.
# setup.sh can add a variable with a line NAME=value in the file $RUN_VARS,
# and render.sh fills it in each file that it renders after that line.
# check.sh gets RUN_VARS too.
#
# Usage: run.sh <plugin> [<set>] (make conformance-kind runs it)
#
# Inputs:
#   KIND_CLUSTER     the name of the kind cluster (required)
#   OPERATOR_DIR     the checkout of the operator (required, see up.sh)
#   CHART_NAMESPACE  the namespace of the plugin releases (required)
#   REPORT           the JUnit report of config.yaml (required). The report
#                    of config-<name>.yaml ends in -<name>.xml instead.
# setup.sh and check.sh read the other variables that they need. It exits 1
# when a suite or check.sh failed.
set -euo pipefail

plugin=${1:?usage: run.sh <plugin> [<set>]}
set=${2:-}
: "${REPORT:?REPORT is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
dir=$repo/test/conformance/$plugin${set:+/$set}
for f in setup.sh values.yaml config.yaml; do
  [ -f "$dir/$f" ] || { echo "${dir#"$repo"/}/$f is missing" >&2; exit 1; }
done
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# setup.sh adds the variables of the objects that it creates for the run.
export RUN_VARS=$work/run-vars
: > "$RUN_VARS"

"$repo/test/kind/up.sh"
"$dir/setup.sh"
"$repo/test/conformance/render.sh" "$dir/values.yaml" > "$work/values.yaml"
"$repo/test/kind/plugin.sh" "$plugin" "$work/values.yaml"
for values in "$dir"/values-*.yaml; do
  [ -f "$values" ] || continue
  release=$(basename "$values" .yaml)
  release=${release#values-}
  "$repo/test/conformance/render.sh" "$values" > "$work/values-$release.yaml"
  "$repo/test/kind/plugin.sh" "$plugin" "$work/values-$release.yaml" "$release"
done

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
  echo "==> Running check.sh of ${dir#"$repo"/test/conformance/}"
  "$dir/check.sh" || failed=1
fi
exit "$failed"
