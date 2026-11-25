#!/usr/bin/env bash
# Gives the release aws its role in the AWS sandbox with sandbox-setup.sh,
# and the release aws-keys the role <run>-aws-keys with the policy of
# aws-kms only, which only the ServiceAccount aws-keys can assume.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${RUN_ID:?RUN_ID is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

"$here/../sandbox-setup.sh" "$work"
"$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" aws-keys "$CHART_NAMESPACE" aws-keys \
  "$work/controller-policy-kms.json"
