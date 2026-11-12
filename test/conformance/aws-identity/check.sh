#!/usr/bin/env bash
# The checks of aws-identity that need real IAM and STS, after the suite:
#   - the sandbox tests of pkg/awskit, the IAM behavior that the fake cannot
#     show
#   - a pod under the ServiceAccount of a request gets the credentials of
#     the role from STS, and a pod under another ServiceAccount with the
#     same role annotation gets none
#   - role-prefix.sh, the change of the role name prefix
#
# Needs RUN_ID, AWS_ACCOUNT_ID, AWS_REGION, AWS_CLI_IMAGE, CHART_NAMESPACE,
# and the credentials of the CI role.
set -euo pipefail

: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_ACCOUNT_ID:?AWS_ACCOUNT_ID is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
: "${AWS_CLI_IMAGE:?AWS_CLI_IMAGE is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
ns=conformance-aws-identity-sts

failed=0
fail() {
  echo "FAIL: $*" >&2
  failed=1
}

echo "==> Running the sandbox tests of pkg/awskit"
(cd "$repo" && go test -tags sandbox -count=1 -v -run '^TestSandbox' ./pkg/awskit/) ||
  fail "the sandbox tests of pkg/awskit"

# run_pod <name> <service account> runs aws sts get-caller-identity in a pod
# and sets phase and logs.
run_pod() {
  kubectl delete pod -n "$ns" "$1" --ignore-not-found --wait
  kubectl apply -f - << YAML
apiVersion: v1
kind: Pod
metadata: {name: $1, namespace: $ns}
spec:
  serviceAccountName: $2
  restartPolicy: Never
  containers:
    - name: aws
      image: $AWS_CLI_IMAGE
      args: [sts, get-caller-identity, --output, json]
      env:
        - {name: AWS_REGION, value: $AWS_REGION}
        - {name: AWS_EC2_METADATA_DISABLED, value: "true"}
YAML
  phase=""
  for _ in $(seq 60); do
    phase=$(kubectl get pod -n "$ns" "$1" -o jsonpath='{.status.phase}')
    case $phase in Succeeded | Failed) break ;; esac
    sleep 2
  done
  logs=$(kubectl logs -n "$ns" "$1" 2>&1 || true)
  echo "$1 under $2: $phase"
  echo "$logs"
}

echo "==> Checking that STS gives the role of a request only to its ServiceAccount"
kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$here/request.yaml"
kubectl wait -n "$ns" pluginrequest/sts-check --for=condition=Ready --timeout=5m
principal=$(kubectl get -n "$ns" pluginrequest/sts-check -o jsonpath='{.status.outputs.principal}')
# The webhook gives both pods the role ARN and a token. Only the role trusts
# one of them.
for sa in trusted stranger; do
  kubectl create serviceaccount -n "$ns" "$sa" --dry-run=client -o yaml | kubectl apply -f -
  kubectl annotate serviceaccount -n "$ns" "$sa" --overwrite "eks.amazonaws.com/role-arn=$principal"
done

# IAM can refuse a new trust policy for some seconds.
for _ in 1 2 3; do
  run_pod trusted trusted
  if [ "$phase" = Succeeded ]; then
    break
  fi
  sleep 20
done
arn=$(jq -r .Arn <<< "$logs" 2> /dev/null || true)
[ "$phase" = Succeeded ] || fail "the pod under trusted ended $phase, want Succeeded"
[[ $arn == "arn:aws:sts::$AWS_ACCOUNT_ID:assumed-role/${principal##*/}/"* ]] ||
  fail "the pod under trusted is '$arn', want the assumed role ${principal##*/}"

run_pod stranger stranger
kubectl get pod -n "$ns" stranger -o json |
  jq -e '.spec.containers[0].env[]? | select(.name == "AWS_ROLE_ARN")' > /dev/null ||
  fail "the webhook did not give the pod under stranger AWS_ROLE_ARN"
[ "$phase" = Failed ] || fail "the pod under stranger ended $phase, want Failed"
[[ $logs == *AccessDenied* && $logs == *AssumeRoleWithWebIdentity* ]] ||
  fail "the pod under stranger did not fail on AccessDenied of AssumeRoleWithWebIdentity"

if [ "$failed" -ne 0 ]; then
  kubectl get -n "$ns" pluginrequest/sts-check -o yaml || true
fi
kubectl delete -n "$ns" pluginrequest/sts-check --timeout=3m
kubectl delete namespace "$ns" --timeout=3m

"$repo/test/conformance/role-prefix.sh" aws-identity cloud-identity aws-irsa camunda "$RUN_ID-hub-b" ||
  failed=1
exit "$failed"
