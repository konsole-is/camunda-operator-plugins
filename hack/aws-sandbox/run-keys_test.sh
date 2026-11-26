#!/usr/bin/env bash
# Tests run-keys.sh against a stub aws that answers the tagging API and the
# KMS calls from JSON files. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

# A missing tagging.json makes the tagging API refuse the call, as for a
# role without tag:GetResources. A key without tags-<key>.json answers
# AccessDenied, as for a key of no run.
cat > "$stub/aws" << 'STUB'
#!/usr/bin/env bash
echo "$*" >> "$STUB/calls"
arg() {
  local name=$1
  shift
  while [ $# -gt 1 ]; do
    [ "$1" != "$name" ] || { echo "$2"; return; }
    shift
  done
}
case "$1 $2" in
  "resourcegroupstaggingapi get-resources")
    cat "$STUB/tagging.json" 2> /dev/null || { echo "An error occurred (AccessDeniedException)" >&2; exit 254; }
    ;;
  "kms list-keys") cat "$STUB/keys.json" 2> /dev/null || { echo "An error occurred (InternalError)" >&2; exit 254; } ;;
  "kms list-resource-tags")
    key=$(arg --key-id "$@")
    [ ! -f "$STUB/tags-$key.error" ] || { echo "An error occurred ($(cat "$STUB/tags-$key.error"))" >&2; exit 254; }
    cat "$STUB/tags-$key.json" 2> /dev/null || { echo "An error occurred (AccessDeniedException)" >&2; exit 254; }
    ;;
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
STUB
chmod +x "$stub/aws"
export PATH="$stub:$PATH" STUB=$stub

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

name="the tagging API"
rm -f "$stub"/*.json "$stub/calls"
cat > "$stub/tagging.json" << 'JSON'
{"ResourceTagMappingList": [
  {"ResourceARN": "arn:aws:kms:eu-west-1:1:key/k-1", "Tags": [{"Key": "cpo-ci-run", "Value": "cpo-ci-1-1-1"}]},
  {"ResourceARN": "arn:aws:kms:eu-west-1:1:key/k-2", "Tags": [{"Key": "CPO-CI-RUN", "Value": "cpo-ci-2-1-1"}]},
  {"ResourceARN": "arn:aws:kms:eu-west-1:1:key/k-3", "Tags": [{"Key": "team", "Value": "x"}]},
  {"ResourceARN": "arn:aws:kms:eu-west-1:1:key/k-4", "Tags": []}
]}
JSON
out=$("$here/run-keys.sh" eu-west-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
want=$'k-1 cpo-ci-1-1-1\nk-2 cpo-ci-2-1-1'
[ "$out" = "$want" ] || fail "$name" "printed '$out', want '$want'"
! grep -q '^kms ' "$stub/calls" || fail "$name" "called KMS: $(grep '^kms ' "$stub/calls")"
grep -q -- '--region eu-west-1' "$stub/calls" || fail "$name" "did not ask the region"

name="a role without the tagging API"
rm -f "$stub"/*.json "$stub/calls"
echo '{"Keys": [{"KeyId": "k-1"}, {"KeyId": "k-5"}]}' > "$stub/keys.json"
echo '{"Tags": [{"TagKey": "cpo-ci-run", "TagValue": "cpo-ci-1-1-1"}]}' > "$stub/tags-k-1.json"
out=$("$here/run-keys.sh" eu-west-1 2> /dev/null)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ "$out" = "k-1 cpo-ci-1-1-1" ] || fail "$name" "printed '$out', want 'k-1 cpo-ci-1-1-1'"

name="a read of the tags of a key that fails for another reason"
rm -f "$stub"/*.json "$stub"/*.error "$stub/calls"
echo '{"Keys": [{"KeyId": "k-1"}, {"KeyId": "k-6"}]}' > "$stub/keys.json"
echo '{"Tags": [{"TagKey": "cpo-ci-run", "TagValue": "cpo-ci-1-1-1"}]}' > "$stub/tags-k-1.json"
echo ThrottlingException > "$stub/tags-k-6.error"
out=$("$here/run-keys.sh" eu-west-1 2> /dev/null)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="a listing that fails both ways"
rm -f "$stub"/*.json "$stub/calls"
out=$("$here/run-keys.sh" eu-west-1 2> /dev/null)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

exit "$failed"
