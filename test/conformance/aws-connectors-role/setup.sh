#!/usr/bin/env bash
# Gives the controller of aws-connectors-role its role in the AWS sandbox:
# installs the pod identity webhook, and creates the role <run>-aws-connectors-role
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
"$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" aws-connectors-role "$CHART_NAMESPACE" aws-connectors-role \
  "$work/controller-policy.json"

# The managed policy that the case of config.yaml attaches. It allows
# nothing that exists.
aws iam create-policy --policy-name "$RUN_ID-conn-case" --tags "Key=cpo-ci-run,Value=$RUN_ID" \
  --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBucket\",\"Resource\":\"arn:aws:s3:::$RUN_ID-none\"}]}" \
  > /dev/null
