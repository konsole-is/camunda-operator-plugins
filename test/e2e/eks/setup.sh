#!/usr/bin/env bash
# Installs what the eks journey needs beside the operator and ECK: the
# default StorageClass gp3, the Pod Identity association of the ServiceAccount
# aws to the role cpo-sandbox-eks-plugins, and one release aws of the chart
# aws with aws-identity, aws-kms, and aws-ebs on, from values.yaml. Then it
# waits until the registrations of the journey are Ready.
#
# Inputs:
#   CHART_NAMESPACE           the namespace of the plugin release (required)
#   RUN_ID                    the run, also the EKS cluster (required)
#   AWS_EKS_PLUGINS_ROLE_ARN  the role of the release (required)
# The scripts that it calls read AWS_REGION, AWS_ACCOUNT_ID,
# AWS_EKS_REGISTRY, and the credentials of the role cpo-sandbox-eks.
set -euo pipefail

: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"
: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_EKS_PLUGINS_ROLE_ARN:?AWS_EKS_PLUGINS_ROLE_ARN is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$here/../../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

kubectl apply -f "$here/storageclass.yaml"
kubectl create namespace "$CHART_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# The association must exist before the pod of the release starts: the Pod
# Identity webhook gives a pod its credentials only at its creation.
echo "==> Giving the ServiceAccount aws its role"
existing=$(aws eks list-pod-identity-associations --cluster-name "$RUN_ID" --namespace "$CHART_NAMESPACE" \
  --service-account aws --query 'associations[0].associationId' --output text)
if [ "$existing" = None ] || [ -z "$existing" ]; then
  existing=$(aws eks create-pod-identity-association --cluster-name "$RUN_ID" --namespace "$CHART_NAMESPACE" \
    --service-account aws --role-arn "$AWS_EKS_PLUGINS_ROLE_ARN" --tags "cpo-ci-run=$RUN_ID" \
    --query association.associationId --output text)
fi
aws eks describe-pod-identity-association --cluster-name "$RUN_ID" --association-id "$existing" > /dev/null

"$repo/test/e2e/render.sh" "$here/values.yaml" > "$work/values.yaml"
"$repo/test/eks/plugin.sh" aws "$work/values.yaml"

# Each controller writes its registration after the pod is Ready, and the
# operator accepts it after that. The three waits share one bound of 4
# minutes, within the step "Run the proof".
registrations=(pluginregistration/aws-pod-identity pluginregistration/aws-kms-key pluginregistration/aws-ebs-encrypted)
echo "==> Waiting for the PluginRegistrations ${registrations[*]}"
kubectl wait "${registrations[@]}" --for=create --timeout=120s
kubectl wait "${registrations[@]}" --for=condition=Accepted --timeout=60s
kubectl wait "${registrations[@]}" --for=condition=Ready --timeout=60s
