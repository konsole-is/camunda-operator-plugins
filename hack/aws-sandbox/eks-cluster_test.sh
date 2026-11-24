#!/usr/bin/env bash
# Tests eks-cluster.sh against a stub aws and kubectl, which write each call
# to $STUB/calls. make test-scripts runs it.
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
stub=$(mktemp -d)
trap 'rm -rf "$stub"' EXIT

cat > "$stub/aws" << 'EOF'
#!/usr/bin/env bash
echo "aws $*" >> "$STUB/calls"
for a in "$@"; do
  [[ $a != file://* ]] || cp "${a#file://}" "$STUB/user-data"
done
arg() {
  local name=$1
  shift
  while [ $# -gt 1 ]; do
    [ "$1" != "$name" ] || { echo "$2"; return; }
    shift
  done
}
case "$1 $2" in
  "eks describe-cluster-versions") echo '{"clusterVersions": [{"clusterVersion": "1.34"}]}' ;;
  "eks describe-addon-versions")
    echo '{"addons": [{"addonVersions": [{"addonVersion": "v1.0.0-old", "compatibilities": [{"defaultVersion": false}]},
      {"addonVersion": "v1.1.0", "compatibilities": [{"defaultVersion": true}]}]}]}'
    ;;
  "eks describe-addon-configuration") jq -n --rawfile s "$STUB/schema.json" '{configurationSchema: $s}' ;;
  "eks describe-cluster")
    echo '{"cluster": {"endpoint": "https://abc.eks.example", "version": "1.34",
      "certificateAuthority": {"data": "Q0E="}, "kubernetesNetworkConfig": {"serviceIpv4Cidr": "10.100.0.0/16"},
      "resourcesVpcConfig": {"clusterSecurityGroupId": "sg-1"}}}'
    ;;
  "ec2 describe-instances") cat "$STUB/instances.json" ;;
  "ssm get-parameter") echo '{"Parameter": {"Value": "ami-1"}}' ;;
  "ec2 run-instances") echo '{"Instances": [{"InstanceId": "i-1"}]}' ;;
  # EventBridge Scheduler refuses a universal target whose input lacks a
  # member of the request of the API action, with the name of that member.
  "scheduler create-schedule")
    target=$(arg --target "$@")
    case $(jq -r .Arn <<< "$target") in
      *:eks:deleteCluster) member=Name ;;
      *:ec2:terminateInstances) member=InstanceIds ;;
      *) member="" ;;
    esac
    if [ -n "$member" ] && ! jq -e --arg m "$member" '.Input | fromjson | has($m)' <<< "$target" > /dev/null; then
      echo "An error occurred (ValidationException) when calling the CreateSchedule operation: Invalid RequestJson provided. Reason Request payload is missing the following field(s): $member." >&2
      exit 254
    fi
    echo '{}'
    ;;
  "eks create-cluster" | "eks create-access-entry" | \
    "eks create-pod-identity-association" | "eks create-addon") echo '{}' ;;
  # The cluster takes ELAPSED seconds of the clock of the stub date.
  # FAIL_WAIT names an add-on, or "cluster", whose wait fails.
  "eks wait")
    [ "$3" != cluster-active ] || echo "${ELAPSED:-0}" > "$STUB/offset"
    if [ "${FAIL_WAIT:-}" = cluster ] && [ "$3" = cluster-active ] ||
      { [ -n "${FAIL_WAIT:-}" ] && [ "$(arg --addon-name "$@")" = "${FAIL_WAIT:-}" ]; }; then
      echo "Waiter failed: terminal failure state" >&2
      exit 255
    fi
    ;;
  "eks describe-addon") echo '{"status": "CREATE_FAILED", "health": {"issues": [{"code": "ConfigurationConflict", "message": "Conflicts found"}]}}' ;;
  "eks update-kubeconfig") ;;
  *) echo "unexpected call: aws $*" >&2; exit 3 ;;
esac
EOF
cat > "$stub/kubectl" << 'EOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$STUB/calls"
case $1 in
  get) echo node/ip-10-42-0-1 ;;
  wait) [ -z "${FAIL_NODE:-}" ] || { echo "error: timed out waiting for the condition" >&2; exit 1; } ;;
  describe) echo "Ready False KubeletNotReady container runtime network not ready" ;;
esac
EOF
# date +%s reads a clock that the stub aws can move forward.
real_date=$(command -v date)
cat > "$stub/date" << EOF
#!/usr/bin/env bash
if [ "\$*" = +%s ]; then
  echo \$((\$("$real_date" +%s) + \$(cat "$stub/offset" 2> /dev/null || echo 0)))
else
  exec "$real_date" "\$@"
fi
EOF
chmod +x "$stub/aws" "$stub/kubectl" "$stub/date"
export PATH="$stub:$PATH" STUB=$stub AWS_REGION=eu-west-1 AWS_EKS_VPC_ID=vpc-1 \
  AWS_EKS_SUBNET_IDS=subnet-1,subnet-2 AWS_EKS_CLUSTER_ROLE_ARN=arn:aws:iam::1:role/cluster \
  AWS_EKS_NODE_ROLE_ARN=arn:aws:iam::1:role/node AWS_EKS_NODE_PROFILE=node \
  AWS_EKS_CSI_ROLE_ARN=arn:aws:iam::1:role/csi AWS_EKS_SCHEDULER_ROLE_ARN=arn:aws:iam::1:role/scheduler \
  AWS_EKS_INSTANCE_TYPE=t3.xlarge KUBECONFIG=$stub/kubeconfig

failed=0
fail() {
  echo "FAIL $1: $2" >&2
  failed=1
}

schema='{"properties": {"controller": {"properties": {"extraVolumeTags": {"type": "object"}}}}}'
reset() {
  rm -f "$stub/calls" "$stub/user-data" "$stub/offset"
  echo "$schema" > "$stub/schema.json"
  echo '{"Reservations": []}' > "$stub/instances.json"
}
line_of() { grep -n -m1 -F -e "$1" "$stub/calls" | cut -d: -f1; }
call() { grep -m1 -F -e "$1" "$stub/calls"; }

name="a run ID that does not match"
reset
"$here/eks-cluster.sh" BAD > /dev/null 2>&1
code=$?
[ "$code" -eq 2 ] || fail "$name" "exit $code, want 2"
[ ! -s "$stub/calls" ] || fail "$name" "called: $(cat "$stub/calls")"

reset
out=$("$here/eks-cluster.sh" cpo-ci-1-1-1 2> "$stub/stderr")
code=$?
[ "$code" -eq 0 ] || fail "a cluster" "exit $code, want 0: $(cat "$stub/stderr")"
grep -qx 'cluster=cpo-ci-1-1-1' <<< "$out" || fail "a cluster" "printed '$out', want cluster=cpo-ci-1-1-1"

name="the delete schedule comes first"
schedule=$(call "aws scheduler create-schedule")
for want in "--name cpo-ci-1-1-1-delete-cluster" "--group-name cpo-sandbox-eks" "at(" \
  "--action-after-completion DELETE" "aws-sdk:eks:deleteCluster" '\"Name\":\"cpo-ci-1-1-1\"'; do
  [[ $schedule == *"$want"* ]] || fail "$name" "the first schedule '$schedule' lacks '$want'"
done
first_create=$(grep -n -m1 -E 'aws [a-z0-9]+ (create-|run-instances)' "$stub/calls" | cut -d: -f1)
[ "$first_create" = "$(line_of 'aws scheduler create-schedule')" ] ||
  fail "$name" "the first create is not the delete schedule: $(sed -n "${first_create}p" "$stub/calls")"

name="every create carries the run tag"
grep -E 'aws eks (create-cluster|create-addon|create-access-entry|create-pod-identity-association)' "$stub/calls" > "$stub/creates"
[ "$(wc -l < "$stub/creates")" -eq 5 ] || fail "$name" "made $(wc -l < "$stub/creates") EKS creates, want 5"
while read -r line; do
  [[ $line == *"--tags cpo-ci-run=cpo-ci-1-1-1"* ]] || fail "$name" "no run tag in '$line'"
done < "$stub/creates"
run=$(call "aws ec2 run-instances")
for kind in instance volume network-interface; do
  [[ $run == *"{\"ResourceType\":\"$kind\",\"Tags\":[{\"Key\":\"cpo-ci-run\",\"Value\":\"cpo-ci-1-1-1\"}"* ]] ||
    fail "$name" "no run tag for the $kind in '$run'"
done

name="the cluster"
cluster=$(call "aws eks create-cluster")
for want in "--name cpo-ci-1-1-1" "--kubernetes-version 1.34" "supportType=STANDARD" "authenticationMode=API" \
  "bootstrapClusterCreatorAdminPermissions=true" "subnetIds=subnet-1,subnet-2"; do
  [[ $cluster == *"$want"* ]] || fail "$name" "'$cluster' lacks '$want'"
done
[[ $cluster != *"--logging"* ]] || fail "$name" "'$cluster' turns logs on"

name="the node"
for want in "--count 1" "--instance-type t3.xlarge" "--image-id ami-1" "--subnet-id subnet-1" "--security-group-ids sg-1" \
  "HttpTokens=required" "HttpPutResponseHopLimit=1" "CpuCredits=standard" \
  "--instance-initiated-shutdown-behavior terminate" "DeleteOnTermination=true" "VolumeType=gp3"; do
  [[ $run == *"$want"* ]] || fail "$name" "'$run' lacks '$want'"
done
for want in "shutdown -h +" "vm.max_map_count=262144" "kind: NodeConfig" "name: cpo-ci-1-1-1" \
  "apiServerEndpoint: https://abc.eks.example" "certificateAuthority: Q0E=" "cidr: 10.100.0.0/16"; do
  grep -qF -e "$want" "$stub/user-data" 2> /dev/null || fail "$name" "the user data lacks '$want'"
done
nodes=$(grep -F 'aws scheduler create-schedule' "$stub/calls" | sed -n 2p)
for want in "--name cpo-ci-1-1-1-terminate-nodes" "aws-sdk:ec2:terminateInstances" '\"InstanceIds\":[\"i-1\"]'; do
  [[ $nodes == *"$want"* ]] || fail "$name" "the second schedule '$nodes' lacks '$want'"
done
[ "$(line_of 'aws ec2 run-instances')" -lt "$(line_of 'cpo-ci-1-1-1-terminate-nodes')" ] ||
  fail "$name" "the node schedule comes before the node"
ami=$(call "aws ssm get-parameter")
[[ $ami == *"/aws/service/eks/optimized-ami/1.34/amazon-linux-2023/x86_64/standard/recommended/image_id"* ]] ||
  fail "$name" "the image parameter is '$ami'"

name="the CSI driver"
csi=$(call "create-addon --cluster-name cpo-ci-1-1-1 --addon-name aws-ebs-csi-driver")
for want in "--addon-version v1.1.0" '--configuration-values {"controller":{"extraVolumeTags":{"cpo-ci-run":"cpo-ci-1-1-1"}}}'; do
  [[ $csi == *"$want"* ]] || fail "$name" "'$csi' lacks '$want'"
done
association=$(line_of "aws eks create-pod-identity-association")
[ -n "$association" ] && [ "$association" -lt "$(line_of 'create-addon --cluster-name cpo-ci-1-1-1 --addon-name aws-ebs-csi-driver')" ] ||
  fail "$name" "no Pod Identity association before the add-on"
[[ $(call "aws eks create-pod-identity-association") == *"--namespace kube-system --service-account ebs-csi-controller-sa"* ]] ||
  fail "$name" "the association is not for the controller"

name="the kubeconfig and the node"
[[ $(call "aws eks update-kubeconfig") == *"--name cpo-ci-1-1-1 --alias cpo-ci-1-1-1"* ]] || fail "$name" "no kubeconfig context"
call "kubectl wait --for=condition=Ready nodes --all" > /dev/null || fail "$name" "did not wait for the node"

name="an add-on version without the keys"
reset
echo '{"properties": {"controller": {"properties": {"env": {}}}}}' > "$stub/schema.json"
"$here/eks-cluster.sh" cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
! grep -qE 'create-|run-instances' "$stub/calls" || fail "$name" "created: $(grep -E 'create-|run-instances' "$stub/calls")"

name="two nodes already run"
reset
echo '{"Reservations": [{"Instances": [{"InstanceId": "i-8"}, {"InstanceId": "i-9"}]}]}' > "$stub/instances.json"
"$here/eks-cluster.sh" cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
! grep -qE 'create-|run-instances' "$stub/calls" || fail "$name" "created: $(grep -E 'create-|run-instances' "$stub/calls")"

name="an input that is not set"
reset
AWS_EKS_SCHEDULER_ROLE_ARN='' "$here/eks-cluster.sh" cpo-ci-1-1-1 > /dev/null 2>&1
code=$?
[ "$code" -eq 2 ] || fail "$name" "exit $code, want 2"
[ ! -s "$stub/calls" ] || fail "$name" "called: $(cat "$stub/calls")"

for wait in cluster eks-pod-identity-agent aws-ebs-csi-driver; do
  name="a wait for $wait that fails"
  reset
  FAIL_WAIT=$wait "$here/eks-cluster.sh" cpo-ci-1-1-1 > /dev/null 2> "$stub/stderr"
  code=$?
  [ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
  case $wait in
    cluster) want="aws eks describe-cluster --name cpo-ci-1-1-1" ;;
    *) want="aws eks describe-addon --cluster-name cpo-ci-1-1-1 --addon-name $wait" ;;
  esac
  grep -qF -- "$want" "$stub/calls" || fail "$name" "did not read the state with '$want'"
  [ "$wait" = cluster ] || grep -q ConfigurationConflict "$stub/stderr" ||
    fail "$name" "did not print the health of the add-on: $(cat "$stub/stderr")"
done

name="a node that does not become Ready"
reset
FAIL_NODE=1 "$here/eks-cluster.sh" cpo-ci-1-1-1 > /dev/null 2> "$stub/stderr"
code=$?
[ "$code" -eq 1 ] || fail "$name" "exit $code, want 1"
grep -q 'KubeletNotReady' "$stub/stderr" || fail "$name" "did not print the node conditions: $(cat "$stub/stderr")"

# at_of <schedule name> prints the time of the schedule in epoch seconds.
at_of() {
  local at
  at=$(grep -F -- "--name $1 " "$stub/calls" | grep -oE 'at\([0-9T:-]+\)' | tr -d 'at()')
  [ -z "$at" ] || date -u -d "${at}Z" +%s
}

for elapsed in 0 1500 6000; do
  name="the node ends before the cluster after $elapsed seconds of cluster creation"
  reset
  before=$(date +%s)
  ELAPSED=$elapsed "$here/eks-cluster.sh" cpo-ci-1-1-1 > /dev/null 2>&1 || fail "$name" "the script failed"
  node=$(at_of cpo-ci-1-1-1-terminate-nodes)
  cluster=$(at_of cpo-ci-1-1-1-delete-cluster)
  shutdown=$(grep -oE 'shutdown -h \+[0-9]+' "$stub/user-data" | grep -oE '[0-9]+$')
  if [ -z "$node" ] || [ -z "$cluster" ] || [ -z "$shutdown" ]; then
    fail "$name" "no schedule or no shutdown"
    continue
  fi
  [ "$node" -lt "$cluster" ] || fail "$name" "the node schedule $node is not before the cluster schedule $cluster"
  [ $((cluster - node)) -eq 600 ] || fail "$name" "the cluster schedule is $((cluster - node)) s after the node schedule, want 600"
  [ $((node - before)) -le $((150 * 60 + 5)) ] || fail "$name" "the node schedule is $((node - before)) s after the start, want 150 minutes"
  [ $((before + elapsed + shutdown * 60)) -lt "$node" ] ||
    fail "$name" "the node shuts down after $shutdown minutes, after its schedule"
done

exit "$failed"
