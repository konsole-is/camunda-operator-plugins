#!/usr/bin/env bash
# The checks after the suites of two releases in one namespace: aws with
# every controller but aws-kms, and aws-keys with only aws-kms.
#   1. Each release holds its own Lease, its ServiceAccount has serve only
#      on its own registrations, and aws starts no controller aws-kms.
#   2. identity-check.sh and connectors-role-check.sh against the release aws.
#   3. The move of aws-kms into the release aws, as the docs give it: a key
#      of aws-keys never leaves Ready and keeps its key through the uninstall
#      of aws-keys and the upgrade of aws. The release aws serves its delete.
#
# Needs RUN_ID, AWS_REGION, CHART_NAMESPACE, the variables of the two checks,
# and the credentials of the CI role.
set -euo pipefail

: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../../.." && pwd)
ns=conformance-move

failed=0
fail() {
  echo "FAIL: $*" >&2
  failed=1
}

# started <release> prints the controllers that the Pod of the release
# started, one for each line.
started() {
  kubectl logs -n "$CHART_NAMESPACE" "deployment/$1" --tail=-1 |
    jq -rR 'fromjson? | select(.msg == "Starting Controller") | .controller' | sort -u
}

report() {
  kubectl get pluginregistrations -o wide || true
  kubectl get leases -n "$CHART_NAMESPACE" -o wide || true
  kubectl get -n "$ns" pluginrequests -o yaml || true
}

echo "==> Checking the Lease of each release"
for release in aws aws-keys; do
  pod=$(kubectl get pods -n "$CHART_NAMESPACE" -l "app.kubernetes.io/instance=$release" \
    -o jsonpath='{.items[0].metadata.name}')
  holder=$(kubectl get lease -n "$CHART_NAMESPACE" "$release" -o jsonpath='{.spec.holderIdentity}' || true)
  [[ -n $pod && $holder == "$pod"* ]] ||
    fail "the Lease $release is held by '$holder', want the Pod '$pod' of the release $release"
done

echo "==> Checking that each ServiceAccount has serve only on its own registrations"
own_aws=(aws-irsa aws-connectors-role aws-connectors-existing-role aws-ebs-encrypted aws-s3)
own_keys=(aws-kms-key aws-kms-key-multi-region)
for sa in aws aws-keys; do
  for registration in "${own_aws[@]}" "${own_keys[@]}"; do
    want=no
    if [[ $sa == aws && " ${own_aws[*]} " == *" $registration "* ]] ||
      [[ $sa == aws-keys && " ${own_keys[*]} " == *" $registration "* ]]; then
      want=yes
    fi
    got=$(kubectl auth can-i serve "pluginregistrations.plugins.camunda.io/$registration" \
      --as="system:serviceaccount:$CHART_NAMESPACE:$sa" || true)
    [ "$got" = "$want" ] || fail "can the ServiceAccount $sa serve $registration: '$got', want $want"
  done
done
for pair in aws-kms-key=aws-keys aws-irsa=aws; do
  got=$(kubectl get "pluginregistration/${pair%%=*}" -o jsonpath='{.spec.serviceAccount.name}')
  [ "$got" = "${pair#*=}" ] || fail "the registration ${pair%%=*} names the ServiceAccount '$got', want ${pair#*=}"
done
if grep -qx aws-kms <<< "$(started aws)"; then
  fail "the release aws started the controller aws-kms, which is off in it"
fi
[ "$(started aws-keys)" = aws-kms ] ||
  fail "the release aws-keys started '$(started aws-keys | paste -sd ' ' -)', want only aws-kms"

"$here/../identity-check.sh" || failed=1
"$here/../connectors-role-check.sh" || failed=1

echo "==> Creating the key moved-key of the release aws-keys"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f - << YAML
apiVersion: plugins.camunda.io/v1alpha1
kind: PluginRequest
metadata:
  name: moved-key
  namespace: $ns
  labels:
    plugins.camunda.io/section: encryption-key
    plugins.camunda.io/registration: aws-kms-key
    fleet.camunda.io/claim: moved-key
spec:
  section: encryption-key
  registration: aws-kms-key
  claimRef: {kind: CamundaClusterClaim, name: moved-key}
  target: {kind: CamundaCluster, name: moved-key, serviceAccountName: moved-key-camunda}
  deletionPolicy: Delete
  parameters: {pendingWindowDays: 7}
YAML
kubectl wait -n "$ns" pluginrequest/moved-key --for=condition=Ready --timeout=5m
key=$(kubectl get -n "$ns" pluginrequest/moved-key -o jsonpath='{.status.outputs.keyRef}')
[ -n "$key" ] || { fail "moved-key has no keyRef"; report; exit 1; }
# A Ready condition that turns False and back gets a new lastTransitionTime.
ready_since=$(kubectl get -n "$ns" pluginrequest/moved-key \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}')

echo "==> Moving aws-kms from the release aws-keys into the release aws"
helm uninstall aws-keys --namespace "$CHART_NAMESPACE" --wait
helm upgrade aws "$repo/charts/aws" --namespace "$CHART_NAMESPACE" --reuse-values \
  --set kms.enabled=true --wait --timeout 3m
kubectl wait pluginregistration/aws-kms-key --for=jsonpath='{.spec.serviceAccount.name}'=aws --timeout=3m
kubectl wait pluginregistration/aws-kms-key --for=condition=Ready --timeout=3m
kubectl wait -n "$ns" pluginrequest/moved-key --for=condition=Ready --timeout=5m
got=$(kubectl get -n "$ns" pluginrequest/moved-key -o jsonpath='{.status.outputs.keyRef}')
[ "$got" = "$key" ] || fail "moved-key has the keyRef '$got' after the move, want $key"
got=$(kubectl get -n "$ns" pluginrequest/moved-key \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}')
[ "$got" = "$ready_since" ] ||
  fail "Ready of moved-key changed at $got during the move, want it Ready since $ready_since"
state=$(aws kms describe-key --region "$AWS_REGION" --key-id "$key" --query KeyMetadata.KeyState --output text)
[ "$state" = Enabled ] || fail "the key $key of moved-key is $state after the move, want Enabled"

echo "==> Deleting moved-key through the release aws"
if ! kubectl delete -n "$ns" pluginrequest/moved-key --timeout=5m; then
  fail "the release aws did not serve the delete of moved-key within 5 minutes"
fi
state=$(aws kms describe-key --region "$AWS_REGION" --key-id "$key" --query KeyMetadata.KeyState --output text)
[ "$state" = PendingDeletion ] || fail "the key $key of the deleted moved-key is $state, want PendingDeletion"

if [ "$failed" -ne 0 ]; then
  report
fi
kubectl delete namespace "$ns" --timeout=3m || failed=1
exit "$failed"
