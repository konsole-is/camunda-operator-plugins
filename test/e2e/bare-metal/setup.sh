#!/usr/bin/env bash
# Installs what the bare-metal journey needs beside the operator and ECK:
# RustFS, Envoy Gateway with the Gateway gateway-system/public, and the
# charts of rustfs and gateway-access. Then it waits until the registrations
# of the journey are Ready.
#
# Inputs:
#   CHART_NAMESPACE  the namespace of the plugin releases (required)
# The scripts of test/kind read the other variables that they need.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
kind=$here/../../kind

"$kind/rustfs.sh" "$CHART_NAMESPACE"
"$kind/envoy-gateway.sh"
"$kind/plugin.sh" rustfs "$here/rustfs-values.yaml"
"$kind/plugin.sh" gateway-access "$here/gateway-access-values.yaml"

# Each plugin writes its registration after its pod is Ready, and the
# operator accepts it after that.
for registration in rustfs-bucket gateway-public; do
  echo "==> Waiting for the PluginRegistration $registration"
  kubectl wait "pluginregistration/$registration" --for=create --timeout=120s
  kubectl wait "pluginregistration/$registration" --for=condition=Accepted --timeout=120s
  kubectl wait "pluginregistration/$registration" --for=condition=Ready --timeout=120s
done
