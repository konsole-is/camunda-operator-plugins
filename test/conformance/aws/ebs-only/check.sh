#!/usr/bin/env bash
# The checks of a release with only aws-ebs on, after the suite: the release
# publishes no registration of the other controllers, serves no request of
# them, and starts no controller but aws-ebs.
#
# Needs CHART_NAMESPACE and the kind cluster of run.sh with the release aws.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
ns=conformance-aws-ebs-only

failed=0
fail() {
  echo "FAIL: $*" >&2
  failed=1
}

echo "==> Checking that the release publishes no registration of another controller"
for r in aws-irsa aws-connectors-role aws-connectors-existing-role aws-kms-key aws-kms-key-multi-region aws-s3; do
  if kubectl get "pluginregistration/$r" > /dev/null 2>&1; then
    fail "the PluginRegistration $r exists, but only aws-ebs is on"
  fi
done

echo "==> Checking that the release serves no request of aws-kms-key"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f - << YAML
apiVersion: plugins.camunda.io/v1alpha1
kind: PluginRequest
metadata:
  name: kms-request
  namespace: $ns
  labels:
    plugins.camunda.io/section: encryption-key
    plugins.camunda.io/registration: aws-kms-key
    fleet.camunda.io/claim: kms-request
spec:
  section: encryption-key
  registration: aws-kms-key
  claimRef: {kind: CamundaClusterClaim, name: kms-request}
  target: {kind: CamundaCluster, name: kms-request, serviceAccountName: kms-request-camunda}
  deletionPolicy: Delete
YAML
for _ in $(seq 6); do
  finalizers=$(kubectl get -n "$ns" pluginrequest/kms-request -o jsonpath='{.metadata.finalizers}')
  if [[ $finalizers == *plugins.camunda.io/cleanup* ]]; then
    fail "the request kms-request of aws-kms-key got the finalizer plugins.camunda.io/cleanup"
    break
  fi
  sleep 5
done

echo "==> Checking that the release starts only the controller aws-ebs"
started=$(kubectl logs -n "$CHART_NAMESPACE" deployment/aws --tail=-1 |
  jq -rR 'fromjson? | select(.msg == "Starting Controller") | .controller' | sort -u)
[ "$started" = aws-ebs ] ||
  fail "the release started the controllers '$(paste -sd ' ' <<< "$started")', want only aws-ebs"

if [ "$failed" -ne 0 ]; then
  kubectl get pluginregistrations -o wide || true
  kubectl get -n "$ns" pluginrequest/kms-request -o yaml || true
fi
kubectl delete -n "$ns" pluginrequest/kms-request --timeout=1m || failed=1
kubectl delete namespace "$ns" --timeout=3m || failed=1
exit "$failed"
