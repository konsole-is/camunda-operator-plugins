#!/usr/bin/env bash
# Checks the cost limits of the roles of an EKS run with the IAM policy
# simulator, after an apply of terraform/aws-sandbox. It prints one line for
# each case that starts with "ok" or "FAIL", and a last line
# "eks-limits: <n> ok, <m> failed". It exits 1 when a case fails or a
# simulator call fails.
#
# Usage: eks-limits.sh
#
# Needs AWS_REGION and administrator credentials of the sandbox account.
set -euo pipefail

: "${AWS_REGION:?AWS_REGION is not set}"
account=$(aws sts get-caller-identity --query Account --output text)
ec2=arn:aws:ec2:${AWS_REGION}:${account}
failed=0
passed=0

# expect <role> <action> <resource> <want> [<key>=<type>:<value>...] runs one
# case. want is allowed or explicitDeny; an implicitDeny counts as denied.
# Each case runs in AWS_REGION unless it names aws:RequestedRegion.
expect() {
  local role=$1 action=$2 resource=$3 want=$4 entry key rest name decision
  shift 4
  local entries=() region_set=false
  for entry in "$@"; do
    key=${entry%%=*}
    rest=${entry#*=}
    [ "${key}" != aws:RequestedRegion ] || region_set=true
    entries+=("ContextKeyName=${key},ContextKeyValues=${rest#*:},ContextKeyType=${rest%%:*}")
  done
  ${region_set} || entries+=("ContextKeyName=aws:RequestedRegion,ContextKeyValues=${AWS_REGION},ContextKeyType=string")

  name="${action} by ${role}"
  [ "${resource}" = "*" ] || name+=" on ${resource##*:}"
  [ $# -eq 0 ] || name+=" with $*"
  if ! decision=$(aws iam simulate-principal-policy \
    --policy-source-arn "arn:aws:iam::${account}:role/${role}" \
    --action-names "${action}" --resource-arns "${resource}" \
    --context-entries "${entries[@]}" \
    --query 'EvaluationResults[0].EvalDecision' --output text); then
    echo "FAIL ${name}: the simulator call failed"
    failed=$((failed + 1))
    return
  fi
  if [ "${decision}" = "${want}" ] || { [ "${want}" = explicitDeny ] && [ "${decision}" = implicitDeny ]; }; then
    echo "ok ${name}"
    passed=$((passed + 1))
  else
    echo "FAIL ${name}: ${decision}, want ${want}"
    failed=$((failed + 1))
  fi
}

tag=aws:RequestTag/cpo-ci-run=string:cpo-ci-limits
node=(ec2:MetadataHttpPutResponseHopLimit=numeric:1 ec2:MetadataHttpTokens=string:required)

# The run role.
role=cpo-sandbox-eks
expect "${role}" ec2:RunInstances "${ec2}:instance/*" allowed ec2:InstanceType=string:t3.xlarge "${node[@]}" "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:m5.4xlarge "${node[@]}" "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge \
  ec2:MetadataHttpPutResponseHopLimit=numeric:2 ec2:MetadataHttpTokens=string:required "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge "${node[@]}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge "${node[@]}" "${tag}" \
  aws:RequestedRegion=string:us-east-1
# CreateCluster takes no resource, so a real call is checked against "*".
expect "${role}" eks:CreateCluster "*" allowed eks:supportType=string:STANDARD "${tag}"
expect "${role}" eks:CreateCluster "*" explicitDeny eks:supportType=string:EXTENDED "${tag}"
expect "${role}" eks:CreateCluster "*" explicitDeny eks:supportType=string:STANDARD eks:loggingType/api=boolean:true "${tag}"
expect "${role}" eks:CreateCluster "*" explicitDeny eks:supportType=string:STANDARD
cluster=arn:aws:eks:${AWS_REGION}:${account}:cluster/other
expect "${role}" eks:DeleteCluster "${cluster}" allowed aws:ResourceTag/cpo-ci-run=string:cpo-ci-limits
expect "${role}" eks:DeleteCluster "${cluster}" explicitDeny
# A Pod Identity association reads and passes its role: only the two roles
# of the pods of a run.
for action in iam:GetRole iam:PassRole; do
  for pod_role in cpo-sandbox-eks-ebs-csi cpo-sandbox-eks-plugins; do
    expect "${role}" "${action}" "arn:aws:iam::${account}:role/${pod_role}" allowed
  done
  expect "${role}" "${action}" "arn:aws:iam::${account}:role/cpo-sandbox-eks" explicitDeny
done
for action in ec2:CreateNatGateway ec2:AllocateAddress elasticloadbalancing:CreateLoadBalancer eks:CreateNodegroup \
  logs:CreateLogGroup iam:CreateRole; do
  expect "${role}" "${action}" "*" explicitDeny
done

# The roles that EKS and the node use.
for role in cpo-sandbox-eks-cluster cpo-sandbox-eks-node; do
  for action in elasticloadbalancing:CreateLoadBalancer ec2:CreateNatGateway ec2:AllocateAddress; do
    expect "${role}" "${action}" "*" explicitDeny
  done
done

# The EBS CSI controller. Its managed policy also wants the tag of the driver.
role=cpo-sandbox-eks-ebs-csi
driver=aws:RequestTag/ebs.csi.aws.com/cluster=string:true
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" allowed ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}" "${tag}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:io2 ec2:VolumeSize=numeric:1 "${driver}" "${tag}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:100 "${driver}" "${tag}"
expect "${role}" ec2:CreateSnapshot "${ec2}:volume/*" explicitDeny "${driver}" "${tag}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 \
  ec2:VolumeIops=numeric:16000 "${driver}" "${tag}"
expect "${role}" ec2:CopyVolumes "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:100 "${driver}" "${tag}"
expect "${role}" ec2:ModifyVolume "${ec2}:volume/*" explicitDeny aws:ResourceTag/ebs.csi.aws.com/cluster=string:true

# The controllers of the release aws.
role=cpo-sandbox-eks-plugins
expect "${role}" kms:CreateKey "*" allowed kms:KeyOrigin=string:AWS_KMS "${tag}"
expect "${role}" kms:CreateKey "*" explicitDeny kms:KeyOrigin=string:AWS_KMS
expect "${role}" iam:CreateRole "arn:aws:iam::${account}:role/cpo-ci-limits-hub-x" explicitDeny "${tag}"
expect "${role}" kms:PutKeyPolicy "arn:aws:kms:${AWS_REGION}:${account}:key/x" explicitDeny

echo "eks-limits: ${passed} ok, ${failed} failed"
[ "${failed}" -eq 0 ]
