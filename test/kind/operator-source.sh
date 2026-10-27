#!/usr/bin/env bash
# Checks out konsole-is/camunda-operator at a ref into a directory, so that a
# kind cluster can run the operator that matches the api module of go.mod.
#
# Usage: operator-source.sh <directory> (make operator-source runs it)
#
# Inputs:
#   API_REF        the commit or tag to check out (required)
#   OPERATOR_REPO  the repository to clone (required)
set -euo pipefail

dir=${1:?usage: operator-source.sh <directory>}
: "${API_REF:?API_REF is not set}"
: "${OPERATOR_REPO:?OPERATOR_REPO is not set}"

if [ ! -d "$dir/.git" ]; then
  git clone --filter=blob:none --no-checkout "$OPERATOR_REPO" "$dir"
fi
# A short commit is not a ref that a fetch can name, so the fetch takes every
# branch and tag, and the checkout resolves the commit.
git -C "$dir" fetch --tags --force origin '+refs/heads/*:refs/remotes/origin/*'
git -C "$dir" checkout --force --detach "$API_REF"
echo "Checked out $OPERATOR_REPO at $API_REF in $dir"
