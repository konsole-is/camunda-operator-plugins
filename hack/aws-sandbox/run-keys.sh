#!/usr/bin/env bash
# Prints one line "<key ID> <run>" for each KMS key of a region with the tag
# cpo-ci-run, in any case of the key. It reads the tags of every key with one
# call of the Resource Groups Tagging API. When the caller may not use that
# API, it reads them with one call of kms list-resource-tags for each key,
# which takes minutes in an account with hundreds of keys. A key whose tags
# the caller may not read belongs to no run. It exits 1, and prints nothing,
# when neither listing works, so that a failed read is never an empty list.
#
# Usage: run-keys.sh <region>
#
# The tagging API answers from an index of the tags of the region, not from
# KMS. A key of a run carries its run tag from its create, and no role of a
# run can remove it.
set -euo pipefail

region=${1:?usage: run-keys.sh <region>}
run_tag='[.[] | select((.Key // .TagKey | ascii_downcase) == "cpo-ci-run") | (.Value // .TagValue)][0] // empty'

if out=$(aws resourcegroupstaggingapi get-resources --region "${region}" --resource-type-filters kms:key \
  --output json 2> /dev/null); then
  jq -r ".ResourceTagMappingList[] | (.Tags | ${run_tag}) as \$run | select(\$run != null) |
    (.ResourceARN | sub(\"^.*:key/\"; \"\")) + \" \" + \$run" <<< "${out}"
  exit 0
fi

echo "run-keys.sh: the tagging API refused the listing in ${region}; reading the tags of each key" >&2
keys=$(aws kms list-keys --region "${region}" --output json | jq -r '.Keys[].KeyId') || exit 1
for key in ${keys}; do
  tags=$(aws kms list-resource-tags --region "${region}" --key-id "${key}" --output json 2> /dev/null) || continue
  run=$(jq -r ".Tags | ${run_tag}" <<< "${tags}")
  [ -z "${run}" ] || echo "${key} ${run}"
done
