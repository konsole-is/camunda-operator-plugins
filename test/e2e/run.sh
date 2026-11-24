#!/usr/bin/env bash
# Runs one journey on a new kind cluster. It creates the cluster with the
# operator and ECK, runs setup.sh of the journey, and runs the specs of the
# journey. CI only: the cluster stays after the run, for the logs. The
# journey eks runs on the EKS cluster of a run instead, which
# hack/aws-sandbox/eks-cluster.sh created, and installs the operator there.
#
# Usage: run.sh <journey> (make journey-kind and make journey-eks run it)
#
# Inputs:
#   KIND_CLUSTER     the name of the kind cluster (required, except for eks)
#   EKS_CLUSTER      the EKS cluster of the run (required for eks, see
#                    test/eks/up.sh)
#   OPERATOR_DIR     the checkout of the operator (required, see up.sh)
#   CHART_NAMESPACE  the namespace of the plugin releases (required)
#   REPORT           the JUnit report to write (required)
#   CAMUNDA_MINOR    the Camunda minor of the journey (required). run.sh
#                    fails before it creates the cluster when the operator
#                    checkout has no test/e2e/matrix/<minor>.env.
# setup.sh of the journey reads the other variables that it needs.
set -euo pipefail

journey=${1:?usage: run.sh <journey>}
: "${REPORT:?REPORT is not set}"
: "${OPERATOR_DIR:?OPERATOR_DIR is not set}"
: "${CAMUNDA_MINOR:?CAMUNDA_MINOR is not set}"
if [ "$journey" = eks ]; then
  : "${EKS_CLUSTER:?EKS_CLUSTER is not set: make journey-eks runs the eks journey on the EKS cluster of a run}"
fi
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
setup=$repo/test/e2e/$journey/setup.sh
[ -x "$setup" ] || { echo "test/e2e/$journey/setup.sh is missing or not executable" >&2; exit 1; }

# The versions are those that the operator at the api ref was tested with,
# so they move with the api module of go.mod.
matrix=$OPERATOR_DIR/test/e2e/matrix/$CAMUNDA_MINOR.env
[ -f "$matrix" ] || {
  echo "test/e2e/matrix/$CAMUNDA_MINOR.env is not in the operator checkout at $OPERATOR_DIR, so the operator commit of go.mod does not support Camunda $CAMUNDA_MINOR" >&2
  exit 1
}
value() { awk -F= -v key="$1" '$1 == key { print $2 }' "$2"; }
CAMUNDA_VERSION=$(value CAMUNDA_VERSION "$matrix")
ELASTICSEARCH_VERSION=$(value ELASTICSEARCH_VERSION "$matrix")
ECK_VERSION=$(awk '$1 == "ECK_VERSION" { print $3 }' "$OPERATOR_DIR/Makefile")
for v in CAMUNDA_VERSION ELASTICSEARCH_VERSION ECK_VERSION; do
  [ -n "${!v}" ] || { echo "$v is not in the operator checkout at $OPERATOR_DIR" >&2; exit 1; }
done
export CAMUNDA_VERSION ELASTICSEARCH_VERSION ECK_VERSION

case $journey in
  eks) "$repo/test/eks/up.sh" ;;
  *) "$repo/test/kind/up.sh" ;;
esac
"$repo/test/kind/eck.sh"
# The operator watches the ECK kinds only when their CRDs exist at its start.
kubectl rollout restart deployment/camunda-operator-controller-manager -n camunda-operator-system
kubectl rollout status deployment/camunda-operator-controller-manager -n camunda-operator-system --timeout=180s

cd "$repo"
"$setup"

# Ginkgo stops the suite at its limit and still writes the report. go test
# stops a suite that hangs 5 minutes later. Each job keeps time for the steps
# after the journey.
case $journey in
  # A job of 120m: 13m setup, 70m go test, 3m logs, 14m cleanup = 100m.
  aws) suite_timeout=65m test_timeout=70m ;;
  # A step of 45m: 15m images, charts, and ECK, 30m go test.
  eks) suite_timeout=25m test_timeout=30m ;;
  # A job of 90m: 13m setup, 65m go test, 3m logs = 81m.
  *) suite_timeout=60m test_timeout=65m ;;
esac

echo "==> Running the $journey journey"
mkdir -p "$(dirname "$REPORT")"
# go test prints the output of a test that passes only with -v, and the CI
# log shows each spec and the count of the specs that ran.
JOURNEY=$journey go test -tags e2e -count=1 -v -timeout "$test_timeout" -run '^TestJourneys$' ./test/e2e/ \
  -ginkgo.v -ginkgo.timeout="$suite_timeout" -ginkgo.junit-report="$REPORT"
