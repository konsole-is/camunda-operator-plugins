#!/usr/bin/env bash
# shellcheck disable=SC2016 # the text under test holds ${...} literally
# Tests render.sh. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unset RUN_ID AWS_ACCOUNT_ID AWS_REGION AWS_KMS_REPLICA_REGION AWS_OIDC_PROVIDER_ARN

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

# expect <case> <file text> <want stdout> <want exit> <want stderr> [<name=value>...]
expect() {
  local name=$1 text=$2 want=$3 want_code=$4 want_err=$5 out code
  shift 5
  printf '%s\n' "$text" > "$work/file"
  out=$(env "$@" "$here/render.sh" "$work/file" 2> "$work/stderr")
  code=$?
  [ "$code" -eq "$want_code" ] || fail "$name" "exit $code, want $want_code"
  [ "$out" = "$want" ] || fail "$name" "printed '$out', want '$want'"
  [ "$(cat "$work/stderr")" = "$want_err" ] || fail "$name" "stderr '$(cat "$work/stderr")', want '$want_err'"
}

expect "replaces the run variables" \
  $'role: ${RUN_ID}-hub\narn: arn:aws:iam::${AWS_ACCOUNT_ID}:x\nregions: [${AWS_REGION}, ${AWS_KMS_REPLICA_REGION}]\noidc: ${AWS_OIDC_PROVIDER_ARN}' \
  $'role: cpo-ci-1-1-1-hub\narn: arn:aws:iam::111122223333:x\nregions: [eu-west-1, eu-central-1]\noidc: arn:aws:iam::111122223333:oidc-provider/b/cpo-ci-1-1-1' \
  0 "" RUN_ID=cpo-ci-1-1-1 AWS_ACCOUNT_ID=111122223333 AWS_REGION=eu-west-1 AWS_KMS_REPLICA_REGION=eu-central-1 \
  AWS_OIDC_PROVIDER_ARN=arn:aws:iam::111122223333:oidc-provider/b/cpo-ci-1-1-1

expect "keeps other text" \
  $'hostnamePattern: "{name}.{namespace}.example.com"\nprice: $5\nother: ${HOME}\nglob: *[a]?\nrun: $RUN_ID' \
  $'hostnamePattern: "{name}.{namespace}.example.com"\nprice: $5\nother: ${HOME}\nglob: *[a]?\nrun: $RUN_ID' \
  0 "" RUN_ID=cpo-ci-1-1-1

expect "refuses an empty variable" $'role: ${RUN_ID}-hub' "" 1 'the file names ${RUN_ID}, which is empty'
expect "refuses a variable set to nothing" $'role: ${RUN_ID}-hub' "" 1 'the file names ${RUN_ID}, which is empty' RUN_ID=

expect "a file without variables needs none" "{}" "{}" 0 ""

exit "$failed"
