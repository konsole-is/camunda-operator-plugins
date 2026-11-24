#!/usr/bin/env bash
# shellcheck disable=SC2016 # $at is a variable of jq, not of the shell
# Removes what EKS runs left in the sandbox account: the EC2 instances and
# EKS clusters with the tag cpo-ci-run, their EBS volumes and images, the
# schedules and log groups of their runs, and the network interfaces and
# cluster security groups that a deleted cluster left in the VPC.
#
#   eks-sweep.sh --run <run-id>          what that run created, at any age
#   eks-sweep.sh --older-than <hours>    what any run created that many hours ago
#
# It deletes a cluster only when its run tag starts with cpo-ci- and the
# cluster has a cpo-ci- name or runs in the VPC AWS_EKS_VPC_ID. leftovers.sh
# still reports another cluster with the run tag. sweep.sh removes the IAM
# roles and the KMS keys of a run.
#
# Needs AWS_REGION, AWS_EKS_VPC_ID, and the credentials of the role
# cpo-sandbox-eks or of an administrator. It tries every kind, and exits 1
# when a listing or a removal failed, and 2 on wrong arguments.
set -euo pipefail

run=""
hours=""
case ${1:-} in
  --run) run=${2:-} ;;
  --older-than) hours=${2:-} ;;
esac
if { [ -z "${run}" ] && ! [[ ${hours} =~ ^[0-9]+$ ]]; } ||
  { [ -n "${run}" ] && ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; }; then
  echo "usage: $0 --run <run-id> | --older-than <hours>, where the run ID matches cpo-ci-[a-z0-9-]{1,40}" >&2
  exit 2
fi
: "${AWS_REGION:?AWS_REGION is not set}"
: "${AWS_EKS_VPC_ID:?AWS_EKS_VPC_ID is not set}"
vpc=${AWS_EKS_VPC_ID}
# terraform/aws-sandbox creates the repositories and the schedule group.
repositories=(cpo-sandbox-eks/camunda-operator cpo-sandbox-eks/aws)
group=cpo-sandbox-eks
# EBS detaches the volumes of a terminated node, and EKS removes the network
# interfaces of a deleted cluster, within this time.
settle_seconds=300

cutoff=0
if [ -n "${hours}" ]; then
  cutoff=$(($(date +%s) - hours * 3600))
fi
failed=0

# matches <tag value> <creation date> tells if the sweep removes a resource.
matches() {
  local tag=$1 created=$2
  [ -n "${tag}" ] && [ "${tag}" != None ] || return 1
  if [ -n "${run}" ]; then
    [ "${tag}" = "${run}" ]
  else
    older "${created}"
  fi
}

older() {
  [ -z "${run}" ] && [ "$(date -d "$1" +%s)" -lt "${cutoff}" ]
}

# step runs a command and keeps the sweep going when it fails.
step() {
  if ! "$@" > /dev/null; then
    echo "failed: $*" >&2
    failed=1
  fi
}

# listing <variable> <kind> <aws arguments...> sets the variable to the JSON
# of the call, or to nothing when the call fails.
listing() {
  local into=$1 kind=$2 out
  shift 2
  if out=$(aws "$@" --output json); then
    printf -v "${into}" '%s' "${out}"
  else
    echo "failed to list: ${kind}" >&2
    failed=1
    printf -v "${into}" '%s' ''
  fi
}

# The value of the run tag, in any case of its key, or None.
run_tag='([(.Tags // [])[] | select((.Key | ascii_downcase) == "cpo-ci-run") | .Value][0] // "None")'

# live_runs prints the run tag of each instance of the VPC that is not
# terminated, and fails when the listing fails.
live_runs() {
  aws ec2 describe-instances --output json --filters "Name=vpc-id,Values=${vpc}" "Name=tag-key,Values=cpo-ci-run" \
    "Name=instance-state-name,Values=pending,running,shutting-down,stopping,stopped" |
    jq -r ".Reservations[].Instances[] | select(.State.Name != \"terminated\") | ${run_tag}"
}

# cluster_names prints the clusters of the region, and fails when the
# listing fails.
cluster_names() {
  aws eks list-clusters --output json | jq -r '.clusters[]'
}

# 1. The nodes first, so that no volume stays in use and no pod writes again.
listing instances "EC2 instances" ec2 describe-instances --filters "Name=vpc-id,Values=${vpc}" \
  "Name=tag-key,Values=cpo-ci-run" "Name=instance-state-name,Values=pending,running,shutting-down,stopping,stopped"
ids=()
while IFS='|' read -r id tag launched; do
  [ -n "${id}" ] || continue
  if matches "${tag}" "${launched}"; then
    echo "EC2 instance ${id} (${tag})"
    ids+=("${id}")
  fi
done < <(jq -r "(.Reservations // [])[].Instances[] | select(.State.Name != \"terminated\") |
  [.InstanceId, ${run_tag}, .LaunchTime] | join(\"|\")" <<< "${instances:-null}")
if [ ${#ids[@]} -gt 0 ]; then
  step aws ec2 terminate-instances --instance-ids "${ids[@]}"
  step aws ec2 wait instance-terminated --instance-ids "${ids[@]}"
fi

# 2. The clusters. A cluster in CREATING cannot be deleted yet, and a
# cluster in DELETING is only waited for.
listing clusters "EKS clusters" eks list-clusters
deleted=()
for name in $(jq -r '(.clusters // [])[]' <<< "${clusters:-null}"); do
  err=$(mktemp)
  if ! cluster=$(aws eks describe-cluster --name "${name}" --output json 2> "${err}"); then
    # The sweep of another run can delete it at the same time.
    grep -q ResourceNotFoundException "${err}" || { cat "${err}" >&2; failed=1; }
    rm -f "${err}"
    continue
  fi
  rm -f "${err}"
  IFS='|' read -r status created tag cluster_vpc <<< "$(jq -r '.cluster |
    [.status, .createdAt, (.tags["cpo-ci-run"] // "None"), (.resourcesVpcConfig.vpcId // "")] | join("|")' <<< "${cluster}")"
  if [[ ${tag} != cpo-ci-* ]] || { [[ ${name} != cpo-ci-* ]] && [ "${cluster_vpc}" != "${vpc}" ]; }; then
    continue
  fi
  matches "${tag}" "${created}" || continue
  echo "EKS cluster ${name} (${tag}, ${status})"
  case ${status} in
    DELETING) ;;
    CREATING)
      aws eks wait cluster-active --name "${name}" || true
      step aws eks delete-cluster --name "${name}"
      ;;
    *) step aws eks delete-cluster --name "${name}" ;;
  esac
  deleted+=("${name}")
done
for name in "${deleted[@]}"; do
  step aws eks wait cluster-deleted --name "${name}"
done

# 3. The volumes, by the run tag: the role of the EBS CSI driver creates no
# volume without it.
listing volumes "EBS volumes" ec2 describe-volumes --filters "Name=tag-key,Values=cpo-ci-run"
deadline=$(($(date +%s) + settle_seconds))
while IFS='|' read -r id state created tag; do
  [ -n "${id}" ] || continue
  matches "${tag}" "${created}" || continue
  echo "EBS volume ${id} (${tag}, ${state})"
  while [ "${state}" = in-use ] || [ "${state}" = creating ]; do
    [ "$(date +%s)" -lt "${deadline}" ] || break
    sleep 15
    state=$(aws ec2 describe-volumes --volume-ids "${id}" --query 'Volumes[0].State' --output text) || break
  done
  [ "${state}" != deleting ] || continue
  step aws ec2 delete-volume --volume-id "${id}"
done < <(jq -r "(.Volumes // [])[] | [.VolumeId, .State, .CreateTime, ${run_tag}] | join(\"|\")" <<< "${volumes:-null}")

# The clusters and the nodes that are left decide what of the VPC goes.
remaining=""
if ! remaining=$(cluster_names); then
  echo "failed to list: EKS clusters" >&2
  failed=1
  remaining=unknown
fi

# 4. The network interfaces. The VPC holds only runs, so an available
# interface is a leftover once no cluster runs. EKS removes the interfaces of
# a control plane some minutes after the delete of its cluster.
if [ -z "${remaining}" ]; then
  deadline=$(($(date +%s) + settle_seconds))
  while true; do
    listing interfaces "network interfaces" ec2 describe-network-interfaces --filters "Name=vpc-id,Values=${vpc}"
    [ ${#deleted[@]} -gt 0 ] && [ "$(date +%s)" -lt "${deadline}" ] || break
    [ "$(jq '[(.NetworkInterfaces // [])[] | select(.Status != "available")] | length' <<< "${interfaces:-null}")" -gt 0 ] ||
      break
    sleep 15
  done
  for id in $(jq -r '(.NetworkInterfaces // [])[] | select(.Status == "available") | .NetworkInterfaceId' <<< "${interfaces:-null}"); do
    echo "network interface ${id}"
    step aws ec2 delete-network-interface --network-interface-id "${id}"
  done
fi

# 5. The cluster security group of a deleted cluster:
# eks-cluster-sg-<cluster>-<number>.
listing groups "security groups" ec2 describe-security-groups \
  --filters "Name=vpc-id,Values=${vpc}" "Name=group-name,Values=eks-cluster-sg-cpo-ci-*"
while IFS='|' read -r id group_name; do
  [ -n "${id}" ] || continue
  cluster=${group_name#eks-cluster-sg-}
  cluster=${cluster%-*}
  [ "${remaining}" != unknown ] || continue
  ! grep -qxF "${cluster}" <<< "${remaining}" || continue
  [ -z "${run}" ] || [ "${cluster}" = "${run}" ] || continue
  echo "security group ${id} (${group_name})"
  step aws ec2 delete-security-group --group-id "${id}"
done < <(jq -r '(.SecurityGroups // [])[] | [.GroupId, .GroupName] | join("|")' <<< "${groups:-null}")

# 6. The images of the runs.
for repo in "${repositories[@]}"; do
  listing images "images of ${repo}" ecr describe-images --repository-name "${repo}"
  image_ids=()
  while IFS='|' read -r tag pushed; do
    [ -n "${tag}" ] || continue
    matches "${tag}" "${pushed}" || continue
    echo "image ${repo}:${tag}"
    image_ids+=("imageTag=${tag}")
  done < <(jq -r '(.imageDetails // [])[] | .imagePushedAt as $at | .imageTags[]? | select(startswith("cpo-ci-")) |
    [., ($at | tostring)] | join("|")' <<< "${images:-null}")
  [ ${#image_ids[@]} -gt 0 ] || continue
  if ! result=$(aws ecr batch-delete-image --repository-name "${repo}" --image-ids "${image_ids[@]}" --output json); then
    echo "failed: delete the images of ${repo}" >&2
    failed=1
  elif [ "$(jq '.failures // [] | length' <<< "${result}")" -gt 0 ]; then
    jq -r '.failures[] | "\(.imageId.imageTag): \(.failureCode) \(.failureReason)"' <<< "${result}" >&2
    failed=1
  fi
done

# 7. The control plane log groups, /aws/eks/<cluster>/cluster. A run turns no
# logs on, so a group here comes from a change by hand.
listing logs "log groups" logs describe-log-groups --log-group-name-prefix /aws/eks/cpo-ci-
while IFS='|' read -r log_group created_ms; do
  [ -n "${log_group}" ] || continue
  tag=$(cut -d/ -f4 <<< "${log_group}")
  matches "${tag}" "@$((created_ms / 1000))" || continue
  echo "log group ${log_group}"
  step aws logs delete-log-group --log-group-name "${log_group}"
done < <(jq -r '(.logGroups // [])[] | [.logGroupName, (.creationTime | tostring)] | join("|")' <<< "${logs:-null}")

# 8. The schedules last: they delete the cluster and the node of a run whose
# cleanup did not finish, so they stay while either is there.
if ! live=$(live_runs); then
  echo "failed to list: EC2 instances" >&2
  failed=1
  live=unknown
fi
listing schedules schedules scheduler list-schedules --group-name "${group}" --name-prefix cpo-ci-
while IFS='|' read -r schedule created; do
  [ -n "${schedule}" ] || continue
  owner=${schedule%-delete-cluster}
  owner=${owner%-terminate-nodes}
  [ "${remaining}" != unknown ] && [ "${live}" != unknown ] || continue
  ! grep -qxF "${owner}" <<< "${remaining}" || continue
  ! grep -qxF "${owner}" <<< "${live}" || continue
  matches "${owner}" "${created}" || continue
  echo "schedule ${schedule}"
  step aws scheduler delete-schedule --group-name "${group}" --name "${schedule}"
done < <(jq -r '(.Schedules // [])[] | [.Name, (.CreationDate | tostring)] | join("|")' <<< "${schedules:-null}")

exit "${failed}"
