#!/usr/bin/env bash
# Installs the standard-channel Gateway API CRDs, Envoy Gateway, and the
# Gateway gateway-system/public. The Gateway has an HTTP listener and an
# HTTPS listener whose certificate is the Secret gateway-system/public-tls,
# and both admit routes from every namespace.
#
# Usage: envoy-gateway.sh
#
# Inputs:
#   GATEWAY_API_VERSION    the Gateway API release of the CRDs (required)
#   ENVOY_GATEWAY_VERSION  the Envoy Gateway release (required)
set -euo pipefail

: "${GATEWAY_API_VERSION:?GATEWAY_API_VERSION is not set}"
: "${ENVOY_GATEWAY_VERSION:?ENVOY_GATEWAY_VERSION is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "==> Installing the Gateway API CRDs $GATEWAY_API_VERSION"
kubectl apply --server-side -f \
  "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API_VERSION/standard-install.yaml"

# The Gateway API CRDs of the cluster come from the step above, so Envoy
# Gateway installs only its own CRDs.
echo "==> Installing Envoy Gateway $ENVOY_GATEWAY_VERSION"
helm template eg-crds oci://docker.io/envoyproxy/gateway-crds-helm --version "$ENVOY_GATEWAY_VERSION" \
  --set crds.gatewayAPI.enabled=false --set crds.envoyGateway.enabled=true \
  | kubectl apply --server-side -f -
helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version "$ENVOY_GATEWAY_VERSION" \
  --namespace envoy-gateway-system --create-namespace --set crds.enabled=false --wait --timeout 5m

echo "==> Creating the Gateway gateway-system/public"
kubectl create namespace gateway-system --dry-run=client -o yaml | kubectl apply -f -
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 30 \
  -subj "/CN=*.example.com" -addext "subjectAltName=DNS:*.example.com" \
  -keyout "$work/tls.key" -out "$work/tls.crt" 2> /dev/null
kubectl create secret tls public-tls -n gateway-system --cert="$work/tls.crt" --key="$work/tls.key" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$here/envoy-gateway.yaml"
kubectl wait --for=condition=Accepted gatewayclass/envoy --timeout=120s
kubectl wait --for=condition=Accepted gateway/public -n gateway-system --timeout=120s
