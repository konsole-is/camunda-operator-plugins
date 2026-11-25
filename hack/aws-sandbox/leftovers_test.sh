#!/usr/bin/env bash
# Tests leftovers.sh against a stub aws that answers each listing from a JSON
# file of $STUB, and for another region from $STUB/<region>/. make
# test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

# The four IAM and S3 listings fail without their file. Each other listing
# answers an empty list without its file, and a tag read of a key without a
# file is refused, as for a key of no run.
cat > "$stub/aws" << 'EOF'
#!/usr/bin/env bash
arg() {
  local name=$1
  shift
  while [ $# -gt 1 ]; do
    [ "$1" != "$name" ] || { echo "$2"; return; }
    shift
  done
}
dir=$STUB
region=$(arg --region "$@")
[ -z "$region" ] || [ "$region" = "$AWS_REGION" ] || dir=$STUB/$region
answer() { cat "$dir/$1" 2> /dev/null || echo "$2"; }
case "$1 $2" in
  "sts get-caller-identity") echo 111122223333 ;;
  "iam list-roles") cat "$STUB/roles.json" ;;
  "iam list-policies") cat "$STUB/policies.json" ;;
  "iam list-open-id-connect-providers") cat "$STUB/providers.json" ;;
  "s3api list-buckets") cat "$STUB/buckets.json" ;;
  "eks list-clusters") answer clusters.json '{"clusters": []}' ;;
  "eks describe-cluster")
    cat "$dir/cluster-$(arg --name "$@").json" 2> /dev/null ||
      { echo "An error occurred (ResourceNotFoundException)" >&2; exit 254; }
    ;;
  "ec2 describe-instances") answer instances.json '{"Reservations": []}' ;;
  "ec2 describe-volumes") answer volumes.json '{"Volumes": []}' ;;
  "ec2 describe-network-interfaces") answer interfaces.json '{"NetworkInterfaces": []}' ;;
  "ec2 describe-security-groups") answer groups.json '{"SecurityGroups": []}' ;;
  "ec2 describe-nat-gateways") answer nats.json '{"NatGateways": []}' ;;
  "ec2 describe-addresses") answer addresses.json '{"Addresses": []}' ;;
  "ec2 describe-regions") answer regions.json '{"Regions": []}' ;;
  "elb describe-load-balancers") answer elbs.json '{"LoadBalancerDescriptions": []}' ;;
  "elbv2 describe-load-balancers") answer elbv2s.json '{"LoadBalancers": []}' ;;
  "logs describe-log-groups") answer logs.json '{"logGroups": []}' ;;
  "ecr describe-images")
    repo=$(arg --repository-name "$@")
    answer "images-${repo##*/}.json" '{"imageDetails": []}'
    ;;
  "scheduler list-schedules") answer schedules.json '{"Schedules": []}' ;;
  "kms list-keys") answer keys.json '{"Keys": []}' ;;
  "resourcegroupstaggingapi get-resources") answer tagging.json '{"ResourceTagMappingList": []}' ;;
  "kms list-resource-tags")
    key=$(arg --key-id "$@")
    cat "$dir/tags-$key.json" 2> /dev/null || { echo "An error occurred (AccessDeniedException)" >&2; exit 254; }
    ;;
  # key-<key>.error holds the error of describe-key for that key.
  "kms describe-key")
    key=$(arg --key-id "$@")
    if [ -f "$dir/key-$key.error" ]; then
      echo "An error occurred ($(cat "$dir/key-$key.error")) when calling the DescribeKey operation" >&2
      exit 254
    fi
    cat "$dir/key-$key.json"
    ;;
  "kms list-aliases") answer aliases.json '{"Aliases": []}' ;;
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

name="a role of another run that trusts another account"
account "cpo-ci-other-hub-1" "" "" ""
jq '.Roles[0].AssumeRolePolicyDocument = {Statement: [{Effect: "Allow", Action: "sts:AssumeRole",
  Principal: {AWS: "arn:aws:iam::999999999999:root"}}]}' "$stub/roles.json" > "$stub/x" && mv "$stub/x" "$stub/roles.json"
out=$("$here/leftovers.sh" cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ "$out" = "left: role with a trust of no run cpo-ci-other-hub-1" ] ||
  fail "$name" "printed '$out', want the role"

name="a role of another run that trusts EKS Pod Identity"
jq '.Roles[0].AssumeRolePolicyDocument = {Statement: [{Effect: "Allow", Action: ["sts:AssumeRole", "sts:TagSession"],
  Principal: {Service: "pods.eks.amazonaws.com"}}]}' "$stub/roles.json" > "$stub/x" && mv "$stub/x" "$stub/roles.json"
out=$("$here/leftovers.sh" cpo-ci-1-1-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="a listing that fails"
account "" "" "" ""
rm "$stub/buckets.json"
"$here/leftovers.sh" cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"

# The EKS listings. Each object carries the run tag of its run.
tagged() { jq -cn --arg run "$1" '[{Key: "cpo-ci-run", Value: $run}]'; }
eks_account() {
  rm -rf "$stub"/*.json "$stub"/*.error "$stub"/eu-central-1
  account "" "" "" ""
  echo '{"clusters": []}' > "$stub/clusters.json"
}
add_cluster() { jq --arg c "$1" '.clusters += [$c]' "$stub/clusters.json" > "$stub/x" && mv "$stub/x" "$stub/clusters.json"; }
add_instance() {
  local file=${4:-$stub/instances.json}
  [ -f "$file" ] || echo '{"Reservations": []}' > "$file"
  jq --arg id "$1" --arg run "$2" --arg state "$3" \
    '.Reservations += [{Instances: [{InstanceId: $id, State: {Name: $state}, Tags: [{Key: "cpo-ci-run", Value: $run}]}]}]' \
    "$file" > "$stub/x" && mv "$stub/x" "$file"
}
add_volume() {
  [ -f "$stub/volumes.json" ] || echo '{"Volumes": []}' > "$stub/volumes.json"
  jq --arg id "$1" --argjson tags "$2" '.Volumes += [{VolumeId: $id, State: "available", Tags: $tags}]' \
    "$stub/volumes.json" > "$stub/x" && mv "$stub/x" "$stub/volumes.json"
}
add_image() {
  local file=$stub/images-$1.json
  [ -f "$file" ] || echo '{"imageDetails": []}' > "$file"
  jq --arg tag "$2" '.imageDetails += [{imageTags: [$tag]}]' "$file" > "$stub/x" && mv "$stub/x" "$file"
}
add_key() {
  local dir=${4:-$stub}
  mkdir -p "$dir"
  [ -f "$dir/keys.json" ] || echo '{"Keys": []}' > "$dir/keys.json"
  jq --arg k "$1" '.Keys += [{KeyId: $k}]' "$dir/keys.json" > "$stub/x" && mv "$stub/x" "$dir/keys.json"
  jq -n --arg run "$2" '{Tags: [{TagKey: "cpo-ci-run", TagValue: $run}]}' > "$dir/tags-$1.json"
  jq -n --arg state "$3" '{KeyMetadata: {KeyState: $state}}' > "$dir/key-$1.json"
  [ -f "$dir/tagging.json" ] || echo '{"ResourceTagMappingList": []}' > "$dir/tagging.json"
  jq --arg k "$1" --arg run "$2" '.ResourceTagMappingList += [{ResourceARN: ("arn:aws:kms:eu-west-1:1:key/" + $k),
    Tags: [{Key: "cpo-ci-run", Value: $run}]}]' "$dir/tagging.json" > "$stub/x" && mv "$stub/x" "$dir/tagging.json"
}
export AWS_EKS_VPC_ID=vpc-1

name="--eks nothing of the run"
eks_account
add_cluster cpo-ci-1-1-10
add_instance i-9 cpo-ci-1-1-10 running
add_volume vol-9 "$(tagged cpo-ci-1-1-10)"
add_image aws cpo-ci-1-1-10
echo '{"Schedules": [{"Name": "cpo-ci-1-1-10-delete-cluster"}]}' > "$stub/schedules.json"
add_instance i-8 cpo-ci-1-1-1 terminated
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="--eks a cluster, a node, a volume, an image, and a schedule left"
eks_account
add_cluster cpo-ci-1-1-1
add_instance i-1 cpo-ci-1-1-1 running
add_volume vol-1 "$(tagged cpo-ci-1-1-1)"
add_image aws cpo-ci-1-1-1
echo '{"Schedules": [{"Name": "cpo-ci-1-1-1-delete-cluster"}]}' > "$stub/schedules.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
want="left: EKS cluster cpo-ci-1-1-1
left: EC2 instance i-1
left: EBS volume vol-1
left: image cpo-sandbox-eks/aws:cpo-ci-1-1-1
left: schedule cpo-ci-1-1-1-delete-cluster"
[ "$(sort <<< "$out")" = "$(sort <<< "$want")" ] || fail "$name" "printed '$out', want '$want'"

name="--eks a cluster of the run with another name"
eks_account
add_cluster other
add_cluster cpo-ci-1-1-10
echo '{"cluster": {"name": "other", "tags": {"cpo-ci-run": "cpo-ci-1-1-1"}}}' > "$stub/cluster-other.json"
echo '{"cluster": {"name": "cpo-ci-1-1-10", "tags": {"cpo-ci-run": "cpo-ci-1-1-10"}}}' > "$stub/cluster-cpo-ci-1-1-10.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ "$out" = "left: EKS cluster other" ] || fail "$name" "printed '$out'"

name="--eks a cluster of the run with the run tag in upper case"
eks_account
add_cluster other
echo '{"cluster": {"name": "other", "tags": {"CPO-CI-RUN": "cpo-ci-1-1-1"}}}' > "$stub/cluster-other.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ "$out" = "left: EKS cluster other" ] || fail "$name" "printed '$out'"

name="--eks the security group of a gone cluster with another name"
eks_account
add_cluster live
echo '{"cluster": {"name": "live", "tags": {}}}' > "$stub/cluster-live.json"
echo '{"SecurityGroups": [
  {"GroupId": "sg-7", "GroupName": "eks-cluster-sg-other-123", "Tags": [{"Key": "aws:eks:cluster-name", "Value": "other"}]},
  {"GroupId": "sg-8", "GroupName": "eks-cluster-sg-live-456", "Tags": [{"Key": "aws:eks:cluster-name", "Value": "live"}]},
  {"GroupId": "sg-9", "GroupName": "default"}]}' > "$stub/groups.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ "$out" = "left: security group sg-7" ] || fail "$name" "printed '$out', want only the group of the gone cluster"

name="--eks a volume of the driver without the run tag"
eks_account
add_volume vol-2 '[{"Key": "ebs.csi.aws.com/cluster", "Value": "true"}]'
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing: no volume of a run lacks the run tag"

name="--eks a key listing that fails"
eks_account
echo 'not json' > "$stub/tagging.json"
rm -f "$stub/keys.json"
echo 'not json' > "$stub/keys.json"
"$here/leftovers.sh" --eks cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1: a failed read is no empty list"

name="--eks a NAT gateway in the region"
eks_account
echo '{"NatGateways": [{"NatGatewayId": "nat-1"}]}' > "$stub/nats.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ "$out" = "left: NAT gateway nat-1" ] || fail "$name" "printed '$out'"

name="--eks a network interface or a load balancer in the VPC"
eks_account
echo '{"NetworkInterfaces": [{"NetworkInterfaceId": "eni-1"}]}' > "$stub/interfaces.json"
echo '{"LoadBalancers": [{"LoadBalancerName": "lb-1", "VpcId": "vpc-1"}, {"LoadBalancerName": "lb-2", "VpcId": "vpc-2"}]}' \
  > "$stub/elbv2s.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
want="left: network interface eni-1
left: load balancer lb-1"
[ "$out" = "$want" ] || fail "$name" "printed '$out', want '$want'"

name="--eks a key of the run that is not pending deletion"
eks_account
add_key k-1 cpo-ci-1-1-1 Enabled
add_key k-2 cpo-ci-1-1-10 Enabled
echo '{"Aliases": [{"AliasName": "alias/cpo-ci-1-1-1/eks/orders"}, {"AliasName": "alias/cpo-ci-1-1-10/eks/orders"}]}' \
  > "$stub/aliases.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
want="left: KMS key k-1
left: KMS alias alias/cpo-ci-1-1-1/eks/orders"
[ "$out" = "$want" ] || fail "$name" "printed '$out', want '$want'"

name="--eks a key of the run that is pending deletion"
eks_account
add_key k-1 cpo-ci-1-1-1 PendingDeletion
add_key k-3 "" Enabled
rm "$stub/tags-k-3.json"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="--eks a key that KMS deleted, which the tagging API still lists"
eks_account
add_key k-1 cpo-ci-1-1-1 Enabled
echo NotFoundException > "$stub/key-k-1.error"
out=$("$here/leftovers.sh" --eks cpo-ci-1-1-1 2> /dev/null)
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
[ -z "$out" ] || fail "$name" "printed '$out', want nothing"

name="--eks a read of a key that fails"
eks_account
add_key k-1 cpo-ci-1-1-1 Enabled
echo AccessDeniedException > "$stub/key-k-1.error"
"$here/leftovers.sh" --eks cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"

name="--eks --all-regions an instance in another region"
eks_account
echo '{"Regions": [{"RegionName": "eu-west-1"}, {"RegionName": "eu-central-1"}]}' > "$stub/regions.json"
mkdir -p "$stub/eu-central-1"
add_instance i-2 cpo-ci-1-1-1 running "$stub/eu-central-1/instances.json"
out=$("$here/leftovers.sh" --eks --all-regions cpo-ci-1-1-1)
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
[ "$out" = "left: EC2 instance in eu-central-1 i-2" ] || fail "$name" "printed '$out'"

name="--eks a listing that fails"
eks_account
echo 'not json' > "$stub/volumes.json"
"$here/leftovers.sh" --eks cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"

name="--eks without AWS_EKS_VPC_ID"
eks_account
AWS_EKS_VPC_ID='' "$here/leftovers.sh" --eks cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 2 ] || fail "$name" "exit $code, want 2"

exit "$failed"
