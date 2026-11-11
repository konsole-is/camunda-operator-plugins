#!/usr/bin/env bash
# Creates the IAM role of a plugin controller for one CI run: the role
# <run-id>-<name> with the boundary, trusted only by one ServiceAccount of
# the kind cluster of the run, and the managed policy <run-id>-<name> from a
# policy file, attached to it. The boundary allows only cpo-ci-* policies on
# a role, so the policy of the plugin is a policy of the run. Both carry the
# tag cpo-ci-run. It prints role=<role ARN>.
#
# Usage: plugin-role.sh <run-id> <name> <namespace> <service-account> <policy-file>
#
# Run it after issuer.sh up. Needs AWS_REGION, AWS_ISSUER_BUCKET, and the
# credentials of the CI role. When a call fails, sweep.sh --run removes what
# the script created before it.
set -euo pipefail

if [ $# -ne 5 ]; then
  echo "usage: $0 <run-id> <name> <namespace> <service-account> <policy-file>" >&2
  exit 2
fi
run=$1
name=$2
namespace=$3
service_account=$4
policy=$5
if ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "the run ID must match cpo-ci-[a-z0-9-]{1,40}: ${run}" >&2
  exit 2
fi
if ! [[ ${name} =~ ^[a-z0-9-]+$ ]]; then
  echo "the name must match [a-z0-9-]+: ${name}" >&2
  exit 2
fi
role=${run}-${name}
if [ "${#role}" -gt 64 ]; then
  echo "the role name ${role} is longer than 64 characters" >&2
  exit 2
fi

: "${AWS_REGION:?}" "${AWS_ISSUER_BUCKET:?}"
host="${AWS_ISSUER_BUCKET}.s3.${AWS_REGION}.amazonaws.com"
account=$(aws sts get-caller-identity --query Account --output text)
provider="arn:aws:iam::${account}:oidc-provider/${host}/${run}"

trust=$(jq -cn --arg provider "${provider}" --arg issuer "${host}/${run}" \
  --arg subject "system:serviceaccount:${namespace}:${service_account}" '{
    Version: "2012-10-17",
    Statement: [{
      Effect: "Allow",
      Principal: {Federated: $provider},
      Action: "sts:AssumeRoleWithWebIdentity",
      Condition: {StringEquals: {
        ($issuer + ":aud"): "sts.amazonaws.com",
        ($issuer + ":sub"): $subject
      }}
    }]
  }')

policy_arn=$(aws iam create-policy --policy-name "${role}" --policy-document "file://${policy}" \
  --tags "Key=cpo-ci-run,Value=${run}" --query Policy.Arn --output text)
role_arn=$(aws iam create-role --role-name "${role}" --assume-role-policy-document "${trust}" \
  --permissions-boundary "arn:aws:iam::${account}:policy/cpo-sandbox-boundary" \
  --tags "Key=cpo-ci-run,Value=${run}" --query Role.Arn --output text)
aws iam attach-role-policy --role-name "${role}" --policy-arn "${policy_arn}"
echo "role=${role_arn}"
