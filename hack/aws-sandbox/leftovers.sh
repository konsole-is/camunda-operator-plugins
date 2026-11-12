#!/usr/bin/env bash
# shellcheck disable=SC2016 # $run is a variable of jq, not of the shell
# Prints each role, managed policy, OIDC provider, and bucket that one CI
# run left in the sandbox account, one line "left: <kind> <name>" each. It
# reads names, not tags, so it finds also an object that lost its tag.
#
# Usage: leftovers.sh <run-id>
#
# Needs the credentials of the CI role. It exits 1 when it printed a line or
# a listing failed.
set -euo pipefail

run=${1:?usage: leftovers.sh <run-id>}
if ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "the run ID must match cpo-ci-[a-z0-9-]{1,40}: ${run}" >&2
  exit 2
fi

failed=0

# left <kind> <jq filter> <aws arguments...> prints the names of the listing
# that belong to the run.
left() {
  local kind=$1 filter=$2 names
  shift 2
  if ! names=$(aws "$@" --output json | jq -r --arg run "${run}" "${filter}"); then
    echo "failed to list: ${kind}" >&2
    failed=1
    return
  fi
  while read -r name; do
    [ -n "${name}" ] || continue
    echo "left: ${kind} ${name}"
    failed=1
  done <<< "${names}"
}

# The dash after the run ID keeps cpo-ci-1-1-10 out of the run cpo-ci-1-1-1.
left role '.Roles[].RoleName | select(startswith($run + "-"))' iam list-roles
left policy '.Policies[].PolicyName | select(startswith($run + "-"))' iam list-policies --scope Local
left "OIDC provider" '.OpenIDConnectProviderList[].Arn | select(endswith("/" + $run))' \
  iam list-open-id-connect-providers
left bucket '.Buckets[].Name | select(startswith($run + "-"))' s3api list-buckets

exit "${failed}"
