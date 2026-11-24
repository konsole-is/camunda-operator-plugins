#!/usr/bin/env bash
# Tests eks-limits.sh against a stub aws, whose simulator answers from rules
# in $STUB/decisions. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

# Each line of decisions is "<regex> <decision>", or "-<regex> <decision>"
# for a call that does not match. The first rule that holds for the
# arguments of a simulator call gives its decision; no rule gives allowed.
# FAIL_ACTION makes the call of that action fail.
cat > "$stub/aws" << 'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "sts get-caller-identity") echo 111122223333 ;;
  "iam simulate-principal-policy")
    args="$*"
    echo "$args" >> "$STUB/calls"
    if [ -n "${FAIL_ACTION:-}" ] && [[ $args == *"--action-names $FAIL_ACTION "* ]]; then
      echo "An error occurred (InvalidInput)" >&2
      exit 254
    fi
    while read -r rule decision; do
      [ -n "$rule" ] || continue
      if [[ $rule == -* ]]; then
        [[ $args =~ ${rule#-} ]] || { echo "$decision"; exit 0; }
      elif [[ $args =~ $rule ]]; then
        echo "$decision"
        exit 0
      fi
    done < "$STUB/decisions"
    echo allowed
    ;;
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

# The rules of a sandbox whose limits hold. A create without the run tag is
# denied by the absence of an allow.
limits() {
  cat << 'EOF'
m5\.4xlarge explicitDeny
HopLimit,ContextKeyValues=2, explicitDeny
us-east-1 explicitDeny
EXTENDED explicitDeny
loggingType/api explicitDeny
io2 explicitDeny
VolumeSize,ContextKeyValues=100, explicitDeny
VolumeIops,ContextKeyValues=16000, explicitDeny
action-names.(ec2:CreateNatGateway|ec2:AllocateAddress|elasticloadbalancing:CreateLoadBalancer|eks:CreateNodegroup|logs:CreateLogGroup|iam:CreateRole|ec2:CreateSnapshot|ec2:ModifyVolume|kms:PutKeyPolicy) explicitDeny
-cpo-ci-run implicitDeny
EOF
}

name="every case as expected"
limits > "$stub/decisions"
rm -f "$stub/calls"
out=$("$here/eks-limits.sh")
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0: $out"
[ -n "$out" ] || fail "$name" "printed nothing"
bad=$(sed '$d' <<< "$out" | grep -v '^ok ')
[ -z "$bad" ] || fail "$name" "lines that are not ok: $bad"
cases=$(sed '$d' <<< "$out" | wc -l)
[ "$(tail -1 <<< "$out")" = "eks-limits: $cases ok, 0 failed" ] || fail "$name" "the last line is '$(tail -1 <<< "$out")'"
grep -q -- '--policy-source-arn arn:aws:iam::111122223333:role/cpo-sandbox-eks ' "$stub/calls" ||
  fail "$name" "no call for the role cpo-sandbox-eks"
grep -q -- 'ContextKeyName=aws:RequestedRegion,ContextKeyValues=eu-west-1,ContextKeyType=string' "$stub/calls" ||
  fail "$name" "no call in the region of the sandbox"

name="a NAT gateway that the role can create"
{ echo 'role/cpo-sandbox-eks.--action-names.ec2:CreateNatGateway allowed'; limits; } > "$stub/decisions"
out=$("$here/eks-limits.sh")
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
grep -qxF "FAIL ec2:CreateNatGateway by cpo-sandbox-eks: allowed, want explicitDeny" <<< "$out" ||
  fail "$name" "no FAIL line for the NAT gateway in: $out"
[[ $(tail -1 <<< "$out") =~ ^eks-limits:\ [0-9]+\ ok,\ 1\ failed$ ]] || fail "$name" "the last line is '$(tail -1 <<< "$out")'"

name="an allowed case that is denied"
{ echo 'eks:CreateCluster.*STANDARD.*cpo-ci-run explicitDeny'; limits; } > "$stub/decisions"
out=$("$here/eks-limits.sh")
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
grep -q "^FAIL eks:CreateCluster by cpo-sandbox-eks with eks:supportType=string:STANDARD aws:RequestTag/cpo-ci-run=string:cpo-ci-limits: explicitDeny, want allowed$" <<< "$out" ||
  fail "$name" "no FAIL line for the cluster in: $out"

name="a simulator call that fails"
limits > "$stub/decisions"
out=$(FAIL_ACTION=kms:PutKeyPolicy "$here/eks-limits.sh" 2> /dev/null)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
grep -q '^FAIL kms:PutKeyPolicy by cpo-sandbox-eks-plugins: the simulator call failed$' <<< "$out" ||
  fail "$name" "no FAIL line for the failed call in: $out"

exit "$failed"
