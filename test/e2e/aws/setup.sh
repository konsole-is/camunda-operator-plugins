#!/usr/bin/env bash
# Installs what the AWS journey needs beside the operator and ECK: the pod
# identity webhook, the controller role <run>-aws in the AWS sandbox with the
# controller policies of aws-identity, aws-kms, and aws-s3 from
# test/conformance/aws/, and one release aws of the chart aws with those
# three controllers on, from values.yaml. It also creates the reader role
# <run>-journey-reader of reader-policy.json, which the ServiceAccount
# aws-reader/aws-reader assumes, so that the specs read AWS from a pod. Then
# it waits until the registrations of the journey are Ready.
#
# Inputs:
#   CHART_NAMESPACE  the namespace of the plugin release (required)
#   RUN_ID           the run in the AWS sandbox (required)
# The scripts that it calls read the other variables that they need:
# AWS_REGION, AWS_ACCOUNT_ID, AWS_OIDC_PROVIDER_ARN, AWS_ISSUER_BUCKET,
# CERT_MANAGER_VERSION, POD_IDENTITY_WEBHOOK_VERSION, and the credentials of
# the CI role.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${RUN_ID:?RUN_ID is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
kind=$repo/test/kind
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

render=$repo/test/e2e/render.sh

"$kind/pod-identity-webhook.sh"

# The release and its ServiceAccount are aws, which the
# serviceAccount.annotations of values.yaml name.
policies=()
for controller in identity kms s3; do
  "$render" "$repo/test/conformance/aws/controller-policy-$controller.json" \
    > "$work/controller-policy-$controller.json"
  policies+=("$work/controller-policy-$controller.json")
done
"$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" aws "$CHART_NAMESPACE" aws "${policies[@]}"

echo "==> Creating the reader role of the journey"
"$render" "$here/reader-policy.json" > "$work/reader-policy.json"
out=$("$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" journey-reader aws-reader aws-reader \
  "$work/reader-policy.json")
echo "$out"
reader=$(sed -n 's/^role=//p' <<< "$out")
[ -n "$reader" ] || { echo "plugin-role.sh printed no role ARN for the reader" >&2; exit 1; }
kubectl create namespace aws-reader --dry-run=client -o yaml | kubectl apply -f -
kubectl create serviceaccount aws-reader -n aws-reader --dry-run=client -o yaml | kubectl apply -f -
kubectl annotate serviceaccount aws-reader -n aws-reader --overwrite "eks.amazonaws.com/role-arn=$reader"

"$render" "$here/values.yaml" > "$work/values.yaml"
"$kind/plugin.sh" aws "$work/values.yaml"

# Each controller writes its registration after the pod is Ready, and the
# operator accepts it after that.
for registration in aws-irsa aws-kms-key aws-s3; do
  echo "==> Waiting for the PluginRegistration $registration"
  kubectl wait "pluginregistration/$registration" --for=create --timeout=120s
  kubectl wait "pluginregistration/$registration" --for=condition=Accepted --timeout=120s
  kubectl wait "pluginregistration/$registration" --for=condition=Ready --timeout=120s
done
