#!/usr/bin/env bash
# Removes what one EKS run created in the sandbox account: eks-sweep.sh --run
# for the node, the cluster, the volumes, the images, and the schedules, then
# sweep.sh --run for the hub role and the KMS key. It fails when
# leftovers.sh --eks still finds anything of the run. It runs all three also
# when one fails, so a run ID can be torn down again until it exits 0.
#
# Usage: eks-teardown.sh <run-id>
#
# Needs AWS_REGION, AWS_EKS_VPC_ID, and the credentials of the role
# cpo-sandbox-eks or of an administrator.
set -uo pipefail

run=${1:-}
if ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "usage: eks-teardown.sh <run-id>, where the run ID matches cpo-ci-[a-z0-9-]{1,40}: ${run}" >&2
  exit 2
fi
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

failed=0
"${here}/eks-sweep.sh" --run "${run}" || failed=1
"${here}/sweep.sh" --run "${run}" || failed=1
"${here}/leftovers.sh" --eks "${run}" || failed=1
exit "${failed}"
