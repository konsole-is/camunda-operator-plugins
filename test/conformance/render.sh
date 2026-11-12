#!/usr/bin/env bash
# Prints a file with the variables of the run filled in: ${RUN_ID},
# ${AWS_ACCOUNT_ID}, ${AWS_REGION}, ${AWS_KMS_REPLICA_REGION}, and
# ${AWS_OIDC_PROVIDER_ARN}. All other text stays as it is, such as {name},
# $5, or ${HOME}. It exits 1 when the file names one of these variables and
# its value is empty, so that no name of the run starts with a dash.
#
# Usage: render.sh <file>
set -euo pipefail

file=${1:?usage: render.sh <file>}
text=$(< "$file")
for name in RUN_ID AWS_ACCOUNT_ID AWS_REGION AWS_KMS_REPLICA_REGION AWS_OIDC_PROVIDER_ARN; do
  placeholder="\${$name}"
  [[ $text == *"$placeholder"* ]] || continue
  value=${!name:-}
  if [ -z "$value" ]; then
    echo "the file names $placeholder, which is empty" >&2
    exit 1
  fi
  text=${text//"$placeholder"/"$value"}
done
printf '%s\n' "$text"
