#!/usr/bin/env bash
# shellcheck disable=SC2329 # the delete_* functions run through step
# Removes what CI runs left in the sandbox account: every role, managed
# policy, OIDC provider, and bucket with the tag cpo-ci-run, and schedules
# every such KMS key for deletion in 7 days.
#
#   sweep.sh --run <run-id>          what that run created, at any age
#   sweep.sh --older-than <hours>    what any run created that many hours ago
#
# It touches only cpo-ci-* names with the tag. Needs AWS_REGION and the
# credentials of the CI role, whose boundary allows nothing else.
set -euo pipefail

run=""
hours=""
case ${1:-} in
  --run) run=${2:?} ;;
  --older-than) hours=${2:?} ;;
  *)
    echo "usage: $0 --run <run-id> | --older-than <hours>" >&2
    exit 2
    ;;
esac
: "${AWS_REGION:?}"

cutoff=0
if [ -n "${hours}" ]; then
  cutoff=$(($(date +%s) - hours * 3600))
fi
failed=0

# matches <tag value> <creation date> tells if the sweep removes a resource.
matches() {
  local tag=$1 created=$2
  [ -n "${tag}" ] && [ "${tag}" != None ] || return 1
  if [ -n "${run}" ]; then
    [ "${tag}" = "${run}" ]
  else
    [ "$(date -d "${created}" +%s)" -lt "${cutoff}" ]
  fi
}

# step runs a delete_* function and keeps the sweep going when it fails.
step() {
  if ! "$@"; then
    echo "failed: $*" >&2
    failed=1
  fi
}

delete_role() {
  local role=$1 p
  for p in $(aws iam list-role-policies --role-name "${role}" --query 'PolicyNames[]' --output text); do
    aws iam delete-role-policy --role-name "${role}" --policy-name "${p}"
  done
  for p in $(aws iam list-attached-role-policies --role-name "${role}" --query 'AttachedPolicies[].PolicyArn' --output text); do
    aws iam detach-role-policy --role-name "${role}" --policy-arn "${p}"
  done
  aws iam delete-role --role-name "${role}"
}

delete_policy() {
  local arn=$1 v
  for v in $(aws iam list-policy-versions --policy-arn "${arn}" --query 'Versions[?!IsDefaultVersion].VersionId' --output text); do
    aws iam delete-policy-version --policy-arn "${arn}" --version-id "${v}"
  done
  aws iam delete-policy --policy-arn "${arn}"
}

delete_bucket() {
  local bucket=$1 objects
  while true; do
    objects=$(aws s3api list-object-versions --bucket "${bucket}" --max-items 500 \
      --query '{Objects: [Versions[].{Key: Key, VersionId: VersionId}, DeleteMarkers[].{Key: Key, VersionId: VersionId}][] | [?Key != null]}' \
      --output json)
    [ "$(jq '.Objects | length' <<<"${objects}")" -gt 0 ] || break
    aws s3api delete-objects --bucket "${bucket}" --delete "${objects}" >/dev/null
  done
  aws s3api delete-bucket --bucket "${bucket}"
}

delete_key() {
  local key=$1 alias
  for alias in $(aws kms list-aliases --key-id "${key}" --query 'Aliases[].AliasName' --output text); do
    aws kms delete-alias --alias-name "${alias}"
  done
  aws kms schedule-key-deletion --key-id "${key}" --pending-window-in-days 7 >/dev/null
}

# Roles before policies, so that the policies are detached.
while read -r role created; do
  [ -n "${role}" ] || continue
  tag=$(aws iam list-role-tags --role-name "${role}" --query "Tags[?Key=='cpo-ci-run'].Value | [0]" --output text)
  if matches "${tag}" "${created}"; then
    echo "role ${role} (${tag})"
    step delete_role "${role}"
  fi
done < <(aws iam list-roles --query "Roles[?starts_with(RoleName, 'cpo-ci-')].[RoleName, CreateDate]" --output text)

while read -r arn created; do
  [ -n "${arn}" ] || continue
  tag=$(aws iam list-policy-tags --policy-arn "${arn}" --query "Tags[?Key=='cpo-ci-run'].Value | [0]" --output text)
  if matches "${tag}" "${created}"; then
    echo "policy ${arn} (${tag})"
    step delete_policy "${arn}"
  fi
done < <(aws iam list-policies --scope Local --query "Policies[?starts_with(PolicyName, 'cpo-ci-')].[Arn, CreateDate]" --output text)

for arn in $(aws iam list-open-id-connect-providers --query 'OpenIDConnectProviderList[].Arn' --output text); do
  # The boundary allows reads only of the providers of the issuer bucket.
  provider=$(aws iam get-open-id-connect-provider --open-id-connect-provider-arn "${arn}" \
    --query "[CreateDate, Tags[?Key=='cpo-ci-run'].Value | [0]]" --output text 2>/dev/null) || continue
  read -r created tag <<<"${provider}"
  if matches "${tag}" "${created}"; then
    echo "OIDC provider ${arn} (${tag})"
    step aws iam delete-open-id-connect-provider --open-id-connect-provider-arn "${arn}"
  fi
done

while read -r bucket created; do
  [ -n "${bucket}" ] || continue
  tag=$(aws s3api get-bucket-tagging --bucket "${bucket}" \
    --query "TagSet[?Key=='cpo-ci-run'].Value | [0]" --output text 2>/dev/null) || continue
  if matches "${tag}" "${created}"; then
    echo "bucket ${bucket} (${tag})"
    step delete_bucket "${bucket}"
  fi
done < <(aws s3api list-buckets --query "Buckets[?starts_with(Name, 'cpo-ci-')].[Name, CreationDate]" --output text)

for key in $(aws kms list-keys --query 'Keys[].KeyId' --output text); do
  # The boundary allows reads only of tagged keys; others answer AccessDenied.
  tag=$(aws kms list-resource-tags --key-id "${key}" \
    --query "Tags[?TagKey=='cpo-ci-run'].TagValue | [0]" --output text 2>/dev/null) || continue
  read -r state created < <(aws kms describe-key --key-id "${key}" \
    --query 'KeyMetadata.[KeyState, CreationDate]' --output text)
  [ "${state}" != PendingDeletion ] || continue
  if matches "${tag}" "${created}"; then
    echo "KMS key ${key} (${tag})"
    step delete_key "${key}"
  fi
done

exit ${failed}
