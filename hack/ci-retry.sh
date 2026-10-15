#!/usr/bin/env bash
# ci-retry.sh runs a command, and runs it again when it fails, for a maximum
# of five attempts. The pause after each failed attempt is 10 seconds longer
# than the pause before. The output of each attempt stays in the log. When the
# last attempt fails, the script exits with the exit code of that attempt.
#
# Usage: hack/ci-retry.sh <command> [<argument>...]
set -uo pipefail

attempts=5
status=0
for attempt in $(seq 1 "${attempts}"); do
  "$@" && exit 0
  status=$?
  if [ "${attempt}" -lt "${attempts}" ]; then
    echo "Attempt ${attempt} of ${attempts} of '$*' failed with exit code ${status}. The next attempt starts in $((attempt * 10)) seconds." >&2
    sleep $((attempt * 10))
  fi
done
echo "All ${attempts} attempts of '$*' failed." >&2
exit "${status}"
