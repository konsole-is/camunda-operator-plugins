#!/usr/bin/env bash
# Tests failure-issue.sh against a stub gh that logs each call and answers
# issue list from a JSON file. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

cat > "$stub/gh" << 'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$STUB/calls"
case "$1 $2" in
  "issue list") cat "$STUB/list.json" ;;
  "issue create" | "issue comment") ;;
  *) echo "unexpected call: gh $*" >&2; exit 3 ;;
esac
STUB
chmod +x "$stub/gh"
export PATH="$stub:$PATH" STUB=$stub GH_REPO=konsole-is/camunda-operator-plugins

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

# run <case> <issue list JSON> files the issue and leaves the calls in $calls.
run() {
  : > "$stub/calls"
  printf '%s\n' "$2" > "$stub/list.json"
  "$here/failure-issue.sh" "Conformance failed on main" https://example.com/run/1
  code=$?
  [ "$code" -eq 0 ] || fail "$1" "exit $code, want 0"
  calls=$(grep -v '^issue list' "$stub/calls")
}

name="no open issue"
run "$name" '[{"number": 3, "title": "Conformance failed on main at abc"}]'
[ "$(grep -c '' <<< "$calls")" -eq 1 ] || fail "$name" "calls '$calls', want one"
[[ $calls == "issue create --title Conformance failed on main --body "*https://example.com/run/1* ]] ||
  fail "$name" "calls '$calls', want issue create with the run URL"

name="an open issue"
run "$name" '[{"number": 3, "title": "Conformance failed on main at abc"}, {"number": 7, "title": "Conformance failed on main"}]'
[ "$(grep -c '' <<< "$calls")" -eq 1 ] || fail "$name" "calls '$calls', want one"
[[ $calls == "issue comment 7 --body "*https://example.com/run/1* ]] ||
  fail "$name" "calls '$calls', want issue comment 7 with the run URL"

exit "$failed"
