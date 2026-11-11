#!/usr/bin/env bash
# Installs the ECK CRDs and the ECK operator, which run the Elasticsearch of
# an ElasticsearchCluster, and waits until the operator is up.
#
# Usage: eck.sh
#
# Inputs:
#   ECK_VERSION  the ECK release (required)
set -euo pipefail

: "${ECK_VERSION:?ECK_VERSION is not set}"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "==> Installing ECK $ECK_VERSION"
for part in crds operator; do
  curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 --max-time 60 \
    -o "$work/$part.yaml" "https://download.elastic.co/downloads/eck/$ECK_VERSION/$part.yaml"
  # The CRDs are larger than the annotation of a client-side apply.
  kubectl apply --server-side -f "$work/$part.yaml"
done
kubectl rollout status statefulset/elastic-operator -n elastic-system --timeout=300s
