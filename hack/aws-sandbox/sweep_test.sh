#!/usr/bin/env bash
# Tests sweep.sh against a stub aws, for a role that another run deletes
# between the listing and the read of its tags. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

# The stub lists one role of another run. TAGS_ERROR is the error of the
# read of its tags.
cat > "$stub/aws" << 'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "iam list-roles") printf 'cpo-ci-other-hub-1\t2026-11-11T00:00:00Z\n' ;;
  "iam list-role-tags") echo "An error occurred ($TAGS_ERROR) when calling the ListRoleTags operation" >&2; exit 254 ;;
  "iam list-policies" | "iam list-open-id-connect-providers" | "s3api list-buckets" | "kms list-keys") ;;
  # KEY_RUN is the run of the one key that the tagging API lists.
  "resourcegroupstaggingapi get-resources")
    if [ -n "${KEY_RUN:-}" ]; then
      echo '{"ResourceTagMappingList": [{"ResourceARN": "arn:aws:kms:eu-west-1:1:key/k-1", "Tags": [{"Key": "cpo-ci-run", "Value": "'"$KEY_RUN"'"}]}]}'
    else
      echo '{"ResourceTagMappingList": []}'
    fi
    ;;
  "kms describe-key") printf 'Enabled\t2026-11-11T00:00:00Z\n' ;;
  "kms list-aliases") ;;
  "kms schedule-key-deletion") echo "$*" >> "$STUB/deleted" ;;
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
STUB
chmod +x "$stub/aws"
export PATH="$stub:$PATH" STUB=$stub AWS_REGION=eu-west-1

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

name="a role that is gone before the read of its tags"
TAGS_ERROR=NoSuchEntity "$here/sweep.sh" --run cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"

name="a key of the run that the tagging API lists"
rm -f "$stub/deleted"
TAGS_ERROR=NoSuchEntity KEY_RUN=cpo-ci-1-1-1 "$here/sweep.sh" --run cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
grep -q -- '--key-id k-1 --pending-window-in-days 7' "$stub/deleted" 2> /dev/null ||
  fail "$name" "did not schedule the deletion of the key"

name="a key of another run"
rm -f "$stub/deleted"
TAGS_ERROR=NoSuchEntity KEY_RUN=cpo-ci-1-1-10 "$here/sweep.sh" --run cpo-ci-1-1-1 > /dev/null 2>&1
[ ! -e "$stub/deleted" ] || fail "$name" "scheduled the deletion of a key of another run"

name="a read of the tags that fails"
TAGS_ERROR=AccessDenied "$here/sweep.sh" --run cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"

exit "$failed"
