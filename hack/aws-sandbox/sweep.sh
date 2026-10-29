#!/usr/bin/env bash
# shellcheck disable=SC2329 # the delete_* functions run through step
# Removes what CI runs left in the sandbox account: every role, managed
# policy, OIDC provider, and bucket with the tag cpo-ci-run, and schedules
# every such KMS key for deletion in 7 days.
#
#   sweep.sh --run <run-id>          what that run created, at any age
#   sweep.sh --older-than <hours>    what any run created that many hours ago
#
# It removes a role, policy, OIDC provider, or KMS key only when it has the
# tag. With --older-than, it also removes a cpo-ci-* bucket without the tag
# that is that many hours old. Needs AWS_REGION and the credentials of the CI
# role. It exits 1 when a removal or the listing of a resource type failed.
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
    older "${created}"
  fi
}

older() {
  [ -z "${run}" ] && [ "$(date -d "$1" +%s)" -lt "${cutoff}" ]
}

# run_tag reads the tags of a resource as JSON and prints the value of
# cpo-ci-run, or nothing. The boundary checks that key in any case, so a
# run can write it in any case.
run_tag() {
  jq -r '[(.Tags // .TagSet // [])[] | select(((.Key // .TagKey) | ascii_downcase) == "cpo-ci-run") | (.Value // .TagValue)][0] // ""'
}

# step runs a delete_* function and keeps the sweep going when it fails.
step() {
  if ! "$@"; then
    echo "failed: $*" >&2
    failed=1
  fi
}

# The delete_* functions run inside the condition of step, where errexit
# is off, so each command ends the function on its own failure.

delete_role() {
  local role=$1 names p
  names=$(aws iam list-role-policies --role-name "${role}" --query 'PolicyNames[]' --output text) || return 1
  for p in ${names}; do
    aws iam delete-role-policy --role-name "${role}" --policy-name "${p}" || return 1
  done
  names=$(aws iam list-attached-role-policies --role-name "${role}" --query 'AttachedPolicies[].PolicyArn' --output text) || return 1
  for p in ${names}; do
    aws iam detach-role-policy --role-name "${role}" --policy-arn "${p}" || return 1
  done
  aws iam delete-role --role-name "${role}"
}

delete_policy() {
  local arn=$1 versions v
  versions=$(aws iam list-policy-versions --policy-arn "${arn}" --query 'Versions[?!IsDefaultVersion].VersionId' --output text) || return 1
  for v in ${versions}; do
    aws iam delete-policy-version --policy-arn "${arn}" --version-id "${v}" || return 1
  done
  aws iam delete-policy --policy-arn "${arn}"
}

delete_bucket() {
  local bucket=$1 objects result
  while true; do
    objects=$(aws s3api list-object-versions --bucket "${bucket}" --max-items 500 \
      --query '{Objects: [Versions[].{Key: Key, VersionId: VersionId}, DeleteMarkers[].{Key: Key, VersionId: VersionId}][] | [?Key != null]}' \
      --output json) || return 1
    [ "$(jq '.Objects | length' <<<"${objects}")" -gt 0 ] || break
    result=$(aws s3api delete-objects --bucket "${bucket}" --delete "${objects}" --output json) || return 1
    # A partial failure exits 0 and lists the objects in Errors.
    if [ "$(jq '.Errors // [] | length' <<<"${result}")" -gt 0 ]; then
      jq -r '.Errors[] | "\(.Key): \(.Code) \(.Message)"' <<<"${result}" >&2
      return 1
    fi
  done
  aws s3api delete-bucket --bucket "${bucket}"
}

delete_key() {
  local key=$1 aliases alias
  aliases=$(aws kms list-aliases --key-id "${key}" --query 'Aliases[].AliasName' --output text) || return 1
  for alias in ${aliases}; do
    aws kms delete-alias --alias-name "${alias}" || return 1
  done
  aws kms schedule-key-deletion --key-id "${key}" --pending-window-in-days 7 >/dev/null
}

# Roles before policies, so that the policies are detached.
if ! roles=$(aws iam list-roles --query "Roles[?starts_with(RoleName, 'cpo-ci-')].[RoleName, CreateDate]" --output text); then
  echo "failed to list: roles" >&2
  failed=1
fi
while read -r role created; do
  [ -n "${role}" ] || continue
  if ! tag=$(aws iam list-role-tags --role-name "${role}" --output json | run_tag); then
    failed=1
    continue
  fi
  if matches "${tag}" "${created}"; then
    echo "role ${role} (${tag})"
    step delete_role "${role}"
  fi
done <<<"${roles}"

if ! policies=$(aws iam list-policies --scope Local --query "Policies[?starts_with(PolicyName, 'cpo-ci-')].[Arn, CreateDate]" --output text); then
  echo "failed to list: policies" >&2
  failed=1
fi
while read -r arn created; do
  [ -n "${arn}" ] || continue
  if ! tag=$(aws iam list-policy-tags --policy-arn "${arn}" --output json | run_tag); then
    failed=1
    continue
  fi
  if matches "${tag}" "${created}"; then
    echo "policy ${arn} (${tag})"
    step delete_policy "${arn}"
  fi
done <<<"${policies}"

if ! providers=$(aws iam list-open-id-connect-providers --query 'OpenIDConnectProviderList[].Arn' --output text); then
  echo "failed to list: providers" >&2
  failed=1
fi
for arn in ${providers}; do
  # The boundary allows reads only of the providers of the issuer bucket.
  provider=$(aws iam get-open-id-connect-provider --open-id-connect-provider-arn "${arn}" \
    --output json 2>/dev/null) || continue
  created=$(jq -r .CreateDate <<<"${provider}")
  tag=$(run_tag <<<"${provider}")
  if matches "${tag}" "${created}"; then
    echo "OIDC provider ${arn} (${tag})"
    step aws iam delete-open-id-connect-provider --open-id-connect-provider-arn "${arn}"
  fi
done

if ! buckets=$(aws s3api list-buckets --query "Buckets[?starts_with(Name, 'cpo-ci-')].[Name, CreationDate]" --output text); then
  echo "failed to list: buckets" >&2
  failed=1
fi
while read -r bucket created; do
  [ -n "${bucket}" ] || continue
  # A bucket without tags answers NoSuchTagSet.
  tag=$(aws s3api get-bucket-tagging --bucket "${bucket}" --output json 2>/dev/null | run_tag) || tag=""
  untagged=false
  if [ -z "${tag}" ] || [ "${tag}" = None ]; then
    untagged=true
  fi
  # The boundary cannot keep a run from replacing the tags of a bucket.
  if matches "${tag}" "${created}" || { ${untagged} && older "${created}"; }; then
    echo "bucket ${bucket} (${tag:-no tag})"
    step delete_bucket "${bucket}"
  fi
done <<<"${buckets}"

if ! keys=$(aws kms list-keys --query 'Keys[].KeyId' --output text); then
  echo "failed to list: keys" >&2
  failed=1
fi
for key in ${keys}; do
  # The boundary allows reads only of tagged keys; others answer AccessDenied.
  tag=$(aws kms list-resource-tags --key-id "${key}" --output json 2>/dev/null | run_tag) || continue
  if ! metadata=$(aws kms describe-key --key-id "${key}" --query 'KeyMetadata.[KeyState, CreationDate]' --output text); then
    failed=1
    continue
  fi
  read -r state created <<<"${metadata}"
  [ "${state}" != PendingDeletion ] || continue
  if matches "${tag}" "${created}"; then
    echo "KMS key ${key} (${tag})"
    step delete_key "${key}"
  fi
done

exit ${failed}
