#!/usr/bin/env bash
# The check of aws-connectors-role that needs real IAM, after the suite:
# role-prefix.sh, the change of the role name prefix.
set -euo pipefail

: "${RUN_ID:?RUN_ID is not set}"
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)

"$repo/test/conformance/role-prefix.sh" aws-connectors-role connectors-permissions aws-connectors-role \
  connectors "$RUN_ID-conn-b"
