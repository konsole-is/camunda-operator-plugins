#!/usr/bin/env bash
# Builds the image of a plugin, loads it into the kind cluster, and installs
# the chart of the plugin with a values file. The release has the name of the
# plugin.
#
# Usage: plugin.sh <plugin> <values file>
#
# Inputs:
#   KIND_CLUSTER     the name of the kind cluster (required)
#   CHART_NAMESPACE  the namespace of the release (required)
set -euo pipefail

plugin=${1:?usage: plugin.sh <plugin> <values file>}
values=${2:?usage: plugin.sh <plugin> <values file>}
: "${KIND_CLUSTER:?KIND_CLUSTER is not set}"
: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
image=camunda-operator-plugins-$plugin:kind

echo "==> Building the $plugin image and loading it into kind"
make -C "$repo" docker-build PLUGIN="$plugin" IMG="$image"
kind load docker-image --name "$KIND_CLUSTER" "$image"

echo "==> Installing the $plugin chart"
helm upgrade --install "$plugin" "$repo/charts/$plugin" \
  --namespace "$CHART_NAMESPACE" --create-namespace \
  --values "$values" \
  --set-string image.repository="${image%:*}" \
  --set-string image.tag="${image##*:}" \
  --wait --timeout 3m
