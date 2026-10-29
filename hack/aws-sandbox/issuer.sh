#!/usr/bin/env bash
# Publishes the OIDC issuer of the kind cluster of one CI run, so that STS
# trusts the ServiceAccount tokens of that cluster.
#
#   issuer.sh up <run-id> <dir>   writes a signing key and a kind config into
#                                 <dir>, publishes the issuer documents, and
#                                 registers the IAM OIDC provider
#   issuer.sh down <run-id>       removes the OIDC provider and the documents
#
# Start kind with `kind create cluster --config <dir>/kind.yaml` after `up`.
# `down` removes only the issuer. sweep.sh --run <run-id> removes what the
# run created with the issuer.
#
# Needs AWS_REGION and AWS_ISSUER_BUCKET, and the credentials of the CI role.
set -euo pipefail

usage() {
  echo "usage: $0 up <run-id> <dir> | down <run-id>" >&2
  exit 2
}

[ $# -ge 2 ] || usage
action=$1
run=$2
# The run ID names the OIDC provider and goes into every resource name.
if ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "the run ID must match cpo-ci-[a-z0-9-]{1,40}: ${run}" >&2
  exit 2
fi

: "${AWS_REGION:?}" "${AWS_ISSUER_BUCKET:?}"
host="${AWS_ISSUER_BUCKET}.s3.${AWS_REGION}.amazonaws.com"
issuer="https://${host}/${run}"
account=$(aws sts get-caller-identity --query Account --output text)
provider="arn:aws:iam::${account}:oidc-provider/${host}/${run}"

b64url() {
  base64 -w0 | tr '+/' '-_' | tr -d '='
}

up() {
  local dir=$1
  mkdir -p "${dir}"
  openssl genrsa -out "${dir}/sa.key" 2048 2>/dev/null
  openssl rsa -in "${dir}/sa.key" -pubout -out "${dir}/sa.pub" 2>/dev/null

  # The kid is the one the API server gives a token: the SHA-256 of the DER
  # form of the public key.
  local kid n
  kid=$(openssl rsa -pubin -in "${dir}/sa.pub" -outform DER 2>/dev/null | openssl dgst -sha256 -binary | b64url)
  n=$(openssl rsa -pubin -in "${dir}/sa.pub" -noout -modulus | cut -d= -f2 | xxd -r -p | b64url)

  cat >"${dir}/openid-configuration" <<EOF
{
  "issuer": "${issuer}",
  "jwks_uri": "${issuer}/openid/v1/jwks",
  "response_types_supported": ["id_token"],
  "subject_types_supported": ["public"],
  "id_token_signing_alg_values_supported": ["RS256"]
}
EOF
  cat >"${dir}/jwks" <<EOF
{"keys": [{"use": "sig", "kty": "RSA", "kid": "${kid}", "alg": "RS256", "n": "${n}", "e": "AQAB"}]}
EOF

  aws s3api put-object --bucket "${AWS_ISSUER_BUCKET}" --key "${run}/.well-known/openid-configuration" \
    --body "${dir}/openid-configuration" --content-type application/json >/dev/null
  aws s3api put-object --bucket "${AWS_ISSUER_BUCKET}" --key "${run}/openid/v1/jwks" \
    --body "${dir}/jwks" --content-type application/json >/dev/null

  aws iam create-open-id-connect-provider --url "${issuer}" --client-id-list sts.amazonaws.com \
    --tags "Key=cpo-ci-run,Value=${run}" >/dev/null

  cat >"${dir}/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraMounts:
      - hostPath: ${dir}
        containerPath: /etc/kubernetes/issuer
        readOnly: true
    kubeadmConfigPatches:
      - |
        kind: ClusterConfiguration
        apiServer:
          extraArgs:
            service-account-issuer: ${issuer}
            service-account-key-file: /etc/kubernetes/issuer/sa.pub
            service-account-signing-key-file: /etc/kubernetes/issuer/sa.key
          extraVolumes:
            - name: issuer
              hostPath: /etc/kubernetes/issuer
              mountPath: /etc/kubernetes/issuer
              readOnly: true
              pathType: Directory
EOF
  echo "issuer=${issuer}"
  echo "provider=${provider}"
}

down() {
  aws iam delete-open-id-connect-provider --open-id-connect-provider-arn "${provider}" 2>/dev/null || true
  aws s3 rm "s3://${AWS_ISSUER_BUCKET}/${run}/" --recursive >/dev/null
}

case ${action} in
  up)
    [ $# -eq 3 ] || usage
    up "$3"
    ;;
  down)
    [ $# -eq 2 ] || usage
    down
    ;;
  *) usage ;;
esac
