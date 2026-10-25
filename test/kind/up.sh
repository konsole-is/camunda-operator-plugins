#!/usr/bin/env bash
# Creates a kind cluster and installs the operator from its chart, built from
# a checkout of konsole-is/camunda-operator. The operator brings the CRDs, the
# core plugin sections, the registry of the registrations, and the grant
# admission policy. CI only: never run it against a cluster that you use.
#
# Usage: up.sh
#
# Inputs:
#   KIND_CLUSTER  the name of the kind cluster (required)
#   OPERATOR_DIR  the checkout of the operator, as make operator-source
#                 writes it (required)
set -euo pipefail

: "${KIND_CLUSTER:?KIND_CLUSTER is not set}"
: "${OPERATOR_DIR:?OPERATOR_DIR is not set}"
operator_image=camunda-operator:kind

echo "==> Creating the kind cluster $KIND_CLUSTER"
kind create cluster --name "$KIND_CLUSTER" --wait 120s

echo "==> Building the operator image and loading it into kind"
make -C "$OPERATOR_DIR" docker-build IMG="$operator_image"
kind load docker-image --name "$KIND_CLUSTER" "$operator_image"

echo "==> Installing the operator from its chart"
make -C "$OPERATOR_DIR" helm-generate IMG="$operator_image"
make -C "$OPERATOR_DIR" helm-deploy IMG="$operator_image"

echo "==> The operator is up"
