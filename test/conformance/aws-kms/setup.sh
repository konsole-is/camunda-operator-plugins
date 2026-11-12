#!/usr/bin/env bash
# Gives the controller of aws-kms its role in the AWS sandbox:
# installs the pod identity webhook, and creates the role <run>-aws-kms
# with the policy of controller-policy.json, which only the ServiceAccount
# of the release can assume.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${RUN_ID:?RUN_ID is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

"$repo/test/kind/pod-identity-webhook.sh"
"$repo/test/conformance/render.sh" "$here/controller-policy.json" > "$work/controller-policy.json"
"$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" aws-kms "$CHART_NAMESPACE" aws-kms \
  "$work/controller-policy.json"
