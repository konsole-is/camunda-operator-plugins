#!/usr/bin/env bash
# Creates the IAM role of a plugin controller for one CI run: the role
# <run-id>-<name> with the boundary, trusted only by one ServiceAccount of
# the kind cluster of the run, and one managed policy for each policy file,
# attached to it. With one file the policy is <run-id>-<name>. With more,
# the policy of the file controller-policy-<x>.json is <run-id>-<name>-<x>,
# so that each controller of a release has its own policy. The boundary
# allows only cpo-ci-* policies on a role, so the policies of the plugin are
# policies of the run. Each carries the tag cpo-ci-run. It prints
# role=<role ARN>.
#
# Usage: plugin-role.sh <run-id> <name> <namespace> <service-account> <policy-file>...
#
# Run it after issuer.sh up. Needs AWS_REGION, AWS_ISSUER_BUCKET, and the
# credentials of the CI role. When a call fails, sweep.sh --run removes what
# the script created before it.
set -euo pipefail

if [ $# -lt 5 ]; then
  echo "usage: $0 <run-id> <name> <namespace> <service-account> <policy-file>..." >&2
  exit 2
fi
run=$1
name=$2
namespace=$3
service_account=$4
shift 4
policies=("$@")
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
policy_names=()
for policy in "${policies[@]}"; do
  if [ "${#policies[@]}" -eq 1 ]; then
    policy_names+=("${role}")
    continue
  fi
  suffix=$(basename "${policy}" .json)
  suffix=${suffix#controller-policy-}
  if ! [[ ${suffix} =~ ^[a-z0-9-]+$ ]]; then
    echo "the name of the policy file ${policy}, without controller-policy- and .json, must match [a-z0-9-]+" >&2
    exit 2
  fi
  policy_names+=("${role}-${suffix}")
done

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

policy_arns=()
for i in "${!policies[@]}"; do
  policy_arns+=("$(aws iam create-policy --policy-name "${policy_names[i]}" \
    --policy-document "file://${policies[i]}" \
    --tags "Key=cpo-ci-run,Value=${run}" --query Policy.Arn --output text)")
done
role_arn=$(aws iam create-role --role-name "${role}" --assume-role-policy-document "${trust}" \
  --permissions-boundary "arn:aws:iam::${account}:policy/cpo-sandbox-boundary" \
  --tags "Key=cpo-ci-run,Value=${run}" --query Role.Arn --output text)
for arn in "${policy_arns[@]}"; do
  aws iam attach-role-policy --role-name "${role}" --policy-arn "${arn}"
done
echo "role=${role_arn}"
