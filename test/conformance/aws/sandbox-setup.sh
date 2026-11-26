#!/usr/bin/env bash
# The setup of each set of the AWS sandbox. It installs the pod identity
# webhook, and gives the release aws its controller role <run>-aws: one
# managed policy for each controller-policy-<controller>.json, and a trust
# for the ServiceAccount aws only. It also creates what the suites use:
#   - the managed policy <run>-conn-case, which the case of
#     config-connectors-role.yaml attaches. It allows nothing that exists.
#   - the KMS key of the aws-s3 case with-key and of s3-check.sh, with the
#     tag cpo-ci-run, so that the sweep of the run schedules its deletion.
#     Its ARN goes to the run file as AWS_KMS_KEY_ARN.
# A set runs it from its setup.sh. The policies are rendered into the
# directory of the first argument, so that the set can give one of them to
# a second release.
#
# Usage: sandbox-setup.sh <work directory>
set -euo pipefail

work=${1:?usage: sandbox-setup.sh <work directory>}
: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
: "${RUN_VARS:?RUN_VARS is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)

# Each bucket name starts with the run ID as bucketPrefix, which allows at
# most 40 characters.
if [ "${#RUN_ID}" -gt 40 ]; then
  echo "RUN_ID $RUN_ID has ${#RUN_ID} characters, and bucketPrefix allows at most 40" >&2
  exit 1
fi

"$repo/test/kind/pod-identity-webhook.sh"
policies=()
for c in identity connectors-role kms s3; do
  "$repo/test/conformance/render.sh" "$here/controller-policy-$c.json" > "$work/controller-policy-$c.json"
  policies+=("$work/controller-policy-$c.json")
done
"$repo/hack/aws-sandbox/plugin-role.sh" "$RUN_ID" aws "$CHART_NAMESPACE" aws "${policies[@]}"

aws iam create-policy --policy-name "$RUN_ID-conn-case" --tags "Key=cpo-ci-run,Value=$RUN_ID" \
  --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"s3:ListBucket\",\"Resource\":\"arn:aws:s3:::$RUN_ID-none\"}]}" \
  > /dev/null

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
