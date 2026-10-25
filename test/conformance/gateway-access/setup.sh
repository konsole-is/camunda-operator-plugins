#!/usr/bin/env bash
# Installs Envoy Gateway with the Gateway gateway-system/public, and the
# targets of config.yaml: a CamundaCluster and a CamundaManagementCluster
# with the Services of their endpoints. No Camunda pod runs. The operator
# does not reconcile the paused cluster, and it cannot resolve the
# references of the plane, so the endpoints that this script writes stay.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
namespace=conformance-gateway

"$here/../../kind/envoy-gateway.sh"

echo "==> Creating the targets in $namespace"
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -n "$namespace" -f "$here/targets.yaml"
# The plane writes its first status before the endpoints go in. Each later
# status write of the operator reads the endpoints first.
kubectl wait -n "$namespace" camundamanagementcluster/conf-plane --for=condition=Ready=False --timeout=120s
kubectl patch -n "$namespace" camundacluster/conf-cluster --subresource=status --type=merge \
  --patch-file "$here/cluster-endpoints.yaml"
kubectl patch -n "$namespace" camundamanagementcluster/conf-plane --subresource=status --type=merge \
  --patch-file "$here/plane-endpoints.yaml"
