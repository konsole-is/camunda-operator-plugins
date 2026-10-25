#!/usr/bin/env bash
# Starts a RustFS server at http://rustfs.rustfs.svc:9000, the default
# endpoint of the rustfs chart, and writes its admin key into the Secret
# rustfs-admin of a namespace, as the chart expects it. The data lives in an
# emptyDir.
#
# Usage: rustfs.sh <namespace of the admin Secret>
#
# Inputs:
#   RUSTFS_VERSION  the tag of the image rustfs/rustfs (required)
set -euo pipefail

namespace=${1:?usage: rustfs.sh <namespace of the admin Secret>}
: "${RUSTFS_VERSION:?RUSTFS_VERSION is not set}"
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
access_key=rustfsadmin
secret_key=$(openssl rand -hex 20)

echo "==> Starting RustFS $RUSTFS_VERSION"
for ns in rustfs "$namespace"; do
  kubectl create namespace "$ns" --dry-run=client -o yaml | kubectl apply -f -
done
for ns in rustfs "$namespace"; do
  kubectl create secret generic rustfs-admin -n "$ns" \
    --from-literal=accessKey="$access_key" --from-literal=secretKey="$secret_key" \
    --dry-run=client -o yaml | kubectl apply -f -
done
sed "s|__RUSTFS_VERSION__|$RUSTFS_VERSION|" "$here/rustfs.yaml" | kubectl apply -n rustfs -f -
kubectl rollout status deployment/rustfs -n rustfs --timeout=180s
