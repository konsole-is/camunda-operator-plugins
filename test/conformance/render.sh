#!/usr/bin/env bash
# Prints a file with the variables of the run filled in: ${RUN_ID},
# ${AWS_ACCOUNT_ID}, ${AWS_REGION}, ${AWS_KMS_REPLICA_REGION}, and
# ${AWS_OIDC_PROVIDER_ARN}, from the environment. When RUN_VARS names a file,
# each line NAME=value of that file adds the variable ${NAME}. All other text
# stays as it is, such as {name}, $5, or ${HOME}. It exits 1 when the file
# names one of these variables and its value is empty, so that no name of the
# run starts with a dash. It also exits 1 on a line of RUN_VARS that is not
# NAME=value, that sets a variable of the environment, or that sets a name
# again.
#
# Usage: render.sh <file>
set -euo pipefail

file=${1:?usage: render.sh <file>}
env_names=(RUN_ID AWS_ACCOUNT_ID AWS_REGION AWS_KMS_REPLICA_REGION AWS_OIDC_PROVIDER_ARN)

declare -A values=()
for name in "${env_names[@]}"; do
  values[$name]=${!name:-}
done
file_names=()
if [ -n "${RUN_VARS:-}" ]; then
  [ -f "$RUN_VARS" ] || { echo "RUN_VARS names $RUN_VARS, which is not a file" >&2; exit 1; }
  n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    [ -n "$line" ] || continue
    name=${line%%=*}
    if [[ $line != *=* || ! $name =~ ^[A-Z][A-Z0-9_]*$ ]]; then
      echo "line $n of RUN_VARS is not NAME=value" >&2
      exit 1
    fi
    if [[ " ${env_names[*]} " == *" $name "* ]]; then
      echo "line $n of RUN_VARS sets $name, which comes from the environment of the run" >&2
      exit 1
    fi
    if [[ " ${file_names[*]} " == *" $name "* ]]; then
      echo "line $n of RUN_VARS sets $name again" >&2
      exit 1
    fi
    file_names+=("$name")
    values[$name]=${line#*=}
  done < "$RUN_VARS"
fi

text=$(< "$file")
for name in "${env_names[@]}" "${file_names[@]}"; do
  placeholder="\${$name}"
  [[ $text == *"$placeholder"* ]] || continue
  value=${values[$name]}
  if [ -z "$value" ]; then
    echo "the file names $placeholder, which is empty" >&2
    exit 1
  fi
  text=${text//"$placeholder"/"$value"}
done
printf '%s\n' "$text"
