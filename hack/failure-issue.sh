#!/usr/bin/env bash
# Files an issue for a red workflow run on main. When an open issue has the
# title, it adds a comment with the run to it instead, so that one issue
# collects the red runs until someone fixes the cause.
#
# Usage: failure-issue.sh <title> <run URL>
#
# Needs GH_TOKEN and GH_REPO.
set -euo pipefail

title=${1:?usage: failure-issue.sh <title> <run URL>}
url=${2:?usage: failure-issue.sh <title> <run URL>}
body="The workflow failed on main: $url. Find the cause in the logs of the red jobs and fix it. Do not run the workflow again to make it green."

number=$(gh issue list --state open --search "in:title $title" --json number,title --limit 100 |
  jq -r --arg title "$title" '[.[] | select(.title == $title) | .number][0] // ""')
if [ -n "$number" ]; then
  gh issue comment "$number" --body "$body"
else
  gh issue create --title "$title" --body "$body"
fi
