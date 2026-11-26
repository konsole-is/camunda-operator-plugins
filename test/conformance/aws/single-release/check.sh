#!/usr/bin/env bash
# The checks after the suites of the release with every controller on:
# s3-check.sh, the checks of aws-s3 that need real S3.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

"$here/../s3-check.sh"
