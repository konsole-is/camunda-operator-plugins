#!/usr/bin/env bash
# shellcheck disable=SC2016 # $run, $id, and $at are variables of jq, not of the shell
# Tests eks-sweep.sh against a stub aws that answers from JSON files of
# $STUB and writes each call to $STUB/calls. A terminate or a delete changes
# the files, as AWS would. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

# FAIL_CALL makes the call "<service> <command>" fail.
cat > "$stub/aws" << 'EOF'
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
if [ "${FAIL_CALL:-}" = "$1 $2" ]; then
  echo "An error occurred (InternalError) when calling $2" >&2
  exit 254
fi
edit() { jq "${@:2}" "$STUB/$1" > "$STUB/edit" && mv "$STUB/edit" "$STUB/$1"; }
case "$1 $2" in
  "ec2 describe-instances") cat "$STUB/instances.json" ;;
  "ec2 terminate-instances")
    shift 2
    while [ $# -gt 0 ]; do
      [ "$1" = --instance-ids ] || [[ $1 == --* ]] || edit instances.json --arg id "$1" \
        '(.Reservations[].Instances[] | select(.InstanceId == $id) | .State.Name) = "terminated"'
      shift
    done
    ;;
  "eks list-clusters") jq '{clusters: [.[].name]}' "$STUB/clusters.json" ;;
  "eks describe-cluster")
    jq -e --arg n "$(arg --name "$@")" '{cluster: (.[] | select(.name == $n))}' "$STUB/clusters.json" 2> /dev/null ||
      { echo "An error occurred (ResourceNotFoundException) when calling DescribeCluster" >&2; exit 254; }
    ;;
  "eks delete-cluster") edit clusters.json --arg n "$(arg --name "$@")" 'map(select(.name != $n))' ;;
  "ec2 describe-volumes") cat "$STUB/volumes.json" ;;
  "ec2 delete-volume") edit volumes.json --arg id "$(arg --volume-id "$@")" '.Volumes |= map(select(.VolumeId != $id))' ;;
  "ec2 describe-network-interfaces") cat "$STUB/interfaces.json" ;;
  "ec2 describe-security-groups") cat "$STUB/groups.json" ;;
  "ecr describe-images") repo=$(arg --repository-name "$@"); cat "$STUB/images-${repo##*/}.json" ;;
  "ecr batch-delete-image") echo '{"imageIds": [], "failures": []}' ;;
  "scheduler list-schedules") cat "$STUB/schedules.json" ;;
  "logs describe-log-groups") cat "$STUB/logs.json" ;;
  "ec2 wait" | "eks wait" | "ec2 delete-network-interface" | "ec2 delete-security-group" | \
    "scheduler delete-schedule" | "logs delete-log-group") ;;
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
EOF
chmod +x "$stub/aws"
export PATH="$stub:$PATH" STUB=$stub AWS_REGION=eu-west-1 AWS_EKS_VPC_ID=vpc-1

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

old=2026-01-01T00:00:00Z
young=$(date -u +%FT%TZ)

empty() {
  rm -f "$stub"/*.json "$stub/calls"
  echo '{"Reservations": []}' > "$stub/instances.json"
  echo '[]' > "$stub/clusters.json"
  echo '{"Volumes": []}' > "$stub/volumes.json"
  echo '{"NetworkInterfaces": []}' > "$stub/interfaces.json"
  echo '{"SecurityGroups": []}' > "$stub/groups.json"
  echo '{"imageDetails": []}' > "$stub/images-camunda-operator.json"
  echo '{"imageDetails": []}' > "$stub/images-aws.json"
  echo '{"Schedules": []}' > "$stub/schedules.json"
  echo '{"logGroups": []}' > "$stub/logs.json"
}
add() { jq "${@:2}" "$stub/$1" > "$stub/edit" && mv "$stub/edit" "$stub/$1"; }

# a_run <run> <instance> <volume> <age> <cluster status> adds the cluster, the
# node, a volume of the driver, an image, and the two schedules of a run.
a_run() {
  local run=$1 instance=$2 volume=$3 at=$4 status=$5
  add instances.json --arg id "$instance" --arg run "$run" --arg at "$at" \
    '.Reservations += [{Instances: [{InstanceId: $id, LaunchTime: $at, State: {Name: "running"},
      Tags: [{Key: "cpo-ci-run", Value: $run}]}]}]'
  add clusters.json --arg run "$run" --arg at "$at" --arg status "$status" \
    '. += [{name: $run, status: $status, createdAt: $at, tags: {"cpo-ci-run": $run}}]'
  add volumes.json --arg id "$volume" --arg run "$run" --arg at "$at" \
    '.Volumes += [{VolumeId: $id, State: "available", CreateTime: $at,
      Tags: [{Key: "cpo-ci-run", Value: $run}, {Key: "ebs.csi.aws.com/cluster", Value: "true"}]}]'
  add images-aws.json --arg run "$run" --arg at "$at" '.imageDetails += [{imageTags: [$run], imagePushedAt: $at}]'
  add schedules.json --arg run "$run" --arg at "$at" \
    '.Schedules += [{Name: ($run + "-delete-cluster"), CreationDate: $at}, {Name: ($run + "-terminate-nodes"), CreationDate: $at}]'
}

# line_of <pattern> prints the number of the first call that matches.
line_of() { grep -n -m1 -e "$1" "$stub/calls" | cut -d: -f1; }
called() { grep -q -e "$1" "$stub/calls"; }

name="an old run"
empty
a_run cpo-ci-1-1-1 i-1 vol-1 "$old" ACTIVE
"$here/eks-sweep.sh" --older-than 2 > /dev/null
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
for want in "ec2 terminate-instances --instance-ids i-1" "eks delete-cluster --name cpo-ci-1-1-1" \
  "ec2 delete-volume --volume-id vol-1" "ecr batch-delete-image --repository-name cpo-sandbox-eks/aws --image-ids imageTag=cpo-ci-1-1-1" \
  "scheduler delete-schedule --group-name cpo-sandbox-eks --name cpo-ci-1-1-1-delete-cluster" \
  "scheduler delete-schedule --group-name cpo-sandbox-eks --name cpo-ci-1-1-1-terminate-nodes"; do
  called "$want" || fail "$name" "no call '$want'"
done
[ "$(line_of 'terminate-instances')" -lt "$(line_of 'eks delete-cluster')" ] ||
  fail "$name" "the cluster is deleted before the node is terminated"
last_delete=$(grep -n -E ' (delete|terminate|batch-delete)' "$stub/calls" | tail -1)
[[ $last_delete == *"scheduler delete-schedule"* ]] || fail "$name" "the last delete is '$last_delete', not a schedule"

name="a young run"
empty
a_run cpo-ci-1-1-1 i-1 vol-1 "$young" ACTIVE
"$here/eks-sweep.sh" --older-than 2 > /dev/null
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
! grep -E ' (delete-|terminate-|batch-delete)' "$stub/calls" || fail "$name" "removed a part of a young run"

name="the run and its neighbour"
empty
a_run cpo-ci-1-1-1 i-1 vol-1 "$young" ACTIVE
a_run cpo-ci-1-1-10 i-10 vol-10 "$young" ACTIVE
echo '{"NetworkInterfaces": [{"NetworkInterfaceId": "eni-1", "Status": "available"}]}' > "$stub/interfaces.json"
"$here/eks-sweep.sh" --run cpo-ci-1-1-1 > /dev/null
code=$?
[ "$code" -eq 0 ] || fail "$name" "exit $code, want 0"
for want in "terminate-instances --instance-ids i-1$" "delete-cluster --name cpo-ci-1-1-1$" "delete-volume --volume-id vol-1$" \
  "imageTag=cpo-ci-1-1-1 --output json" "delete-schedule --group-name cpo-sandbox-eks --name cpo-ci-1-1-1-delete-cluster$"; do
  called "$want" || fail "$name" "no call '$want'"
done
! grep -E 'i-10|cpo-ci-1-1-10|vol-10|delete-network-interface' "$stub/calls" | grep -E 'delete|terminate' ||
  fail "$name" "removed a part of the neighbour, or a network interface while its cluster is up"

name="a cluster that is deleting"
empty
add clusters.json --arg at "$old" '. += [{name: "cpo-ci-1-1-1", status: "DELETING", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-1-1-1"}}]'
"$here/eks-sweep.sh" --run cpo-ci-1-1-1 > /dev/null
! called "eks delete-cluster" || fail "$name" "deleted a cluster that is deleting"
[ "$(grep -c 'eks wait cluster-deleted --name cpo-ci-1-1-1' "$stub/calls")" -eq 1 ] || fail "$name" "did not wait once for the delete"

name="a cluster that failed to create"
empty
add clusters.json --arg at "$old" '. += [{name: "cpo-ci-1-1-1", status: "FAILED", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-1-1-1"}}]'
"$here/eks-sweep.sh" --older-than 2 > /dev/null
called "eks delete-cluster --name cpo-ci-1-1-1" || fail "$name" "did not delete the cluster"

name="a cluster that is still creating"
empty
add clusters.json --arg at "$young" '. += [{name: "cpo-ci-1-1-1", status: "CREATING", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-1-1-1"}}]'
"$here/eks-sweep.sh" --run cpo-ci-1-1-1 > /dev/null
[ -n "$(line_of 'eks wait cluster-active --name cpo-ci-1-1-1')" ] &&
  [ "$(line_of 'eks wait cluster-active')" -lt "$(line_of 'eks delete-cluster --name cpo-ci-1-1-1')" ] ||
  fail "$name" "did not wait for the cluster before its delete"

name="a cluster of the run with another name"
empty
add clusters.json --arg at "$young" '. += [
  {name: "in-vpc", status: "ACTIVE", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-1-1-1"}, resourcesVpcConfig: {vpcId: "vpc-1"}},
  {name: "elsewhere", status: "ACTIVE", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-1-1-1"}, resourcesVpcConfig: {vpcId: "vpc-9"}},
  {name: "kept", status: "ACTIVE", createdAt: $at, tags: {}}]'
"$here/eks-sweep.sh" --run cpo-ci-1-1-1 > /dev/null
called "eks delete-cluster --name in-vpc" || fail "$name" "did not delete the cluster with the run tag in the VPC"
! called "eks delete-cluster --name elsewhere" || fail "$name" "deleted a cluster with another name outside the VPC"
! called "eks delete-cluster --name kept" || fail "$name" "deleted a cluster without the run tag"

name="the security group of a cluster of the run with another name"
empty
add clusters.json --arg at "$young" '. += [
  {name: "other", status: "ACTIVE", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-1-1-1"}, resourcesVpcConfig: {vpcId: "vpc-1"}},
  {name: "live", status: "ACTIVE", createdAt: $at, tags: {}, resourcesVpcConfig: {vpcId: "vpc-1"}}]'
echo '{"SecurityGroups": [
  {"GroupId": "sg-7", "GroupName": "eks-cluster-sg-other-123", "Tags": [{"Key": "aws:eks:cluster-name", "Value": "other"}]},
  {"GroupId": "sg-8", "GroupName": "eks-cluster-sg-live-456", "Tags": [{"Key": "aws:eks:cluster-name", "Value": "live"}]},
  {"GroupId": "sg-9", "GroupName": "eks-cluster-sg-gone-789", "Tags": [{"Key": "aws:eks:cluster-name", "Value": "gone"}]}]}' \
  > "$stub/groups.json"
"$here/eks-sweep.sh" --run cpo-ci-1-1-1 > /dev/null
called "ec2 delete-security-group --group-id sg-7" || fail "$name" "did not delete the group of the deleted cluster other"
! called "sg-8" || fail "$name" "deleted the group of a live cluster"
! called "sg-9" || fail "$name" "--run deleted the group of a cluster of no run of its own"
rm -f "$stub/calls"
"$here/eks-sweep.sh" --older-than 2 > /dev/null
called "ec2 delete-security-group --group-id sg-9" || fail "$name" "--older-than did not delete the orphan group"
! called "sg-8" || fail "$name" "--older-than deleted the group of a live cluster"

name="a cluster whose run tag does not start with cpo-ci-"
empty
add clusters.json --arg at "$old" '. += [{name: "cpo-ci-9", status: "ACTIVE", createdAt: $at, tags: {"cpo-ci-run": "prod"}}]'
"$here/eks-sweep.sh" --older-than 2 > /dev/null
! called "eks delete-cluster" || fail "$name" "deleted a cluster with a run tag of another shape"

name="interfaces while another run's cluster is up"
empty
echo '{"NetworkInterfaces": [{"NetworkInterfaceId": "eni-1", "Status": "available"}]}' > "$stub/interfaces.json"
add clusters.json --arg at "$young" '. += [{name: "cpo-ci-2-1-1", status: "ACTIVE", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-2-1-1"}}]'
"$here/eks-sweep.sh" --older-than 2 > /dev/null
! called "delete-network-interface" || fail "$name" "deleted an interface while a cluster is up"

name="interfaces when no cluster is left"
empty
echo '{"NetworkInterfaces": [{"NetworkInterfaceId": "eni-1", "Status": "available"}]}' > "$stub/interfaces.json"
"$here/eks-sweep.sh" --older-than 2 > /dev/null
called "ec2 delete-network-interface --network-interface-id eni-1" || fail "$name" "did not delete the interface"

name="the security group of a cluster that is gone"
empty
echo '{"SecurityGroups": [{"GroupId": "sg-1", "GroupName": "eks-cluster-sg-cpo-ci-1-1-1-123456"},
  {"GroupId": "sg-2", "GroupName": "eks-cluster-sg-cpo-ci-2-1-1-654321"}]}' > "$stub/groups.json"
add clusters.json --arg at "$young" '. += [{name: "cpo-ci-2-1-1", status: "ACTIVE", createdAt: $at, tags: {"cpo-ci-run": "cpo-ci-2-1-1"}}]'
"$here/eks-sweep.sh" --older-than 2 > /dev/null
called "ec2 delete-security-group --group-id sg-1" || fail "$name" "did not delete the group of the gone cluster"
! called "sg-2" || fail "$name" "deleted the group of a cluster that is up"

name="a volume of the driver without the run tag"
empty
add volumes.json '.Volumes += [{VolumeId: "vol-2", State: "available", CreateTime: "2026-11-24T00:00:00Z",
  Tags: [{Key: "ebs.csi.aws.com/cluster", Value: "true"}]}]'
"$here/eks-sweep.sh" --run cpo-ci-1-1-1 > /dev/null
! called "ec2 delete-volume --volume-id vol-2" || fail "$name" "deleted a volume without the run tag"

name="an old volume whose run tag names no run"
empty
add volumes.json --arg at "$old" '.Volumes += [{VolumeId: "vol-3", State: "available", CreateTime: $at,
  Tags: [{Key: "cpo-ci-run", Value: "prod"}]}]'
"$here/eks-sweep.sh" --older-than 2 > /dev/null
! called "ec2 delete-volume --volume-id vol-3" || fail "$name" "deleted a volume of no run"

name="an old volume and cluster with the run tag in upper case"
empty
add volumes.json --arg at "$old" '.Volumes += [{VolumeId: "vol-4", State: "available", CreateTime: $at,
  Tags: [{Key: "CPO-CI-RUN", Value: "cpo-ci-1-1-1"}]}]'
add clusters.json --arg at "$old" '. += [{name: "cpo-ci-1-1-1", status: "ACTIVE", createdAt: $at, tags: {"CPO-CI-RUN": "cpo-ci-1-1-1"}}]'
"$here/eks-sweep.sh" --older-than 2 > /dev/null
called "ec2 delete-volume --volume-id vol-4" || fail "$name" "did not delete the volume"
called "eks delete-cluster --name cpo-ci-1-1-1" || fail "$name" "did not delete the cluster"
! grep -q 'tag-key' "$stub/calls" || fail "$name" "listed by the case-sensitive tag-key filter: $(grep tag-key "$stub/calls")"

name="a listing that fails"
empty
a_run cpo-ci-1-1-1 i-1 vol-1 "$old" ACTIVE
FAIL_CALL="ec2 describe-volumes" "$here/eks-sweep.sh" --older-than 2 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
called "eks delete-cluster --name cpo-ci-1-1-1" || fail "$name" "did not delete the cluster after the failed listing"

name="a run ID that does not match"
empty
"$here/eks-sweep.sh" --run BAD > /dev/null 2>&1
code=$?
[ "$code" -eq 2 ] || fail "$name" "exit $code, want 2"
[ ! -s "$stub/calls" ] || fail "$name" "called aws: $(cat "$stub/calls")"

exit "$failed"
