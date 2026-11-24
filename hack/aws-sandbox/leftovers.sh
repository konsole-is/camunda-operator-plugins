#!/usr/bin/env bash
# shellcheck disable=SC2016 # $run and $vpc are variables of jq, not of the shell
# Prints each role, managed policy, OIDC provider, and bucket that one CI
# run left in the sandbox account, one line "left: <kind> <name>" each. It
# reads names, not tags, so it finds also an object that lost its tag.
#
# Usage: leftovers.sh [--eks] [--all-regions] <run-id>
#
# --eks also prints what an EKS run left in AWS_REGION: its EKS cluster, EC2
# instance, EBS volumes, images, schedules, log groups, KMS keys that are not
# pending deletion, and KMS aliases. It prints each network interface, load
# balancer, and cluster security group of the run in the VPC AWS_EKS_VPC_ID,
# and each NAT gateway and Elastic IP of the region, because no run creates
# one. An EBS volume of the EBS CSI driver without a run tag counts for each
# run. --all-regions also prints the EKS clusters, EC2 instances, EBS
# volumes, and KMS keys of the run in each other region, as "<kind> in
# <region>". It needs administrator credentials.
#
# Needs the credentials of the CI role, or of the EKS role with --eks. It
# exits 1 when it printed a line or a listing failed, and 2 on wrong
# arguments.
set -euo pipefail

eks=false
all_regions=false
while [ $# -gt 0 ]; do
  case $1 in
    --eks) eks=true ;;
    --all-regions) all_regions=true ;;
    *) break ;;
  esac
  shift
done
run=${1:-}
if ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "usage: leftovers.sh [--eks] [--all-regions] <run-id>, where the run ID matches cpo-ci-[a-z0-9-]{1,40}: ${run}" >&2
  exit 2
fi
if ${eks} && { [ -z "${AWS_REGION:-}" ] || [ -z "${AWS_EKS_VPC_ID:-}" ]; }; then
  echo "--eks needs AWS_REGION and AWS_EKS_VPC_ID" >&2
  exit 2
fi
if ${all_regions} && ! ${eks}; then
  echo "--all-regions needs --eks" >&2
  exit 2
fi

failed=0

# left <kind> <jq filter> <aws arguments...> prints the names of the listing
# that belong to the run.
left() {
  local kind=$1 filter=$2 names
  shift 2
  if ! names=$(aws "$@" --output json | jq -r --arg run "${run}" --arg vpc "${AWS_EKS_VPC_ID:-}" "${filter}"); then
    echo "failed to list: ${kind}" >&2
    failed=1
    return
  fi
  while read -r name; do
    [ -n "${name}" ] || continue
    echo "left: ${kind} ${name}"
    failed=1
  done <<< "${names}"
}

# The jq filters of the EKS kinds that --all-regions reads in each region.
# The value of the run tag is read in any case of its key.
run_tag='([(.Tags // [])[] | select((.Key | ascii_downcase) == "cpo-ci-run") | .Value][0])'
instances_filter=".Reservations[].Instances[] | select(.State.Name != \"terminated\" and ${run_tag} == \$run) | .InstanceId"
# A volume of the driver without the run tag can belong to any run.
volumes_filter=".Volumes[] | select(${run_tag} as \$tag | \$tag == \$run or
  any((.Tags // [])[]; .Key == \"kubernetes.io/cluster/\" + \$run) or
  (\$tag == null and any((.Tags // [])[]; .Key == \"ebs.csi.aws.com/cluster\"))) | .VolumeId"

# clusters_left <region> <kind> prints each EKS cluster of the run in region:
# the cluster with the name of the run, and each cluster with its run tag.
clusters_left() {
  local region=$1 kind=$2 names name tag err
  if ! names=$(aws eks list-clusters --region "${region}" --output json | jq -r '.clusters[]'); then
    echo "failed to list: ${kind}" >&2
    failed=1
    return
  fi
  err=$(mktemp)
  for name in ${names}; do
    if [ "${name}" != "${run}" ]; then
      if ! tag=$(aws eks describe-cluster --region "${region}" --name "${name}" --output json 2> "${err}" |
        jq -r '.cluster.tags["cpo-ci-run"] // ""'); then
        # A cluster that is gone since the listing is no leftover.
        grep -q ResourceNotFoundException "${err}" || { echo "failed to read: ${kind} ${name}" >&2; failed=1; }
        continue
      fi
      [ "${tag}" = "${run}" ] || continue
    fi
    echo "left: ${kind} ${name}"
    failed=1
  done
  rm -f "${err}"
}

# keys_left <region> <kind> prints each KMS key of the run in region that is
# not pending deletion. A key whose tags the role cannot read belongs to no
# run.
keys_left() {
  local region=$1 kind=$2 keys key tags state
  if ! keys=$(aws kms list-keys --region "${region}" --output json | jq -r '.Keys[].KeyId'); then
    echo "failed to list: ${kind}" >&2
    failed=1
    return
  fi
  for key in ${keys}; do
    tags=$(aws kms list-resource-tags --region "${region}" --key-id "${key}" --output json 2> /dev/null) || continue
    [ "$(jq -r '[.Tags[]? | select((.TagKey | ascii_downcase) == "cpo-ci-run") | .TagValue][0] // ""' <<< "${tags}")" = "${run}" ] ||
      continue
    if ! state=$(aws kms describe-key --region "${region}" --key-id "${key}" --output json | jq -r .KeyMetadata.KeyState); then
      echo "failed to read: ${kind} ${key}" >&2
      failed=1
      continue
    fi
    case ${state} in PendingDeletion | PendingReplicaDeletion) continue ;; esac
    echo "left: ${kind} ${key}"
    failed=1
  done
}

# The dash after the run ID keeps cpo-ci-1-1-10 out of the run cpo-ci-1-1-1.
left role '.Roles[].RoleName | select(startswith($run + "-"))' iam list-roles
left policy '.Policies[].PolicyName | select(startswith($run + "-"))' iam list-policies --scope Local
left "OIDC provider" '.OpenIDConnectProviderList[].Arn | select(endswith("/" + $run))' \
  iam list-open-id-connect-providers
left bucket '.Buckets[].Name | select(startswith($run + "-"))' s3api list-buckets

if ${eks}; then
  clusters_left "${AWS_REGION}" "EKS cluster"
  left "EC2 instance" "${instances_filter}" ec2 describe-instances --filters "Name=tag-key,Values=cpo-ci-run"
  left "EBS volume" "${volumes_filter}" ec2 describe-volumes \
    --filters "Name=tag-key,Values=cpo-ci-run,ebs.csi.aws.com/cluster,kubernetes.io/cluster/${run}"
  left "network interface" '.NetworkInterfaces[].NetworkInterfaceId' \
    ec2 describe-network-interfaces --filters "Name=vpc-id,Values=${AWS_EKS_VPC_ID}"
  left "security group" '.SecurityGroups[] | select(.GroupName | startswith("eks-cluster-sg-" + $run + "-")) | .GroupId' \
    ec2 describe-security-groups --filters "Name=vpc-id,Values=${AWS_EKS_VPC_ID}"
  left "NAT gateway" '.NatGateways[].NatGatewayId' \
    ec2 describe-nat-gateways --filter "Name=state,Values=pending,available,deleting,failed"
  left "Elastic IP" '.Addresses[] | .AllocationId // .PublicIp' ec2 describe-addresses
  left "load balancer" '.LoadBalancerDescriptions[] | select(.VPCId == $vpc) | .LoadBalancerName' \
    elb describe-load-balancers
  left "load balancer" '.LoadBalancers[] | select(.VpcId == $vpc) | .LoadBalancerName' elbv2 describe-load-balancers
  left "log group" '.logGroups[].logGroupName' logs describe-log-groups --log-group-name-prefix "/aws/eks/${run}/"
  # terraform/aws-sandbox/eks-registry.tf creates these two repositories.
  for repo in cpo-sandbox-eks/camunda-operator cpo-sandbox-eks/aws; do
    left image ".imageDetails[].imageTags[]? | select(. == \$run) | \"${repo}:\" + ." \
      ecr describe-images --repository-name "${repo}"
  done
  left schedule '.Schedules[].Name | select(. == $run + "-delete-cluster" or . == $run + "-terminate-nodes")' \
    scheduler list-schedules --group-name cpo-sandbox-eks --name-prefix "${run}-"
  keys_left "${AWS_REGION}" "KMS key"
  left "KMS alias" '.Aliases[].AliasName | select(startswith("alias/" + $run + "/"))' kms list-aliases
fi

if ${all_regions}; then
  if ! regions=$(aws ec2 describe-regions --output json | jq -r '.Regions[].RegionName'); then
    echo "failed to list: regions" >&2
    failed=1
  fi
  for region in ${regions:-}; do
    [ "${region}" != "${AWS_REGION}" ] || continue
    clusters_left "${region}" "EKS cluster in ${region}"
    left "EC2 instance in ${region}" "${instances_filter}" \
      ec2 describe-instances --region "${region}" --filters "Name=tag-key,Values=cpo-ci-run"
    left "EBS volume in ${region}" "${volumes_filter}" ec2 describe-volumes --region "${region}" \
      --filters "Name=tag-key,Values=cpo-ci-run,ebs.csi.aws.com/cluster,kubernetes.io/cluster/${run}"
    keys_left "${region}" "KMS key in ${region}"
  done
fi

exit "${failed}"
