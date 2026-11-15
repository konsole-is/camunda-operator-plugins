#!/usr/bin/env bash
# Gives the controller of aws-s3 its role in the AWS sandbox: installs the
# pod identity webhook, and creates the role <run>-aws-s3 with the policy of
# controller-policy.json, which only the ServiceAccount of the release can
# assume. Creates the KMS key of the case with-key, with the tag cpo-ci-run,
# so that the sweep of the run schedules its deletion, and writes its ARN to
# the run file as AWS_KMS_KEY_ARN.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
: "${RUN_VARS:?RUN_VARS is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Each bucket name starts with the run ID as bucketPrefix, which allows at
# most 40 characters.
if [ "${#RUN_ID}" -gt 40 ]; then
  echo "RUN_ID $RUN_ID has ${#RUN_ID} characters, and bucketPrefix allows at most 40" >&2
  exit 1
fi

"$repo/test/kind/pod-identity-webhook.sh"
"$repo/test/conformance/render.sh" "$here/controller-policy.json" > "$work/controller-policy.json"
"$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" aws-s3 "$CHART_NAMESPACE" aws-s3 \
  "$work/controller-policy.json"

echo "==> Creating the KMS key of the run"
key=$(aws kms create-key --region "$AWS_REGION" \
  --description "Conformance of aws-s3, run $RUN_ID" \
  --tags "TagKey=cpo-ci-run,TagValue=$RUN_ID" \
  --query KeyMetadata.Arn --output text)
[[ $key == arn:aws:kms:"$AWS_REGION":*:key/* ]] || {
  echo "aws kms create-key gave '$key', which is not a key ARN" >&2
  exit 1
}
echo "AWS_KMS_KEY_ARN=$key" >> "$RUN_VARS"
