#!/usr/bin/env bash
# Tests plugin-role.sh against a stub aws that logs each call and answers
# with ARNs. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

cat > "$stub/aws" << 'STUB'
#!/usr/bin/env bash
echo "$*" >> "$STUB/calls"
case "$1 $2" in
  "sts get-caller-identity") echo 111122223333 ;;
  "iam create-policy")
    shift 2
    while [ "$1" != --policy-name ]; do shift; done
    echo "arn:aws:iam::111122223333:policy/$2"
    ;;
  "iam create-role") echo arn:aws:iam::111122223333:role/r ;;
  "iam attach-role-policy") ;;
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
STUB
chmod +x "$stub/aws"
export PATH="$stub:$PATH" STUB=$stub AWS_REGION=eu-west-1 AWS_ISSUER_BUCKET=issuer
for f in controller-policy-kms.json controller-policy-s3.json reader-policy.json; do
  echo '{}' > "$stub/$f"
done

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

# created prints the names of the created policies, then the ARNs that the
# role got, one for each line.
created() {
  grep '^iam create-policy' "$stub/calls" | sed -E 's/.*--policy-name ([^ ]+).*/\1/'
  grep '^iam attach-role-policy' "$stub/calls" | sed -E 's/.*--policy-arn ([^ ]+).*/\1/'
}

name="one policy file"
: > "$stub/calls"
"$here/plugin-role.sh" cpo-ci-1-1-0 journey-reader aws-reader aws-reader "$stub/reader-policy.json" > /dev/null ||
  fail "$name" "exit $?, want 0"
want="cpo-ci-1-1-0-journey-reader
arn:aws:iam::111122223333:policy/cpo-ci-1-1-0-journey-reader"
[ "$(created)" = "$want" ] || fail "$name" "created and attached '$(created)', want '$want'"

name="one policy for each file"
: > "$stub/calls"
"$here/plugin-role.sh" cpo-ci-1-1-0 aws camunda-operator-plugins aws \
  "$stub/controller-policy-kms.json" "$stub/controller-policy-s3.json" > /dev/null ||
  fail "$name" "exit $?, want 0"
want="cpo-ci-1-1-0-aws-kms
cpo-ci-1-1-0-aws-s3
arn:aws:iam::111122223333:policy/cpo-ci-1-1-0-aws-kms
arn:aws:iam::111122223333:policy/cpo-ci-1-1-0-aws-s3"
[ "$(created)" = "$want" ] || fail "$name" "created and attached '$(created)', want '$want'"

name="no policy file"
: > "$stub/calls"
"$here/plugin-role.sh" cpo-ci-1-1-0 aws camunda-operator-plugins aws > /dev/null 2>&1
code=$?
[ "$code" -eq 2 ] || fail "$name" "exit $code, want 2"
[ ! -s "$stub/calls" ] || fail "$name" "called aws: $(cat "$stub/calls")"

exit "$failed"
