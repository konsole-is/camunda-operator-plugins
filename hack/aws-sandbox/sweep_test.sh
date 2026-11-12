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
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
STUB
chmod +x "$stub/aws"
export PATH="$stub:$PATH" AWS_REGION=eu-west-1

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

name="a role that is gone before the read of its tags"
TAGS_ERROR=NoSuchEntity "$here/sweep.sh" --run cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"

name="a read of the tags that fails"
TAGS_ERROR=AccessDenied "$here/sweep.sh" --run cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"

exit "$failed"
