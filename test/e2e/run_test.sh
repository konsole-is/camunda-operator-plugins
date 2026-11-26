#!/usr/bin/env bash
# Tests the Camunda minor of run.sh and of make journey-kind, the variables
# that make journey-kind and make journey-eks pass, and that the eks journey
# needs its EKS cluster. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
unset CAMUNDA_MINOR

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

# An operator checkout with the matrix file of 8.9 only. kind and kubectl
# write to started instead of to a cluster.
operator=$work/operator
mkdir -p "$operator/test/e2e/matrix" "$work/bin"
printf 'CAMUNDA_VERSION=8.9.9\nELASTICSEARCH_VERSION=8.19.0\n' > "$operator/test/e2e/matrix/8.9.env"
printf 'ECK_VERSION ?= 3.2.0\n' > "$operator/Makefile"
for tool in kind kubectl; do
  printf '#!/usr/bin/env bash\necho "%s $*" >> "%s"\nexit 1\n' "$tool" "$work/started" > "$work/bin/$tool"
  chmod +x "$work/bin/$tool"
done

# run_journey <minor> runs run.sh for the bare-metal journey, with stderr in
# $work/stderr.
run_journey() {
  rm -f "$work/started"
  PATH="$work/bin:$PATH" KIND_CLUSTER=journey OPERATOR_DIR="$operator" REPORT="$work/report.xml" \
    CAMUNDA_MINOR="$1" "$here/run.sh" bare-metal > /dev/null 2> "$work/stderr"
}

run_journey 8.99
code=$?
want="test/e2e/matrix/8.99.env is not in the operator checkout at $operator, so the operator commit of go.mod does not support Camunda 8.99"
[ "$code" -eq 1 ] || fail "a minor without a matrix file" "exit $code, want 1"
[ "$(cat "$work/stderr")" = "$want" ] || fail "a minor without a matrix file" "stderr '$(cat "$work/stderr")', want '$want'"
[ ! -e "$work/started" ] || fail "a minor without a matrix file" "started the cluster: $(cat "$work/started")"

run_journey 8.9
grep -q '^kind create cluster' "$work/started" 2> /dev/null ||
  fail "a minor with a matrix file" "did not create the cluster: $(cat "$work/stderr")"

name="the eks journey without EKS_CLUSTER"
rm -f "$work/started"
PATH="$work/bin:$PATH" OPERATOR_DIR="$operator" REPORT="$work/report.xml" CAMUNDA_MINOR=8.9 EKS_CLUSTER='' \
  "$here/run.sh" eks > /dev/null 2> "$work/stderr"
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
grep -q EKS_CLUSTER "$work/stderr" || fail "$name" "stderr '$(cat "$work/stderr")' does not name EKS_CLUSTER"
[ ! -e "$work/started" ] || fail "$name" "started a cluster: $(cat "$work/started")"

# make -n prints the variables that journey-eks passes to the eks journey.
recipe=$(RUN_ID=cpo-ci-test AWS_EKS_REGISTRY=r.example AWS_EKS_PLUGINS_ROLE_ARN=arn:p make -s -n -C "$repo" journey-eks)
for want in 'EKS_CLUSTER="cpo-ci-test"' 'RUN_ID="cpo-ci-test"' 'AWS_EKS_REGISTRY="r.example"' \
  'AWS_EKS_PLUGINS_ROLE_ARN="arn:p"' 'AWS_CLI_IMAGE="public.ecr.aws/aws-cli/aws-cli:' 'test/e2e/run.sh eks'; do
  grep -qF -- "$want" <<< "$recipe" || fail "make journey-eks" "does not pass $want"
done

# make -n prints the recipe of journey-kind with the minor that it passes on.
minor_of_make() {
  make -s -n -C "$repo" journey-kind | grep -o 'CAMUNDA_MINOR="[^"]*"'
}
first=$(make -s -C "$repo" list-camunda-minors | jq -r '.[0]')
got=$(minor_of_make)
[ "$got" = "CAMUNDA_MINOR=\"$first\"" ] ||
  fail "make without CAMUNDA_MINOR" "passes $got, want the first minor of list-camunda-minors, $first"
got=$(CAMUNDA_MINOR=8.99 minor_of_make)
[ "$got" = 'CAMUNDA_MINOR="8.99"' ] || fail "make with CAMUNDA_MINOR in the environment" "passes $got, want 8.99"

# make -n prints the variables that journey-kind passes to the AWS journey.
recipe=$(RUN_ID=cpo-ci-test make -s -n -C "$repo" journey-kind JOURNEY=aws)
for want in 'CERT_MANAGER_VERSION="v[0-9]' 'POD_IDENTITY_WEBHOOK_VERSION="v[0-9]' 'RUN_ID="cpo-ci-test"'; do
  grep -q -- "$want" <<< "$recipe" || fail "make journey-kind for the AWS journey" "does not pass $want"
done

exit "$failed"
