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
# The sweeps skip this value of the run tag.
no_run=aws:RequestTag/cpo-ci-run=string:None
# A run tag of another run, and the session tag of a Pod Identity session
# of the cluster cpo-ci-limits.
other_run=aws:RequestTag/cpo-ci-run=string:cpo-ci-other
session=aws:PrincipalTag/eks-cluster-name=string:cpo-ci-limits
node=(ec2:MetadataHttpPutResponseHopLimit=numeric:1 ec2:MetadataHttpTokens=string:required)
other_region=us-east-1
[ "${AWS_REGION}" != "${other_region}" ] || other_region=us-west-2

# The run role.
role=cpo-sandbox-eks
expect "${role}" ec2:RunInstances "${ec2}:instance/*" allowed ec2:InstanceType=string:t3.xlarge "${node[@]}" "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:m5.4xlarge "${node[@]}" "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge \
  ec2:MetadataHttpPutResponseHopLimit=numeric:2 ec2:MetadataHttpTokens=string:required "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge "${node[@]}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge "${node[@]}" "${tag}" \
  "aws:RequestedRegion=string:${other_region}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge "${node[@]}" "${no_run}"
expect "${role}" ec2:RunInstances "${ec2}:instance/*" explicitDeny ec2:InstanceType=string:t3.xlarge "${node[@]}" "${tag}" \
  ec2:Tenancy=string:dedicated
root=(ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:20)
expect "${role}" ec2:RunInstances "${ec2}:volume/*" allowed "${root[@]}" "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:volume/*" explicitDeny "${root[@]}" ec2:VolumeIops=numeric:16000 "${tag}"
expect "${role}" ec2:RunInstances "${ec2}:volume/*" explicitDeny "${root[@]}" ec2:VolumeThroughput=numeric:1000 "${tag}"
# CreateCluster takes no resource, so a real call is checked against "*".
expect "${role}" eks:CreateCluster "*" allowed eks:supportType=string:STANDARD "${tag}"
expect "${role}" eks:CreateCluster "*" explicitDeny eks:supportType=string:EXTENDED "${tag}"
expect "${role}" eks:CreateCluster "*" explicitDeny eks:supportType=string:STANDARD eks:loggingType/api=boolean:true "${tag}"
expect "${role}" eks:CreateCluster "*" explicitDeny eks:supportType=string:STANDARD
cluster=arn:aws:eks:${AWS_REGION}:${account}:cluster/other
expect "${role}" eks:DeleteCluster "${cluster}" allowed aws:ResourceTag/cpo-ci-run=string:cpo-ci-limits
expect "${role}" eks:DeleteCluster "${cluster}" explicitDeny
expect "${role}" eks:DeleteCluster "${cluster}" explicitDeny aws:ResourceTag/cpo-ci-run=string:prod
expect "${role}" ec2:DeleteVolume "${ec2}:volume/vol-1" explicitDeny aws:ResourceTag/ebs.csi.aws.com/cluster=string:true
expect "${role}" tag:GetResources "*" allowed
# A Pod Identity association reads and passes its role: only the two roles
# of the pods of a run, and only to Pod Identity.
pods=iam:PassedToService=string:pods.eks.amazonaws.com
for pod_role in cpo-sandbox-eks-ebs-csi cpo-sandbox-eks-plugins; do
  expect "${role}" iam:GetRole "arn:aws:iam::${account}:role/${pod_role}" allowed
  expect "${role}" iam:PassRole "arn:aws:iam::${account}:role/${pod_role}" allowed "${pods}"
  expect "${role}" iam:PassRole "arn:aws:iam::${account}:role/${pod_role}" explicitDeny \
    iam:PassedToService=string:ec2.amazonaws.com
done
expect "${role}" iam:GetRole "arn:aws:iam::${account}:role/cpo-sandbox-eks" explicitDeny
expect "${role}" iam:PassRole "arn:aws:iam::${account}:role/cpo-sandbox-eks" explicitDeny "${pods}"
# The one access entry of a run is that of the node.
run_cluster=arn:aws:eks:${AWS_REGION}:${account}:cluster/cpo-ci-limits
node_role=eks:principalArn=string:arn:aws:iam::${account}:role/cpo-sandbox-eks-node
expect "${role}" eks:CreateAccessEntry "${run_cluster}" allowed "${node_role}" eks:accessEntryType=string:EC2_LINUX "${tag}"
expect "${role}" eks:CreateAccessEntry "${run_cluster}" explicitDeny \
  "eks:principalArn=string:arn:aws:iam::${account}:role/cpo-sandbox-eks" eks:accessEntryType=string:EC2_LINUX "${tag}"
expect "${role}" eks:CreateAccessEntry "${run_cluster}" explicitDeny "${node_role}" eks:accessEntryType=string:STANDARD "${tag}"
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
# The managed policy of the control plane can create and tag any volume.
role=cpo-sandbox-eks-cluster
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:io2 ec2:VolumeSize=numeric:1 "${tag}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1
expect "${role}" ec2:CreateTags "${ec2}:volume/vol-1" explicitDeny "${no_run}"
# A run tag changes only with the create of its resource.
expect "${role}" ec2:CreateTags "${ec2}:volume/vol-1" explicitDeny aws:TagKeys=stringList:cpo-ci-run "${tag}"

# The EBS CSI controller. Its managed policy also wants the tag of the driver.
role=cpo-sandbox-eks-ebs-csi
driver=aws:RequestTag/ebs.csi.aws.com/cluster=string:true
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" allowed ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}" "${tag}" \
  "${session}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}" \
  "${other_run}" "${session}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}" \
  "${tag}" "${session}" ec2:VolumeInitializationRate=numeric:300
expect "${role}" ec2:CreateTags "${ec2}:volume/vol-1" explicitDeny aws:ResourceTag/ebs.csi.aws.com/cluster=string:true \
  aws:TagKeys=stringList:cpo-ci-run "${tag}" "${session}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 "${driver}" \
  "${no_run}"
# Each case below has the session of its own run, so only the limit that it
# names can deny it.
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:io2 ec2:VolumeSize=numeric:1 "${driver}" "${tag}" \
  "${session}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:100 "${driver}" \
  "${tag}" "${session}"
expect "${role}" ec2:CreateSnapshot "${ec2}:volume/*" explicitDeny "${driver}" "${tag}" "${session}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 \
  ec2:VolumeIops=numeric:16000 "${driver}" "${tag}" "${session}"
expect "${role}" ec2:CreateVolume "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:1 \
  ec2:VolumeThroughput=numeric:1000 "${driver}" "${tag}" "${session}"
expect "${role}" ec2:CopyVolumes "${ec2}:volume/*" explicitDeny ec2:VolumeType=string:gp3 ec2:VolumeSize=numeric:100 "${driver}" \
  "${tag}" "${session}"
expect "${role}" ec2:ModifyVolume "${ec2}:volume/*" explicitDeny aws:ResourceTag/ebs.csi.aws.com/cluster=string:true

# The controllers of the release aws.
role=cpo-sandbox-eks-plugins
expect "${role}" kms:CreateKey "*" allowed kms:KeyOrigin=string:AWS_KMS "${tag}" "${session}"
expect "${role}" kms:CreateKey "*" explicitDeny kms:KeyOrigin=string:AWS_KMS "${session}"
expect "${role}" kms:CreateKey "*" explicitDeny kms:KeyOrigin=string:AWS_KMS "${other_run}" "${session}"
expect "${role}" iam:CreateRole "arn:aws:iam::${account}:role/cpo-ci-limits-hub-x" explicitDeny "${tag}"
expect "${role}" kms:PutKeyPolicy "arn:aws:kms:${AWS_REGION}:${account}:key/x" explicitDeny
# The controllers change only the roles, keys, and associations of their own
# run, and write only its run tag.
key=arn:aws:kms:${AWS_REGION}:${account}:key/x
hub=arn:aws:iam::${account}:role/cpo-ci-limits-hub-x
other_hub=arn:aws:iam::${account}:role/cpo-ci-other-hub-x
own=aws:ResourceTag/cpo-ci-run=string:cpo-ci-limits
other=aws:ResourceTag/cpo-ci-run=string:cpo-ci-other
expect "${role}" kms:TagResource "${key}" allowed "${own}" "${tag}" "${session}"
expect "${role}" kms:TagResource "${key}" explicitDeny "${own}" "${no_run}" "${session}"
expect "${role}" kms:TagResource "${key}" explicitDeny "${own}" "${other_run}" "${session}"
expect "${role}" kms:TagResource "${key}" explicitDeny "${other}" "${tag}" "${session}"
expect "${role}" kms:CreateAlias "${key}" explicitDeny "${other}" "${session}"
expect "${role}" kms:ScheduleKeyDeletion "${key}" explicitDeny "${other}" "${session}" \
  kms:ScheduleKeyDeletionPendingWindowInDays=numeric:7
expect "${role}" iam:TagRole "${hub}" allowed "${own}" "${tag}" "${session}"
expect "${role}" iam:TagRole "${hub}" explicitDeny "${own}" "${no_run}" "${session}"
expect "${role}" iam:TagRole "${hub}" explicitDeny "${own}" "${other_run}" "${session}"
expect "${role}" iam:UpdateAssumeRolePolicy "${hub}" allowed "${own}" "${session}"
expect "${role}" iam:UpdateAssumeRolePolicy "${other_hub}" explicitDeny "${other}" "${session}"
expect "${role}" iam:UpdateAssumeRolePolicy "${hub}" explicitDeny "${other}" "${session}"
expect "${role}" iam:PassRole "${hub}" allowed "${own}" "${session}" "${pods}"
expect "${role}" iam:PassRole "${hub}" explicitDeny "${own}" "${session}" iam:PassedToService=string:ec2.amazonaws.com
expect "${role}" eks:DeletePodIdentityAssociation \
  "arn:aws:eks:${AWS_REGION}:${account}:podidentityassociation/cpo-ci-other/a-1" explicitDeny "${session}"

echo "eks-limits: ${passed} ok, ${failed} failed"
[ "${failed}" -eq 0 ]
