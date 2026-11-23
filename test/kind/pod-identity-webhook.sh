#!/usr/bin/env bash
# Installs cert-manager and amazon-eks-pod-identity-webhook, the webhook that
# EKS runs, in the namespace default. A pod under a ServiceAccount with the
# annotation eks.amazonaws.com/role-arn then gets AWS_ROLE_ARN and a token
# for sts.amazonaws.com, as on EKS. The script ends only after the webhook
# changed a pod.
#
# Usage: pod-identity-webhook.sh
#
# Inputs:
#   KIND_CLUSTER                  the name of the kind cluster (required)
#   CERT_MANAGER_VERSION          the cert-manager release (required)
#   POD_IDENTITY_WEBHOOK_VERSION  the webhook release (required)
#   AWS_REGION                    the region of the STS endpoint (required)
set -euo pipefail

: "${KIND_CLUSTER:?KIND_CLUSTER is not set}"
: "${CERT_MANAGER_VERSION:?CERT_MANAGER_VERSION is not set}"
: "${POD_IDENTITY_WEBHOOK_VERSION:?POD_IDENTITY_WEBHOOK_VERSION is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "==> Installing cert-manager $CERT_MANAGER_VERSION"
kubectl apply -f \
  "https://github.com/cert-manager/cert-manager/releases/download/$CERT_MANAGER_VERSION/cert-manager.yaml"
kubectl wait --for=condition=Available deployment --all -n cert-manager --timeout=180s

echo "==> Installing amazon-eks-pod-identity-webhook $POD_IDENTITY_WEBHOOK_VERSION"
for f in auth.yaml deployment-base.yaml service.yaml mutatingwebhook.yaml; do
  curl -fsSL --retry 5 --retry-delay 10 --retry-all-errors -o "$work/$f" \
    "https://raw.githubusercontent.com/aws/amazon-eks-pod-identity-webhook/$POD_IDENTITY_WEBHOOK_VERSION/deploy/$f"
done
image=public.ecr.aws/eks/amazon-eks-pod-identity-webhook:$POD_IDENTITY_WEBHOOK_VERSION
# public.ecr.aws refuses an anonymous pull with 429 Too Many Requests when
# several jobs pull at the same time. So the runner pulls the image with
# retries and loads it into kind, and the node does not pull it.
echo "==> Loading $image into kind"
# The archive holds only the platform of the node. An import of a
# multi-platform index looks for the layers of each platform.
platform=linux/$(docker version --format '{{.Server.Arch}}')
"$here/../../hack/ci-retry.sh" docker pull --platform "$platform" "$image"
docker image save --platform "$platform" -o "$work/webhook.tar" "$image"
kind load image-archive --name "$KIND_CLUSTER" "$work/webhook.tar"
sed -i \
  -e "s|image: IMAGE|image: $image|" \
  -e "s|imagePullPolicy: Always|imagePullPolicy: IfNotPresent|" \
  -e "s|^\( *\)- --token-audience=sts.amazonaws.com$|&\n\1- --aws-default-region=$AWS_REGION\n\1- --sts-regional-endpoint=true|" \
  "$work/deployment-base.yaml"
grep -q -- "--aws-default-region=$AWS_REGION" "$work/deployment-base.yaml" ||
  { echo "deployment-base.yaml of $POD_IDENTITY_WEBHOOK_VERSION has no --token-audience line" >&2; exit 1; }
grep -q "imagePullPolicy: IfNotPresent" "$work/deployment-base.yaml" ||
  { echo "deployment-base.yaml of $POD_IDENTITY_WEBHOOK_VERSION has no imagePullPolicy: Always line" >&2; exit 1; }

kubectl apply -f "$work/auth.yaml"
# The file holds a Certificate, which the cert-manager webhook can refuse
# for some seconds after its Deployment is Available.
"$here/../../hack/ci-retry.sh" kubectl apply -f "$work/deployment-base.yaml"
kubectl apply -f "$work/service.yaml" -f "$work/mutatingwebhook.yaml"
kubectl wait -n default certificate/pod-identity-webhook --for=condition=Ready --timeout=120s
kubectl rollout status -n default deployment/pod-identity-webhook --timeout=180s

# The webhook has failurePolicy Ignore, so a pod that is made before it
# serves gets no credentials and no error.
echo "==> Waiting until the webhook changes a pod"
check=pod-identity-webhook-check
kubectl create serviceaccount "$check" -n default --dry-run=client -o yaml | kubectl apply -f -
kubectl annotate serviceaccount "$check" -n default --overwrite \
  "eks.amazonaws.com/role-arn=arn:aws:iam::111122223333:role/$check"
for _ in $(seq 24); do
  if kubectl run "$check" -n default --image=busybox --restart=Never --dry-run=server -o json \
    --overrides="{\"spec\":{\"serviceAccountName\":\"$check\"}}" |
    jq -e '.spec.containers[0].env[]? | select(.name == "AWS_ROLE_ARN")' > /dev/null; then
    kubectl delete serviceaccount "$check" -n default
    echo "==> The pod identity webhook is up"
    exit 0
  fi
  sleep 5
done
echo "the pod identity webhook did not change a pod in 120 s" >&2
exit 1
