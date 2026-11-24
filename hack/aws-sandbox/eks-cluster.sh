#!/usr/bin/env bash
# Creates the EKS cluster of one run in the sandbox account, with one node,
# the EKS Pod Identity Agent, and the EBS CSI driver. Every object carries the
# tag cpo-ci-run=<run-id>. It writes the kubeconfig context <run-id> and
# prints cluster=<run-id> when the node is Ready.
#
# Usage: eks-cluster.sh <run-id>
#
# Before its first create, it arms a one-time schedule that deletes the
# cluster 160 minutes after the start of the script. After the node, a second
# one terminates the node 150 minutes after the start, and the node shuts
# itself down a few minutes before that. So the node and then the cluster go
# also when no cleanup of the run runs. Remove the run with eks-teardown.sh,
# also when this script fails half way.
#
# Inputs (the environment aws-eks sets each AWS_EKS_* variable):
#   AWS_REGION, AWS_EKS_VPC_ID, AWS_EKS_SUBNET_IDS (comma-separated),
#   AWS_EKS_CLUSTER_ROLE_ARN, AWS_EKS_NODE_ROLE_ARN, AWS_EKS_NODE_PROFILE,
#   AWS_EKS_CSI_ROLE_ARN, AWS_EKS_SCHEDULER_ROLE_ARN, AWS_EKS_INSTANCE_TYPE
#   (required), and the credentials of the role cpo-sandbox-eks
#   EKS_KUBERNETES_VERSION  the Kubernetes version (optional; empty is the
#                           default version of EKS)
#   KUBECONFIG              the kubeconfig file to write (optional)
# It exits 2 on a wrong run ID or a missing input, before any AWS call, and
# 1 when a check or a create fails.
set -euo pipefail

readonly nodes=1 max_nodes=2 lifetime_minutes=150
# The node goes first, so that the delete of the cluster finds no node that
# holds its security group.
readonly cluster_lifetime_minutes=$((lifetime_minutes + 10))
# The node runs its user data some minutes after run-instances, so it shuts
# itself down this much before the end of its schedule.
readonly boot_minutes=5
readonly group=cpo-sandbox-eks

run=${1:-}
if ! [[ ${run} =~ ^cpo-ci-[a-z0-9-]{1,40}$ ]]; then
  echo "usage: eks-cluster.sh <run-id>, where the run ID matches cpo-ci-[a-z0-9-]{1,40}: ${run}" >&2
  exit 2
fi
for input in AWS_REGION AWS_EKS_VPC_ID AWS_EKS_SUBNET_IDS AWS_EKS_CLUSTER_ROLE_ARN AWS_EKS_NODE_ROLE_ARN \
  AWS_EKS_NODE_PROFILE AWS_EKS_CSI_ROLE_ARN AWS_EKS_SCHEDULER_ROLE_ARN AWS_EKS_INSTANCE_TYPE; do
  if [ -z "${!input:-}" ]; then
    echo "${input} is not set" >&2
    exit 2
  fi
done
IFS=, read -r -a subnets <<< "${AWS_EKS_SUBNET_IDS}"
tags=(--tags "cpo-ci-run=${run}")
start=$(date +%s)
node_end=$((start + lifetime_minutes * 60))
cluster_end=$((start + cluster_lifetime_minutes * 60))
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT

# schedule <name> <epoch seconds> <API action> <input JSON> arms a one-time
# schedule that calls the action of the AWS SDK once at that time, and then
# deletes itself.
schedule() {
  local at
  at=$(date -u -d "@$2" +%Y-%m-%dT%H:%M:%S)
  aws scheduler create-schedule --name "$1" --group-name "${group}" \
    --schedule-expression "at(${at})" --schedule-expression-timezone UTC \
    --flexible-time-window Mode=OFF --action-after-completion DELETE \
    --target "$(jq -cn --arg role "${AWS_EKS_SCHEDULER_ROLE_ARN}" --arg arn "arn:aws:scheduler:::aws-sdk:$3" \
      --arg input "$4" '{RoleArn: $role, Arn: $arn, Input: $input}')" > /dev/null
}

# The checks, before anything is created.
version=${EKS_KUBERNETES_VERSION:-}
if [ -z "${version}" ]; then
  version=$(aws eks describe-cluster-versions --default-only --output json | jq -r '.clusterVersions[0].clusterVersion')
fi
csi_version=$(aws eks describe-addon-versions --addon-name aws-ebs-csi-driver --kubernetes-version "${version}" \
  --output json | jq -r '[.addons[0].addonVersions[] | select(any(.compatibilities[]; .defaultVersion))][0].addonVersion // ""')
[ -n "${csi_version}" ] || { echo "EKS lists no default aws-ebs-csi-driver for Kubernetes ${version}" >&2; exit 1; }
# Without extraVolumeTags, the policy of the driver role refuses each volume.
# The hop limit of the node keeps the controller from reading its region from
# the instance metadata, so it takes AWS_REGION.
if ! aws eks describe-addon-configuration --addon-name aws-ebs-csi-driver --addon-version "${csi_version}" --output json |
  jq -e '.configurationSchema | fromjson | .properties.controller.properties | has("extraVolumeTags") and has("env")' > /dev/null; then
  echo "aws-ebs-csi-driver ${csi_version} has no controller.extraVolumeTags or controller.env" >&2
  exit 1
fi
running=$(aws ec2 describe-instances --output json --filters "Name=vpc-id,Values=${AWS_EKS_VPC_ID}" \
  "Name=tag-key,Values=cpo-ci-run" "Name=instance-state-name,Values=pending,running" |
  jq '[.Reservations[].Instances[]] | length')
if [ $((running + nodes)) -gt "${max_nodes}" ]; then
  echo "${running} nodes of EKS runs already run in the VPC, and a run may not add ${nodes} more than ${max_nodes}" >&2
  exit 1
fi
ami=$(aws ssm get-parameter --output json \
  --name "/aws/service/eks/optimized-ami/${version}/amazon-linux-2023/x86_64/standard/recommended/image_id" |
  jq -r .Parameter.Value)

echo "==> Arming the delete of the cluster ${run} in ${cluster_lifetime_minutes} minutes"
schedule "${run}-delete-cluster" "${cluster_end}" eks:deleteCluster "$(jq -cn --arg n "${run}" '{name: $n}')"

echo "==> Creating the EKS cluster ${run} (Kubernetes ${version})"
aws eks create-cluster --name "${run}" --kubernetes-version "${version}" --role-arn "${AWS_EKS_CLUSTER_ROLE_ARN}" \
  --resources-vpc-config "subnetIds=${AWS_EKS_SUBNET_IDS},endpointPublicAccess=true,endpointPrivateAccess=true" \
  --access-config authenticationMode=API,bootstrapClusterCreatorAdminPermissions=true \
  --upgrade-policy supportType=STANDARD "${tags[@]}" > /dev/null
aws eks wait cluster-active --name "${run}"
cluster=$(aws eks describe-cluster --name "${run}" --output json)
endpoint=$(jq -r .cluster.endpoint <<< "${cluster}")
ca=$(jq -r .cluster.certificateAuthority.data <<< "${cluster}")
cidr=$(jq -r .cluster.kubernetesNetworkConfig.serviceIpv4Cidr <<< "${cluster}")
security_group=$(jq -r .cluster.resourcesVpcConfig.clusterSecurityGroupId <<< "${cluster}")

aws eks create-access-entry --cluster-name "${run}" --principal-arn "${AWS_EKS_NODE_ROLE_ARN}" --type EC2_LINUX \
  "${tags[@]}" > /dev/null
# The add-on takes no tags for an association that it creates, and the role
# of the run refuses an association without the run tag.
aws eks create-pod-identity-association --cluster-name "${run}" --namespace kube-system \
  --service-account ebs-csi-controller-sa --role-arn "${AWS_EKS_CSI_ROLE_ARN}" "${tags[@]}" > /dev/null
aws eks create-addon --cluster-name "${run}" --addon-name eks-pod-identity-agent "${tags[@]}" > /dev/null
aws eks create-addon --cluster-name "${run}" --addon-name aws-ebs-csi-driver --addon-version "${csi_version}" \
  --configuration-values "$(jq -cn --arg region "${AWS_REGION}" --arg run "${run}" \
    '{controller: {env: [{name: "AWS_REGION", value: $region}], extraVolumeTags: {"cpo-ci-run": $run}}}')" \
  "${tags[@]}" > /dev/null

# The script part arms the end of the node first. nodeadm reads the
# NodeConfig part, and Elasticsearch needs the kernel limit.
shutdown_minutes=$(((node_end - $(date +%s)) / 60 - boot_minutes))
[ "${shutdown_minutes}" -ge 1 ] || shutdown_minutes=1
cat > "${work}/user-data" << EOF
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="BOUNDARY"

--BOUNDARY
Content-Type: text/x-shellscript; charset="us-ascii"

#!/bin/bash
shutdown -h +${shutdown_minutes}
sysctl -w vm.max_map_count=262144

--BOUNDARY
Content-Type: application/node.eks.aws

---
apiVersion: node.eks.aws/v1alpha1
kind: NodeConfig
spec:
  cluster:
    name: ${run}
    apiServerEndpoint: ${endpoint}
    certificateAuthority: ${ca}
    cidr: ${cidr}

--BOUNDARY--
EOF

tag_specifications=$(jq -cn --arg run "${run}" '[
  {ResourceType: "instance", Tags: [{Key: "cpo-ci-run", Value: $run}, {Key: "Name", Value: ($run + "-node")},
    {Key: ("kubernetes.io/cluster/" + $run), Value: "owned"}]},
  {ResourceType: "volume", Tags: [{Key: "cpo-ci-run", Value: $run}]},
  {ResourceType: "network-interface", Tags: [{Key: "cpo-ci-run", Value: $run}]}
]')
echo "==> Starting the node of ${run}"
instance=""
for subnet in "${subnets[@]:0:2}"; do
  if out=$(aws ec2 run-instances --count "${nodes}" --image-id "${ami}" --instance-type "${AWS_EKS_INSTANCE_TYPE}" \
    --subnet-id "${subnet}" --security-group-ids "${security_group}" \
    --iam-instance-profile "Name=${AWS_EKS_NODE_PROFILE}" \
    --metadata-options HttpTokens=required,HttpPutResponseHopLimit=1,HttpEndpoint=enabled \
    --credit-specification CpuCredits=standard --instance-initiated-shutdown-behavior terminate \
    --block-device-mappings 'DeviceName=/dev/xvda,Ebs={VolumeSize=20,VolumeType=gp3,DeleteOnTermination=true}' \
    --tag-specifications "${tag_specifications}" --user-data "file://${work}/user-data" --output json 2> "${work}/err"); then
    instance=$(jq -r '.Instances[0].InstanceId' <<< "${out}")
    break
  fi
  cat "${work}/err" >&2
  # Only a zone without capacity is worth the other subnet.
  grep -q InsufficientInstanceCapacity "${work}/err" || exit 1
done
[ -n "${instance}" ] || { echo "no zone had capacity for ${AWS_EKS_INSTANCE_TYPE}" >&2; exit 1; }
schedule "${run}-terminate-nodes" "${node_end}" ec2:terminateInstances \
  "$(jq -cn --arg id "${instance}" '{InstanceIds: [$id]}')"

echo "==> Waiting for the node ${instance}"
aws eks update-kubeconfig --name "${run}" --alias "${run}" > /dev/null
for _ in $(seq 1 60); do
  [ -z "$(kubectl get nodes -o name 2> /dev/null)" ] || break
  sleep 10
done
kubectl wait --for=condition=Ready nodes --all --timeout=10m
aws eks wait addon-active --cluster-name "${run}" --addon-name eks-pod-identity-agent
aws eks wait addon-active --cluster-name "${run}" --addon-name aws-ebs-csi-driver
echo "cluster=${run}"
