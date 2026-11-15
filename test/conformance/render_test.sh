#!/usr/bin/env bash
# shellcheck disable=SC2016 # the text under test holds ${...} literally
# Tests render.sh. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unset RUN_ID AWS_ACCOUNT_ID AWS_REGION AWS_KMS_REPLICA_REGION AWS_OIDC_PROVIDER_ARN RUN_VARS

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

# run_vars <text> writes the run file of the next case.
run_vars() {
  printf '%s\n' "$1" > "$work/vars"
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

run_vars $'AWS_KMS_KEY_ARN=arn:aws:kms:eu-west-1:111122223333:key/abc'
expect "fills a variable of the run file" \
  $'key: ${AWS_KMS_KEY_ARN}\nrole: ${RUN_ID}-hub' \
  $'key: arn:aws:kms:eu-west-1:111122223333:key/abc\nrole: cpo-ci-1-1-1-hub' \
  0 "" RUN_ID=cpo-ci-1-1-1 RUN_VARS="$work/vars"

run_vars $'TOKEN=a=b&c'
expect "splits a line at the first = and keeps the value as it is" \
  $'token: ${TOKEN}' $'token: a=b&c' 0 "" RUN_VARS="$work/vars"

run_vars $'FIRST=${SECOND}\nSECOND=literal'
expect "does not fill a placeholder that a value holds" \
  $'${FIRST} ${SECOND}' $'${SECOND} literal' 0 "" RUN_VARS="$work/vars"

expect "does not fill a placeholder that a value of the environment holds" \
  $'${AWS_ACCOUNT_ID} ${AWS_REGION}' $'${AWS_REGION} eu-west-1' 0 "" \
  'AWS_ACCOUNT_ID=${AWS_REGION}' AWS_REGION=eu-west-1

run_vars $'\nFIRST=1\n\nSECOND=2'
expect "skips empty lines" $'${FIRST} ${SECOND}' $'1 2' 0 "" RUN_VARS="$work/vars"

run_vars $'EMPTY='
expect "refuses an empty variable of the run file that the file names" \
  $'x: ${EMPTY}' "" 1 'the file names ${EMPTY}, which is empty' RUN_VARS="$work/vars"
expect "an empty variable of the run file that the file does not name is no error" \
  "{}" "{}" 0 "" RUN_VARS="$work/vars"

run_vars $'lower=x'
expect "refuses a name that is not upper case" "{}" "" 1 'line 1 of RUN_VARS is not NAME=value' RUN_VARS="$work/vars"
run_vars $'NO_VALUE'
expect "refuses a line without =" "{}" "" 1 'line 1 of RUN_VARS is not NAME=value' RUN_VARS="$work/vars"

run_vars $'RUN_ID=cpo-ci-9'
expect "refuses a run variable in the run file" "{}" "" 1 \
  'line 1 of RUN_VARS sets RUN_ID, which comes from the environment of the run' RUN_VARS="$work/vars"

run_vars $'KEY=a\nKEY=b'
expect "refuses a name set twice" "{}" "" 1 'line 2 of RUN_VARS sets KEY again' RUN_VARS="$work/vars"

expect "refuses a RUN_VARS that names no file" "{}" "" 1 \
  "RUN_VARS names $work/missing, which is not a file" RUN_VARS="$work/missing"

expect "keeps a variable of no run file as text" \
  $'key: ${AWS_KMS_KEY_ARN}' $'key: ${AWS_KMS_KEY_ARN}' 0 ""

exit "$failed"
