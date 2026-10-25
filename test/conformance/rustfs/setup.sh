#!/usr/bin/env bash
# Starts the RustFS server that the chart defaults point at, with its admin
# Secret in the release namespace.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
: "${CHART_NAMESPACE:?CHART_NAMESPACE is not set}"

"$here/../../kind/rustfs.sh" "$CHART_NAMESPACE"
