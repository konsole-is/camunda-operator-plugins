#!/usr/bin/env bash
# Removes what one CI run created in the sandbox account: sweep.sh --run,
# then issuer.sh down. It fails when leftovers.sh still finds a role,
# policy, OIDC provider, or bucket of the run. It runs all three also when
# one fails.
#
# Usage: teardown.sh <run-id>
#
# Delete the kind cluster of the run first, so that no controller creates an
# object again. Needs AWS_REGION, AWS_ISSUER_BUCKET, and the credentials of
# the CI role. AWS_KMS_REPLICA_REGION is optional, as for sweep.sh.
set -uo pipefail

run=${1:?usage: teardown.sh <run-id>}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

failed=0
"${here}/sweep.sh" --run "${run}" || failed=1
"${here}/issuer.sh" down "${run}" || failed=1
"${here}/leftovers.sh" "${run}" || failed=1
exit "${failed}"
