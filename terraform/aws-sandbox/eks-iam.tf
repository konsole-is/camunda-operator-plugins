# The roles of an EKS run. Terraform owns every role that the run uses, so a
# run creates no IAM role of its own and gets no IAM create right. Each role
# has the deny policy cpo-sandbox-eks-limits, which holds the cost limits.

locals {
  account     = data.aws_caller_identity.current.account_id
  partition   = data.aws_partition.current.partition
  ec2         = "arn:${local.partition}:ec2:${var.region}:${local.account}"
  eks         = "arn:${local.partition}:eks:${var.region}:${local.account}"
  eks_vpc_arn = "${local.ec2}:vpc/${aws_vpc.eks.id}"
  eks_keys    = "arn:${local.partition}:kms:${var.region}:${local.account}:key/*"
  eks_parts = [
    "${local.eks}:cluster/cpo-ci-*",
    "${local.eks}:addon/cpo-ci-*/*",
    "${local.eks}:access-entry/cpo-ci-*/*",
    "${local.eks}:podidentityassociation/cpo-ci-*/*",
  ]
  managed_policy = "arn:${local.partition}:iam::aws:policy"

  # A create must carry the run tag, with a value that starts with cpo-ci-.
  run_tag_request = [
    { test = "Null", variable = "aws:RequestTag/cpo-ci-run", values = ["false"] },
    { test = "StringLike", variable = "aws:RequestTag/cpo-ci-run", values = ["cpo-ci-?*"] },
  ]
}

# EKS creates this role at the first cluster, and no role of a run can create
# a role.
resource "aws_iam_service_linked_role" "eks" {
  aws_service_name = "eks.amazonaws.com"
}

# Conditions in one statement must all hold, so each limit is a statement of
# its own.
data "aws_iam_policy_document" "eks_limits" {
  statement {
    sid    = "NoCostlyKinds"
    effect = "Deny"
    actions = [
      "ec2:CreateNatGateway",
      "ec2:AllocateAddress",
      "ec2:CreateVpcEndpoint",
      "ec2:CreateTransitGateway",
      "ec2:CreateVpnConnection",
      "ec2:CreateClientVpnEndpoint",
      "ec2:CreateSnapshot",
      "ec2:CreateSnapshots",
      "ec2:CreateFleet",
      "ec2:RequestSpotInstances",
      "ec2:RequestSpotFleet",
      "ec2:CreateLaunchTemplate",
      "ec2:AllocateHosts",
      "ec2:PurchaseReservedInstancesOffering",
      "ec2:CreateVpc",
      "ec2:CreateSubnet",
      "ec2:CreateInternetGateway",
      "elasticloadbalancing:CreateLoadBalancer",
      "elasticloadbalancing:CreateTargetGroup",
      "autoscaling:CreateAutoScalingGroup",
      "autoscaling:CreateLaunchConfiguration",
      "eks:CreateNodegroup",
      "eks:CreateFargateProfile",
      "eks:UpdateClusterConfig",
      "eks:AssociateEncryptionConfig",
      "cloudformation:CreateStack",
      "logs:CreateLogGroup",
      "ecr:CreateRepository",
    ]
    resources = ["*"]
  }
  statement {
    sid         = "OneRegion"
    effect      = "Deny"
    not_actions = ["iam:*", "sts:*"]
    resources   = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = [var.region]
    }
  }
  statement {
    sid       = "CheapInstances"
    effect    = "Deny"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.ec2}:instance/*"]
    condition {
      test     = "StringNotEquals"
      variable = "ec2:InstanceType"
      values   = var.eks_instance_types
    }
  }
  # With a hop limit of 1, a pod that is not on the host network cannot read
  # the credentials of the node role from the instance metadata.
  statement {
    sid       = "NoNodeRoleInPods"
    effect    = "Deny"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.ec2}:instance/*"]
    condition {
      test     = "NumericGreaterThan"
      variable = "ec2:MetadataHttpPutResponseHopLimit"
      values   = ["1"]
    }
  }
  # The sweep finds what a run created by this tag.
  statement {
    sid       = "KeepRunTag"
    effect    = "Deny"
    actions   = ["ec2:DeleteTags", "eks:UntagResource", "kms:UntagResource", "iam:UntagRole"]
    resources = ["*"]
    condition {
      test     = "ForAnyValue:StringEqualsIgnoreCase"
      variable = "aws:TagKeys"
      values   = ["cpo-ci-run"]
    }
  }
}

resource "aws_iam_policy" "eks_limits" {
  name        = "cpo-sandbox-eks-limits"
  description = "The cost limits of every role of an EKS run."
  policy      = data.aws_iam_policy_document.eks_limits.minified_json

  lifecycle {
    precondition {
      condition     = length(data.aws_iam_policy_document.eks_limits.minified_json) <= 6144
      error_message = "The policy is longer than 6144 characters, the limit of a managed policy. Split it into one more policy."
    }
  }
}

# The control plane of each cluster of a run.
data "aws_iam_policy_document" "eks_cluster_trust" {
  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }
    condition {
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["${local.eks}:cluster/cpo-ci-*"]
    }
  }
}

resource "aws_iam_role" "eks_cluster" {
  name               = "cpo-sandbox-eks-cluster"
  description        = "The control plane of the EKS cluster of a run."
  assume_role_policy = data.aws_iam_policy_document.eks_cluster_trust.json
}

# The node of a run.
data "aws_iam_policy_document" "eks_node_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "eks_node" {
  name               = "cpo-sandbox-eks-node"
  description        = "The node of the EKS cluster of a run."
  assume_role_policy = data.aws_iam_policy_document.eks_node_trust.json
}

resource "aws_iam_instance_profile" "eks_node" {
  name = "cpo-sandbox-eks-node"
  role = aws_iam_role.eks_node.name
}

# pod_trust holds the trust of a role that EKS Pod Identity gives to one
# ServiceAccount, in a cluster of a run only.
data "aws_iam_policy_document" "eks_pod_trust" {
  for_each = {
    ebs_csi = { namespace = "kube-system", service_account = "ebs-csi-controller-sa" }
    plugins = { namespace = var.eks_plugins_namespace, service_account = "aws" }
  }

  statement {
    actions = ["sts:AssumeRole", "sts:TagSession"]
    principals {
      type        = "Service"
      identifiers = ["pods.eks.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/kubernetes-namespace"
      values   = [each.value.namespace]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:RequestTag/kubernetes-service-account"
      values   = [each.value.service_account]
    }
    condition {
      test     = "StringLike"
      variable = "aws:RequestTag/eks-cluster-name"
      values   = ["cpo-ci-*"]
    }
  }
}

# The EBS CSI controller of a run. Its volumes carry the run tag, and it uses
# only keys with the run tag.
data "aws_iam_policy_document" "eks_ebs_csi" {
  statement {
    actions   = ["kms:CreateGrant", "kms:ListGrants", "kms:RevokeGrant"]
    resources = [local.eks_keys]
    condition {
      test     = "Bool"
      variable = "kms:GrantIsForAWSResource"
      values   = ["true"]
    }
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:DescribeKey"]
    resources = [local.eks_keys]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["ec2:CreateVolume"]
    resources = ["${local.ec2}:volume/*"]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["true"]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["ec2:CreateVolume"]
    resources = ["${local.ec2}:volume/*"]
    condition {
      test     = "StringNotEquals"
      variable = "ec2:VolumeType"
      values   = ["gp3"]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["ec2:CreateVolume"]
    resources = ["${local.ec2}:volume/*"]
    condition {
      test     = "NumericGreaterThan"
      variable = "ec2:VolumeSize"
      values   = ["10"]
    }
  }
}

resource "aws_iam_policy" "eks_ebs_csi" {
  name        = "cpo-sandbox-eks-ebs-csi"
  description = "The keys and the volume limits of the EBS CSI controller of an EKS run."
  policy      = data.aws_iam_policy_document.eks_ebs_csi.minified_json
}

resource "aws_iam_role" "eks_ebs_csi" {
  name               = "cpo-sandbox-eks-ebs-csi"
  description        = "The EBS CSI controller of the EKS cluster of a run, through Pod Identity."
  assume_role_policy = data.aws_iam_policy_document.eks_pod_trust["ebs_csi"].json
}

# The controllers aws-identity and aws-kms of the release aws of a run run as
# this role. It is the policy of docs/plugins/aws-identity.md and of
# test/conformance/aws/controller-policy-kms.json, held to the names and the
# tag of a run.
data "aws_iam_policy_document" "eks_plugins" {
  statement {
    sid       = "CreateRolesWithTheBoundary"
    actions   = ["iam:CreateRole"]
    resources = ["${local.iam}:role/cpo-ci-*-hub-*"]
    condition {
      test     = "StringEquals"
      variable = "iam:PermissionsBoundary"
      values   = [local.boundary_arn]
    }
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  statement {
    sid       = "RolesWithTheBoundary"
    actions   = ["iam:PutRolePermissionsBoundary", "iam:PutRolePolicy"]
    resources = ["${local.iam}:role/cpo-ci-*-hub-*"]
    condition {
      test     = "StringEquals"
      variable = "iam:PermissionsBoundary"
      values   = [local.boundary_arn]
    }
  }
  statement {
    sid = "Roles"
    actions = [
      "iam:GetRole", "iam:UpdateAssumeRolePolicy", "iam:TagRole", "iam:DeleteRole",
      "iam:GetRolePolicy", "iam:ListRolePolicies", "iam:DeleteRolePolicy",
      "iam:ListAttachedRolePolicies", "iam:DetachRolePolicy", "iam:PassRole",
    ]
    resources = ["${local.iam}:role/cpo-ci-*-hub-*"]
  }
  statement {
    sid       = "Health"
    actions   = ["iam:GetPolicy"]
    resources = [local.boundary_arn]
  }
  statement {
    sid       = "Reads"
    actions   = ["sts:GetCallerIdentity", "kms:ListAliases"]
    resources = ["*"]
  }
  statement {
    sid       = "CreatePodIdentityAssociations"
    actions   = ["eks:CreatePodIdentityAssociation", "eks:TagResource"]
    resources = ["${local.eks}:cluster/cpo-ci-*", "${local.eks}:podidentityassociation/cpo-ci-*/*"]
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  statement {
    sid = "PodIdentityAssociations"
    actions = [
      "eks:ListPodIdentityAssociations", "eks:DescribePodIdentityAssociation",
      "eks:UpdatePodIdentityAssociation", "eks:DeletePodIdentityAssociation",
    ]
    resources = ["${local.eks}:cluster/cpo-ci-*", "${local.eks}:podidentityassociation/cpo-ci-*/*"]
  }
  # A key in a custom key store costs by the hour, and a multi-Region key
  # needs a replica region, which an EKS run does not use.
  statement {
    sid       = "CreateKeys"
    actions   = ["kms:CreateKey"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "kms:KeyOrigin"
      values   = ["AWS_KMS"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "kms:MultiRegion"
      values   = ["false"]
    }
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  # CreateKey with tags also needs TagResource, before the key has a tag.
  statement {
    sid       = "TagNewKeys"
    actions   = ["kms:TagResource"]
    resources = [local.eks_keys]
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  statement {
    sid       = "Aliases"
    actions   = ["kms:CreateAlias", "kms:DeleteAlias"]
    resources = ["arn:${local.partition}:kms:${var.region}:${local.account}:alias/cpo-ci-*/*", local.eks_keys]
  }
  statement {
    sid = "Keys"
    actions = [
      "kms:DescribeKey", "kms:ListResourceTags", "kms:TagResource", "kms:UntagResource",
      "kms:GetKeyRotationStatus", "kms:EnableKeyRotation", "kms:ScheduleKeyDeletion",
    ]
    resources = [local.eks_keys]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    sid       = "ShortestDeletionWindow"
    effect    = "Deny"
    actions   = ["kms:ScheduleKeyDeletion"]
    resources = ["*"]
    condition {
      test     = "NumericNotEquals"
      variable = "kms:ScheduleKeyDeletionPendingWindowInDays"
      values   = ["7"]
    }
  }
  # A key policy can shut out the sweep.
  statement {
    sid       = "NoKeyPolicy"
    effect    = "Deny"
    actions   = ["kms:PutKeyPolicy", "kms:ReplicateKey"]
    resources = ["*"]
  }
  statement {
    sid       = "NoLockout"
    effect    = "Deny"
    actions   = ["kms:CreateKey"]
    resources = ["*"]
    condition {
      test     = "Bool"
      variable = "kms:BypassPolicyLockoutSafetyCheck"
      values   = ["true"]
    }
  }
}

resource "aws_iam_policy" "eks_plugins" {
  name        = "cpo-sandbox-eks-plugins"
  description = "The controllers aws-identity and aws-kms of an EKS run, on cpo-ci-* names only."
  policy      = data.aws_iam_policy_document.eks_plugins.minified_json

  lifecycle {
    precondition {
      condition     = length(data.aws_iam_policy_document.eks_plugins.minified_json) <= 6144
      error_message = "The policy is longer than 6144 characters, the limit of a managed policy. Split it into one more policy."
    }
  }
}

resource "aws_iam_role" "eks_plugins" {
  name               = "cpo-sandbox-eks-plugins"
  description        = "The release aws of the EKS cluster of a run, through Pod Identity."
  assume_role_policy = data.aws_iam_policy_document.eks_pod_trust["plugins"].json
}

# The one-time schedules of a run delete its cluster and its node, also when
# the job of the run is gone.
data "aws_iam_policy_document" "eks_scheduler_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }
  }
}

data "aws_iam_policy_document" "eks_scheduler" {
  statement {
    actions   = ["eks:DeleteCluster"]
    resources = ["${local.eks}:cluster/cpo-ci-*"]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions   = ["ec2:TerminateInstances"]
    resources = ["${local.ec2}:instance/*"]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
}

resource "aws_iam_role" "eks_scheduler" {
  name               = "cpo-sandbox-eks-scheduler"
  description        = "The schedules that delete the EKS cluster and the node of a run."
  assume_role_policy = data.aws_iam_policy_document.eks_scheduler_trust.json
}

resource "aws_iam_role_policy" "eks_scheduler" {
  name   = "delete-runs"
  role   = aws_iam_role.eks_scheduler.id
  policy = data.aws_iam_policy_document.eks_scheduler.json
}

resource "aws_iam_role_policy_attachment" "eks_service" {
  for_each = {
    cluster          = { role = aws_iam_role.eks_cluster.name, policy = "${local.managed_policy}/AmazonEKSClusterPolicy" }
    node_worker      = { role = aws_iam_role.eks_node.name, policy = "${local.managed_policy}/AmazonEKSWorkerNodePolicy" }
    node_cni         = { role = aws_iam_role.eks_node.name, policy = "${local.managed_policy}/AmazonEKS_CNI_Policy" }
    node_registry    = { role = aws_iam_role.eks_node.name, policy = "${local.managed_policy}/AmazonEC2ContainerRegistryPullOnly" }
    ebs_csi_managed  = { role = aws_iam_role.eks_ebs_csi.name, policy = "${local.managed_policy}/AmazonEBSCSIDriverPolicyV2" }
    ebs_csi_keys     = { role = aws_iam_role.eks_ebs_csi.name, policy = aws_iam_policy.eks_ebs_csi.arn }
    plugins          = { role = aws_iam_role.eks_plugins.name, policy = aws_iam_policy.eks_plugins.arn }
    limits_cluster   = { role = aws_iam_role.eks_cluster.name, policy = aws_iam_policy.eks_limits.arn }
    limits_node      = { role = aws_iam_role.eks_node.name, policy = aws_iam_policy.eks_limits.arn }
    limits_ebs_csi   = { role = aws_iam_role.eks_ebs_csi.name, policy = aws_iam_policy.eks_limits.arn }
    limits_plugins   = { role = aws_iam_role.eks_plugins.name, policy = aws_iam_policy.eks_limits.arn }
    limits_scheduler = { role = aws_iam_role.eks_scheduler.name, policy = aws_iam_policy.eks_limits.arn }
  }

  role       = each.value.role
  policy_arn = each.value.policy
}

# The job of an EKS run. It creates the cluster and the node with the run
# tag and within the limits, and removes them.
data "aws_iam_policy_document" "eks_run" {
  statement {
    sid = "Reads"
    actions = [
      "sts:GetCallerIdentity",
      "eks:ListClusters",
      "eks:DescribeAddonVersions",
      "eks:DescribeAddonConfiguration",
      "ec2:Describe*",
      "ecr:GetAuthorizationToken",
      "scheduler:ListSchedules",
      "elasticloadbalancing:DescribeLoadBalancers",
      "logs:DescribeLogGroups",
    ]
    resources = ["*"]
  }
  statement {
    sid       = "AMIParameter"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:${local.partition}:ssm:${var.region}::parameter/aws/service/eks/optimized-ami/*"]
  }
  # Standard support only: extended support costs six times as much. No
  # control plane logs, no Auto Mode, no secrets encryption, and no
  # provisioned control plane.
  statement {
    sid       = "CreateCluster"
    actions   = ["eks:CreateCluster"]
    resources = ["${local.eks}:cluster/cpo-ci-*"]
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
    condition {
      test     = "StringEquals"
      variable = "eks:supportType"
      values   = ["STANDARD"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:loggingType/api"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:loggingType/audit"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:loggingType/authenticator"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:loggingType/controllerManager"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:loggingType/scheduler"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:computeConfigEnabled"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:elasticLoadBalancingEnabled"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:blockStorageEnabled"
      values   = ["false"]
    }
    condition {
      test     = "BoolIfExists"
      variable = "eks:deletionProtection"
      values   = ["false"]
    }
    condition {
      test     = "Null"
      variable = "eks:encryptionConfigProviderKeyArns"
      values   = ["true"]
    }
    condition {
      test     = "Null"
      variable = "eks:controlPlaneScalingTier"
      values   = ["true"]
    }
  }
  statement {
    sid       = "CreateParts"
    actions   = ["eks:CreateAddon", "eks:CreateAccessEntry", "eks:CreatePodIdentityAssociation", "eks:TagResource"]
    resources = local.eks_parts
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  # Reads carry no tag condition: a read of a cluster that is gone must
  # answer ResourceNotFound, which the waiters of the AWS CLI wait for.
  statement {
    sid = "ReadParts"
    actions = [
      "eks:DescribeCluster", "eks:DescribeAddon", "eks:ListAddons",
      "eks:ListPodIdentityAssociations", "eks:DescribePodIdentityAssociation",
    ]
    resources = local.eks_parts
  }
  statement {
    sid = "DeleteParts"
    actions = [
      "eks:DeleteCluster", "eks:DeleteAddon", "eks:DeleteAccessEntry", "eks:DeletePodIdentityAssociation",
    ]
    resources = local.eks_parts
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    sid       = "PassClusterRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.eks_cluster.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["eks.amazonaws.com"]
    }
  }
  statement {
    sid       = "PassNodeRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.eks_node.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ec2.amazonaws.com"]
    }
  }
  # The trust of each of these roles holds only pods.eks.amazonaws.com.
  statement {
    sid       = "PassPodRoles"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.eks_ebs_csi.arn, aws_iam_role.eks_plugins.arn]
  }
  statement {
    sid       = "PassSchedulerRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.eks_scheduler.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["scheduler.amazonaws.com"]
    }
  }
}

# RunInstances checks each resource that it creates or uses on its own.
data "aws_iam_policy_document" "eks_run_node" {
  statement {
    sid       = "RunNode"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.ec2}:instance/*"]
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
    condition {
      test     = "StringEquals"
      variable = "ec2:InstanceType"
      values   = var.eks_instance_types
    }
    condition {
      test     = "NumericLessThanEquals"
      variable = "ec2:MetadataHttpPutResponseHopLimit"
      values   = ["1"]
    }
    condition {
      test     = "StringEquals"
      variable = "ec2:MetadataHttpTokens"
      values   = ["required"]
    }
  }
  statement {
    sid       = "RunNodeVolume"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.ec2}:volume/*"]
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
    condition {
      test     = "NumericLessThanEquals"
      variable = "ec2:VolumeSize"
      values   = ["30"]
    }
    condition {
      test     = "StringEquals"
      variable = "ec2:VolumeType"
      values   = ["gp3"]
    }
  }
  statement {
    sid       = "RunNodeInterface"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.ec2}:network-interface/*"]
    dynamic "condition" {
      for_each = local.run_tag_request
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  statement {
    sid       = "RunNodeNetwork"
    actions   = ["ec2:RunInstances"]
    resources = ["${local.ec2}:subnet/*", "${local.ec2}:security-group/*"]
    condition {
      test     = "ArnEquals"
      variable = "ec2:Vpc"
      values   = [local.eks_vpc_arn]
    }
  }
  statement {
    sid       = "RunNodeImage"
    actions   = ["ec2:RunInstances"]
    resources = ["arn:${local.partition}:ec2:${var.region}::image/*"]
    condition {
      test     = "StringEquals"
      variable = "ec2:Owner"
      values   = var.eks_ami_owners
    }
  }
  statement {
    sid       = "TagOnRun"
    actions   = ["ec2:CreateTags"]
    resources = ["${local.ec2}:instance/*", "${local.ec2}:volume/*", "${local.ec2}:network-interface/*"]
    condition {
      test     = "StringEquals"
      variable = "ec2:CreateAction"
      values   = ["RunInstances"]
    }
  }
  statement {
    sid       = "EndNode"
    actions   = ["ec2:TerminateInstances", "ec2:GetConsoleOutput", "ec2:DeleteVolume"]
    resources = ["${local.ec2}:instance/*", "${local.ec2}:volume/*"]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  # A volume that the EBS CSI driver made carries this tag, also when it
  # lacks the run tag.
  statement {
    sid       = "DeleteDriverVolumes"
    actions   = ["ec2:DeleteVolume"]
    resources = ["${local.ec2}:volume/*"]
    condition {
      test     = "StringEquals"
      variable = "aws:ResourceTag/ebs.csi.aws.com/cluster"
      values   = ["true"]
    }
  }
  # The sandbox VPC holds only runs.
  statement {
    sid       = "CleanVPC"
    actions   = ["ec2:DeleteNetworkInterface", "ec2:DeleteSecurityGroup"]
    resources = ["${local.ec2}:network-interface/*", "${local.ec2}:security-group/*"]
    condition {
      test     = "ArnEquals"
      variable = "ec2:Vpc"
      values   = [local.eks_vpc_arn]
    }
  }
  statement {
    sid = "Images"
    actions = [
      "ecr:BatchCheckLayerAvailability", "ecr:InitiateLayerUpload", "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
      "ecr:DescribeImages", "ecr:ListImages", "ecr:BatchDeleteImage",
    ]
    resources = [for r in aws_ecr_repository.eks : r.arn]
  }
  statement {
    sid       = "Schedules"
    actions   = ["scheduler:CreateSchedule", "scheduler:GetSchedule", "scheduler:DeleteSchedule"]
    resources = ["arn:${local.partition}:scheduler:${var.region}:${local.account}:schedule/${aws_scheduler_schedule_group.eks.name}/cpo-ci-*"]
  }
  statement {
    sid       = "LogGroups"
    actions   = ["logs:DeleteLogGroup"]
    resources = ["arn:${local.partition}:logs:${var.region}:${local.account}:log-group:/aws/eks/cpo-ci-*"]
  }
}

# What sweep.sh --run and leftovers.sh need for the hub role and the key of
# an EKS run, as the policies of the CI role in iam.tf, narrowed to reads and
# removals.
data "aws_iam_policy_document" "eks_cleanup" {
  statement {
    sid = "Lists"
    actions = [
      "iam:ListRoles", "iam:ListPolicies", "iam:ListOpenIDConnectProviders",
      "s3:ListAllMyBuckets", "kms:ListKeys", "kms:ListAliases",
    ]
    resources = ["*"]
  }
  statement {
    sid = "ReadRunIAM"
    actions = [
      "iam:ListRoleTags", "iam:ListRolePolicies", "iam:ListAttachedRolePolicies",
      "iam:ListPolicyTags", "iam:ListPolicyVersions",
    ]
    resources = [local.run_roles, local.run_policies]
  }
  statement {
    sid = "DeleteRunIAM"
    actions = [
      "iam:DeleteRolePolicy", "iam:DetachRolePolicy", "iam:DeleteRole",
      "iam:DeletePolicyVersion", "iam:DeletePolicy",
    ]
    resources = [local.run_roles, local.run_policies]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    sid       = "ReadRunKeys"
    actions   = ["kms:ListResourceTags", "kms:DescribeKey"]
    resources = [local.eks_keys]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    sid       = "DeleteRunAliases"
    actions   = ["kms:DeleteAlias"]
    resources = ["arn:${local.partition}:kms:${var.region}:${local.account}:alias/cpo-ci-*", local.eks_keys]
  }
  statement {
    sid       = "ScheduleRunKeys"
    actions   = ["kms:ScheduleKeyDeletion"]
    resources = [local.eks_keys]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
    condition {
      test     = "NumericEquals"
      variable = "kms:ScheduleKeyDeletionPendingWindowInDays"
      values   = ["7"]
    }
  }
}

resource "aws_iam_policy" "eks_ci" {
  for_each = {
    run      = data.aws_iam_policy_document.eks_run.minified_json
    run-node = data.aws_iam_policy_document.eks_run_node.minified_json
    cleanup  = data.aws_iam_policy_document.eks_cleanup.minified_json
  }

  name        = "cpo-sandbox-eks-${each.key}"
  description = "Permissions of the EKS run role cpo-sandbox-eks, within cpo-sandbox-eks-limits."
  policy      = each.value

  lifecycle {
    precondition {
      condition     = length(each.value) <= 6144
      error_message = "The policy is longer than 6144 characters, the limit of a managed policy. Split it into one more policy."
    }
  }
}

# Only the runs of the trusted user, in the environment aws-eks.
data "aws_iam_policy_document" "eks_ci_trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [local.github_oidc_provider_arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${local.github_subject}:environment:${local.eks_environment}:actor_id:${data.github_user.trusted.id}"]
    }
  }
}

# No boundary: the role has no IAM create right, so a boundary would limit
# nothing. Two hours cover the job and its cleanup.
resource "aws_iam_role" "eks_ci" {
  name                 = "cpo-sandbox-eks"
  description          = "The EKS proof workflow of the plugins repository. Only cpo-ci-* resources, within cpo-sandbox-eks-limits."
  max_session_duration = 7200
  assume_role_policy   = data.aws_iam_policy_document.eks_ci_trust.json

  lifecycle {
    precondition {
      condition     = length(var.alert_emails) > 0
      error_message = "The EKS role needs a budget alert address in alert_emails."
    }
  }
}

resource "aws_iam_role_policy_attachment" "eks_ci" {
  for_each = merge(
    { for k, p in aws_iam_policy.eks_ci : k => p.arn },
    { limits = aws_iam_policy.eks_limits.arn },
  )

  role       = aws_iam_role.eks_ci.name
  policy_arn = each.value
}
