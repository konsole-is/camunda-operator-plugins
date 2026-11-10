#!/usr/bin/env bash
# Runs one journey on a new kind cluster. It creates the cluster with the
# operator and ECK, runs setup.sh of the journey, and runs the specs of the
# journey. CI only: the cluster stays after the run, for the logs.
#
# Usage: run.sh <journey> (make journey-kind runs it)
#
# Inputs:
#   KIND_CLUSTER     the name of the kind cluster (required)
#   OPERATOR_DIR     the checkout of the operator (required, see up.sh)
#   CHART_NAMESPACE  the namespace of the plugin releases (required)
#   REPORT           the JUnit report to write (required)
#   CAMUNDA_MINOR    the Camunda minor of the journey, a matrix file of the
#                    operator checkout (required)
# setup.sh of the journey reads the other variables that it needs.
set -euo pipefail

journey=${1:?usage: run.sh <journey>}
: "${REPORT:?REPORT is not set}"
: "${OPERATOR_DIR:?OPERATOR_DIR is not set}"
: "${CAMUNDA_MINOR:?CAMUNDA_MINOR is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
setup=$repo/test/e2e/$journey/setup.sh
[ -x "$setup" ] || { echo "test/e2e/$journey/setup.sh is missing or not executable" >&2; exit 1; }

# The versions are those that the operator at the api ref was tested with,
# so they move with the api module of go.mod.
matrix=$OPERATOR_DIR/test/e2e/matrix/$CAMUNDA_MINOR.env
value() { awk -F= -v key="$1" '$1 == key { print $2 }' "$2"; }
CAMUNDA_VERSION=$(value CAMUNDA_VERSION "$matrix")
ELASTICSEARCH_VERSION=$(value ELASTICSEARCH_VERSION "$matrix")
ECK_VERSION=$(awk '$1 == "ECK_VERSION" { print $3 }' "$OPERATOR_DIR/Makefile")
for v in CAMUNDA_VERSION ELASTICSEARCH_VERSION ECK_VERSION; do
  [ -n "${!v}" ] || { echo "$v is not in the operator checkout at $OPERATOR_DIR" >&2; exit 1; }
done
export CAMUNDA_VERSION ELASTICSEARCH_VERSION ECK_VERSION

"$repo/test/kind/up.sh"
"$repo/test/kind/eck.sh"
# The operator watches the ECK kinds only when their CRDs exist at its start.
kubectl rollout restart deployment/camunda-operator-controller-manager -n camunda-operator-system
kubectl rollout status deployment/camunda-operator-controller-manager -n camunda-operator-system --timeout=180s

cd "$repo"
"$setup"

echo "==> Running the $journey journey"
mkdir -p "$(dirname "$REPORT")"
JOURNEY=$journey go test -tags e2e -count=1 -timeout 60m -run '^TestJourneys$' ./test/e2e/ \
  -ginkgo.v -ginkgo.junit-report="$REPORT"
