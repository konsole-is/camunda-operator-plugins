#!/usr/bin/env bash
# Builds the image of a plugin, pushes it to the sandbox ECR repository
# cpo-sandbox-eks/<plugin> with the tag of the run, and installs the chart of
# the plugin on the EKS cluster of the run with a values file, as
# test/kind/plugin.sh does on kind. Only the plugin aws has a repository.
# CI only.
#
# Usage: plugin.sh <plugin> <values file> [<release>]
#
# Inputs:
#   CHART_NAMESPACE   the namespace of the release (required)
#   AWS_EKS_REGISTRY  the ECR registry of the sandbox (required)
#   RUN_ID            the run, also the image tag (required)
# test/eks/up.sh logs Docker in to the registry first.
set -euo pipefail

plugin=${1:?usage: plugin.sh <plugin> <values file> [<release>]}
values=${2:?usage: plugin.sh <plugin> <values file> [<release>]}
release=${3:-$plugin}
: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${AWS_EKS_REGISTRY:?AWS_EKS_REGISTRY is not set}"
: "${RUN_ID:?RUN_ID is not set}"
# terraform/aws-sandbox/eks-registry.tf creates one repository for aws.
[ "$plugin" = aws ] || { echo "the sandbox has no ECR repository for the plugin $plugin" >&2; exit 1; }
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
image=$AWS_EKS_REGISTRY/cpo-sandbox-eks/$plugin:$RUN_ID

echo "==> Building the $plugin image and pushing it to ECR"
make -C "$repo" docker-build PLUGIN="$plugin" IMG="$image"
docker push "$image"

echo "==> Installing the $plugin chart as the release $release"
helm upgrade --install "$release" "$repo/charts/$plugin" \
  --namespace "$CHART_NAMESPACE" --create-namespace \
  --values "$values" \
  --set-string image.repository="${image%:*}" \
  --set-string image.tag="${image##*:}" \
  --wait --timeout 3m
