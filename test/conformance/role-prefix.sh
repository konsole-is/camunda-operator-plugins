#!/usr/bin/env bash
# Checks against real IAM what a plugin that names IAM roles does when its
# role name prefix changes. The requests orders and billing get their roles.
# The script gives the role of billing the UID tag of another request and
# changes the prefix of the release. Then the old role of orders must be
# gone with the event RoleDeleted, and the old role of billing must stay with
# the event RoleKept. Last, the delete of each request must delete its new
# role.
#
# Usage: role-prefix.sh <plugin> <section> <registration> <service-account suffix> <new prefix>
#
# The target of each request is the CamundaCluster <claim> with the
# ServiceAccount <claim>-<service-account suffix>. The roles of the new
# prefix must be inside the policy of the controller. Needs CHART_NAMESPACE,
# the credentials of the CI role, and a kind cluster on which run.sh
# installed the plugin. The old role of billing stays in IAM: the sweep of
# the run removes it.
set -euo pipefail

if [ $# -ne 5 ]; then
  echo "usage: $0 <plugin> <section> <registration> <service-account suffix> <new prefix>" >&2
  exit 2
fi
plugin=$1
section=$2
registration=$3
suffix=$4
prefix=$5
: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
ns=conformance-$registration-prefix

failed=0
fail() {
  echo "FAIL: $*" >&2
  failed=1
}

request() {
  cat << EOF
apiVersion: plugins.camunda.io/v1alpha1
kind: PluginRequest
metadata:
  namespace: $ns
  name: $1
  labels:
    plugins.camunda.io/section: $section
    plugins.camunda.io/registration: $registration
    fleet.camunda.io/claim: $1
spec:
  section: $section
  registration: $registration
  claimRef: {kind: CamundaClusterClaim, name: $1}
  target: {kind: CamundaCluster, name: $1, serviceAccountName: $1-$suffix}
  deletionPolicy: Delete
EOF
}

role_of() {
  kubectl get -n "$ns" "pluginrequest/$1" -o jsonpath='{.status.outputs.principal}' | sed 's|.*/||'
}

uid_tag() {
  aws iam get-role --role-name "$1" --output json |
    jq -r '[.Role.Tags[]? | select(.Key == "camunda.io/request-uid") | .Value][0] // ""'
}

# exists <role> prints yes or no. Any error but NoSuchEntity fails.
exists() {
  local err
  if err=$(aws iam get-role --role-name "$1" 2>&1 > /dev/null); then
    echo yes
  elif [[ $err == *NoSuchEntity* ]]; then
    echo no
  else
    echo "$err" >&2
    return 1
  fi
}

# gone <role> waits up to 2 minutes for IAM to show that the role is gone.
gone() {
  for _ in $(seq 24); do
    if [ "$(exists "$1")" = no ]; then
      return 0
    fi
    sleep 5
  done
  return 1
}

# event <request> <reason> <role> succeeds when the request has an event of
# the reason whose note names the role.
event() {
  kubectl get events.events.k8s.io -n "$ns" -o json |
    jq -e --arg name "$1" --arg reason "$2" --arg role "$3" \
      '[.items[] | select(.regarding.name == $name and .reason == $reason and (.note | contains($role)))] | length > 0' \
      > /dev/null
}

report() {
  kubectl get -n "$ns" pluginrequests -o yaml || true
  kubectl get events.events.k8s.io -n "$ns" -o custom-columns=REGARDING:.regarding.name,REASON:.reason,NOTE:.note || true
  kubectl logs -n "$CHART_NAMESPACE" "deployment/$plugin" --tail=100 || true
}

echo "==> Creating the requests orders and billing of $registration"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
{ request orders; echo ---; request billing; } | kubectl apply -f -
kubectl wait -n "$ns" pluginrequest/orders pluginrequest/billing --for=condition=Ready --timeout=5m
orders_old=$(role_of orders)
billing_old=$(role_of billing)
uid=$(kubectl get -n "$ns" pluginrequest/orders -o jsonpath='{.metadata.uid}')
tag=$(uid_tag "$orders_old")
[ "$tag" = "$uid" ] || fail "the role $orders_old has the request UID tag '$tag', want '$uid'"

echo "==> Stopping $plugin, and giving the role $billing_old the UID of another request"
kubectl scale -n "$CHART_NAMESPACE" "deployment/$plugin" --replicas=0
selector=$(kubectl get -n "$CHART_NAMESPACE" "deployment/$plugin" -o json |
  jq -r '.spec.selector.matchLabels | to_entries | map("\(.key)=\(.value)") | join(",")')
for _ in $(seq 24); do
  if [ -z "$(kubectl get pods -n "$CHART_NAMESPACE" -l "$selector" -o name)" ]; then
    break
  fi
  sleep 5
done
aws iam tag-role --role-name "$billing_old" --tags Key=camunda.io/request-uid,Value=another-uid
# A read of IAM can show the old tag for some seconds after TagRole.
for _ in $(seq 12); do
  if [ "$(uid_tag "$billing_old")" = another-uid ]; then
    break
  fi
  sleep 5
done
sleep 10

echo "==> Starting $plugin with the role name prefix $prefix"
helm upgrade "$plugin" "$repo/charts/$plugin" --namespace "$CHART_NAMESPACE" --reuse-values \
  --set-string "roleNamePrefix=$prefix"
kubectl scale -n "$CHART_NAMESPACE" "deployment/$plugin" --replicas=1
kubectl rollout status -n "$CHART_NAMESPACE" "deployment/$plugin" --timeout=3m

moved=false
for _ in $(seq 36); do
  if [[ $(role_of orders) == "$prefix"-* && $(role_of billing) == "$prefix"-* ]]; then
    moved=true
    break
  fi
  sleep 5
done
if ! $moved; then
  fail "the outputs of orders and billing do not name roles of the prefix $prefix after 3 minutes"
  report
  exit 1
fi
kubectl wait -n "$ns" pluginrequest/orders pluginrequest/billing --for=condition=Ready --timeout=3m
orders_new=$(role_of orders)
billing_new=$(role_of billing)

gone "$orders_old" || fail "the old role $orders_old of orders still exists"
for _ in $(seq 12); do
  if event orders RoleDeleted "$orders_old" && event billing RoleKept "$billing_old"; then
    break
  fi
  sleep 5
done
event orders RoleDeleted "$orders_old" || fail "orders has no event RoleDeleted for $orders_old"
event billing RoleKept "$billing_old" || fail "billing has no event RoleKept for $billing_old"
[ "$(exists "$billing_old")" = yes ] || fail "the old role $billing_old of billing, of another UID, is gone"

echo "==> Deleting the requests"
kubectl delete -n "$ns" pluginrequest/orders pluginrequest/billing --timeout=3m
gone "$orders_new" || fail "the role $orders_new of the deleted request orders still exists"
gone "$billing_new" || fail "the role $billing_new of the deleted request billing still exists"
[ "$(exists "$billing_old")" = yes ] || fail "the delete of billing deleted $billing_old, of another UID"

if [ "$failed" -ne 0 ]; then
  report
  exit 1
fi
kubectl delete namespace "$ns" --timeout=3m
echo "==> The prefix change of $plugin deleted only the old roles of its requests"
