#!/usr/bin/env bash
# Gives the one release aws of every controller its role in the AWS
# sandbox, with sandbox-setup.sh.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

"$here/../sandbox-setup.sh" "$work"
