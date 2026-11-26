#!/usr/bin/env bash
# Installs the operator from its chart on the EKS cluster of a run, built from
# a checkout of konsole-is/camunda-operator, as test/kind/up.sh does on kind.
# The image goes to the sandbox ECR repository cpo-sandbox-eks/camunda-operator
# with the tag of the run, which eks-teardown.sh deletes. CI only.
#
# Usage: up.sh
#
# Inputs:
#   EKS_CLUSTER       the EKS cluster of the run, also the kubeconfig context
#                     (required)
#   OPERATOR_DIR      the checkout of the operator, as make operator-source
#                     writes it (required)
#   AWS_EKS_REGISTRY  the ECR registry of the sandbox (required)
#   RUN_ID            the run, also the image tag (required). It must match
#                     cpo-ci-[a-z0-9-]{1,40}, and EKS_CLUSTER must be RUN_ID.
#   AWS_REGION        the region of the registry (required)
# It exits 1 before it builds or installs anything when an input is not set,
# when RUN_ID or EKS_CLUSTER breaks the rule above, or when the kubeconfig
# context is not EKS_CLUSTER.
set -euo pipefail

: "${EKS_CLUSTER:?EKS_CLUSTER is not set}"
: "${OPERATOR_DIR:?OPERATOR_DIR is not set}"
: "${AWS_EKS_REGISTRY:?AWS_EKS_REGISTRY is not set}"
: "${RUN_ID:?RUN_ID is not set}"
: "${AWS_REGION:?AWS_REGION is not set}"
if ! [[ $RUN_ID =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "RUN_ID $RUN_ID is not a run in the AWS sandbox: it must match cpo-ci-[a-z0-9-]{1,40}" >&2
  exit 1
fi
if [ "$EKS_CLUSTER" != "$RUN_ID" ]; then
  echo "EKS_CLUSTER $EKS_CLUSTER is not the cluster $RUN_ID of the run" >&2
  exit 1
fi
current=$(kubectl config current-context)
if [ "$current" != "$EKS_CLUSTER" ]; then
  echo "the current kubeconfig context is $current, not $EKS_CLUSTER of the EKS cluster" >&2
  exit 1
fi
operator_image=$AWS_EKS_REGISTRY/cpo-sandbox-eks/camunda-operator:$RUN_ID

echo "==> Building the operator image and pushing it to ECR"
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$AWS_EKS_REGISTRY"
make -C "$OPERATOR_DIR" docker-build IMG="$operator_image"
docker push "$operator_image"

echo "==> Installing the operator from its chart"
make -C "$OPERATOR_DIR" helm-generate IMG="$operator_image"
make -C "$OPERATOR_DIR" helm-deploy IMG="$operator_image"

echo "==> The operator is up"
