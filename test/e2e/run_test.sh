#!/usr/bin/env bash
# Tests the Camunda minor of run.sh and of make journey-kind. make test-scripts
# runs it.
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

exit "$failed"
