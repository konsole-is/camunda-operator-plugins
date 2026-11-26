#!/usr/bin/env bash
# Tests up.sh against a stub kubectl, aws, docker, and make, which write each
# call to $STUB/calls. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

# kubectl answers the context CONTEXT.
cat > "$stub/kubectl" << 'EOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$STUB/calls"
[ "$*" != "config current-context" ] || echo "$CONTEXT"
EOF
for tool in aws docker make; do
  # shellcheck disable=SC2016 # $* and $STUB are variables of the stub
  printf '#!/usr/bin/env bash\necho "%s $*" >> "$STUB/calls"\n' "$tool" > "$stub/$tool"
done
chmod +x "$stub"/*
export PATH="$stub:$PATH" STUB=$stub OPERATOR_DIR=$stub/operator AWS_EKS_REGISTRY=1.dkr.ecr.eu-west-1.amazonaws.com \
  AWS_REGION=eu-west-1

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

# refused <name> <run> <cluster> <context> checks that up.sh exits 1 before
# it builds or installs anything.
refused() {
  rm -f "$stub/calls"
  RUN_ID=$2 EKS_CLUSTER=$3 CONTEXT=$4 "$here/up.sh" > /dev/null 2>&1
  local code=$?
  [ "$code" -eq 1 ] || fail "$1" "exit $code, want 1"
  ! grep -qE '^(make|docker|aws) ' "$stub/calls" 2> /dev/null || fail "$1" "called: $(cat "$stub/calls")"
}

refused "a run ID and a context that are not of the sandbox" production production production
refused "a cluster that is not the cluster of the run" cpo-ci-1-1-eks cpo-ci-2-1-eks cpo-ci-2-1-eks
refused "a context of another cluster" cpo-ci-1-1-eks cpo-ci-1-1-eks production

name="the cluster of a run"
rm -f "$stub/calls"
RUN_ID=cpo-ci-1-1-eks EKS_CLUSTER=cpo-ci-1-1-eks CONTEXT=cpo-ci-1-1-eks "$here/up.sh" > /dev/null 2>&1
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
grep -q "^make -C $stub/operator helm-deploy IMG=1.dkr.ecr.eu-west-1.amazonaws.com/cpo-sandbox-eks/camunda-operator:cpo-ci-1-1-eks$" \
  "$stub/calls" || fail "$name" "did not install the operator: $(cat "$stub/calls")"

exit "$failed"
