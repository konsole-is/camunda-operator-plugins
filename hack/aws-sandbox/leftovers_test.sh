#!/usr/bin/env bash
# Tests leftovers.sh against a stub aws that answers the four listings from
# JSON files. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

cat > "$stub/aws" << 'EOF'
#!/usr/bin/env bash
case "$1 $2" in
  "iam list-roles") cat "$STUB/roles.json" ;;
  "iam list-policies") cat "$STUB/policies.json" ;;
  "iam list-open-id-connect-providers") cat "$STUB/providers.json" ;;
  "s3api list-buckets") cat "$STUB/buckets.json" ;;
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$stub/aws"
export PATH="$stub:$PATH" STUB=$stub AWS_REGION=eu-west-1

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

# account writes the four listings: roles, policies, provider ARNs, buckets.
account() {
  jq -n --arg v "$1" '{Roles: ($v | split(" ") | map(select(. != "") | {RoleName: .}))}' > "$stub/roles.json"
  jq -n --arg v "$2" '{Policies: ($v | split(" ") | map(select(. != "") | {PolicyName: .}))}' > "$stub/policies.json"
  jq -n --arg v "$3" '{OpenIDConnectProviderList: ($v | split(" ") | map(select(. != "") | {Arn: .}))}' > "$stub/providers.json"
  jq -n --arg v "$4" '{Buckets: ($v | split(" ") | map(select(. != "") | {Name: .}))}' > "$stub/buckets.json"
}

provider=arn:aws:iam::111122223333:oidc-provider/b.s3.eu-west-1.amazonaws.com

name="nothing of the run"
account "cpo-ci-other-hub-1" "" "$provider/cpo-ci-other" ""
out=$("$here/leftovers.sh" cpo-ci-1-1-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="a role and a provider left"
account "cpo-ci-1-1-1-hub-0123456789abcdef cpo-ci-other-hub-1" "cpo-ci-1-1-1-aws-identity" \
  "$provider/cpo-ci-1-1-1" "cpo-ci-1-1-1-backup"
out=$("$here/leftovers.sh" cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
for want in "left: role cpo-ci-1-1-1-hub-0123456789abcdef" "left: policy cpo-ci-1-1-1-aws-identity" \
  "left: OIDC provider $provider/cpo-ci-1-1-1" "left: bucket cpo-ci-1-1-1-backup"; do
  grep -qxF "$want" <<< "$out" || fail "$name" "no line '$want' in '$out'"
done
[ "$(wc -l <<< "$out")" -eq 4 ] || fail "$name" "printed '$out', want 4 lines"

name="a neighbour run"
account "cpo-ci-1-1-10-hub-0123456789abcdef" "cpo-ci-1-1-10-aws-identity" "$provider/cpo-ci-1-1-10" \
  "cpo-ci-1-1-10-backup"
out=$("$here/leftovers.sh" cpo-ci-1-1-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="a listing that fails"
rm "$stub/buckets.json"
"$here/leftovers.sh" cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"

exit "$failed"
