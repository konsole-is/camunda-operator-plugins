data "github_repository" "plugins" {
  full_name = "${var.github_owner}/${var.github_repository}"
}

data "github_user" "owner" {
  username = var.github_owner
}

data "github_user" "trusted" {
  username = var.trusted_user
}

locals {
  # The repository part of the OIDC subject. It has this form only after the
  # OIDC subject customization of README.md, which the provider cannot set.
  github_subject = "repo:${var.github_owner}@${data.github_user.owner.id}/${var.github_repository}@${data.github_repository.plugins.repo_id}"

  environments = {
    trusted = "aws-sandbox"
    review  = "aws-sandbox-review"
  }

  environment_variables = {
    AWS_ROLE_ARN      = aws_iam_role.ci.arn
    AWS_REGION        = var.region
    AWS_ISSUER_BUCKET = aws_s3_bucket.issuer.bucket
    # The sweep removes the replicas of multi-Region keys there.
    AWS_KMS_REPLICA_REGION = var.kms_replica_region
  }
}

resource "github_repository_environment" "trusted" {
  repository  = var.github_repository
  environment = local.environments.trusted
}

resource "github_repository_environment" "review" {
  repository          = var.github_repository
  environment         = local.environments.review
  prevent_self_review = false

  reviewers {
    users = [tonumber(data.github_user.trusted.id)]
  }
}

resource "github_actions_environment_variable" "sandbox" {
  for_each = {
    for pair in setproduct(values(local.environments), keys(local.environment_variables)) :
    "${pair[0]}/${pair[1]}" => { environment = pair[0], name = pair[1] }
  }

  repository    = var.github_repository
  environment   = each.value.environment
  variable_name = each.value.name
  value         = local.environment_variables[each.value.name]

  depends_on = [github_repository_environment.trusted, github_repository_environment.review]
}

# The workflows select aws-sandbox when the actor of the run is this user.
resource "github_actions_variable" "actor_id" {
  repository    = var.github_repository
  variable_name = "AWS_SANDBOX_ACTOR_ID"
  value         = data.github_user.trusted.id
}

# The EKS proof workflow runs in an environment of its own, whose token only
# the EKS run role trusts.
locals {
  eks_environment = "aws-eks"
  eks_registry    = "${local.account}.dkr.ecr.${var.region}.amazonaws.com"

  eks_environment_variables = {
    AWS_EKS_ROLE_ARN           = aws_iam_role.eks_ci.arn
    AWS_REGION                 = var.region
    AWS_EKS_VPC_ID             = aws_vpc.eks.id
    AWS_EKS_SUBNET_IDS         = join(",", aws_subnet.eks[*].id)
    AWS_EKS_REGISTRY           = local.eks_registry
    AWS_EKS_CLUSTER_ROLE_ARN   = aws_iam_role.eks_cluster.arn
    AWS_EKS_NODE_ROLE_ARN      = aws_iam_role.eks_node.arn
    AWS_EKS_NODE_PROFILE       = aws_iam_instance_profile.eks_node.name
    AWS_EKS_CSI_ROLE_ARN       = aws_iam_role.eks_ebs_csi.arn
    AWS_EKS_PLUGINS_ROLE_ARN   = aws_iam_role.eks_plugins.arn
    AWS_EKS_SCHEDULER_ROLE_ARN = aws_iam_role.eks_scheduler.arn
    AWS_EKS_INSTANCE_TYPE      = var.eks_instance_types[0]
  }
}

resource "github_repository_environment" "eks" {
  repository          = var.github_repository
  environment         = local.eks_environment
  prevent_self_review = false

  reviewers {
    users = [tonumber(data.github_user.trusted.id)]
  }
}

resource "github_actions_environment_variable" "eks" {
  for_each = toset(keys(local.eks_environment_variables))

  repository    = var.github_repository
  environment   = github_repository_environment.eks.environment
  variable_name = each.key
  value         = local.eks_environment_variables[each.key]
}

# The role cpo-sandbox-eks-sweep of sweep.tf trusts only this environment.
resource "github_repository_environment" "sweep" {
  repository  = var.github_repository
  environment = local.sweep_environment

  deployment_branch_policy {
    protected_branches     = false
    custom_branch_policies = true
  }
}

resource "github_repository_environment_deployment_policy" "sweep" {
  repository     = var.github_repository
  environment    = github_repository_environment.sweep.environment
  branch_pattern = "main"
}

resource "github_actions_environment_variable" "sweep" {
  for_each = {
    AWS_SWEEP_ROLE_ARN = aws_iam_role.eks_sweep.arn
    AWS_REGION         = var.region
    AWS_EKS_VPC_ID     = aws_vpc.eks.id
  }

  repository    = var.github_repository
  environment   = github_repository_environment.sweep.environment
  variable_name = each.key
  value         = each.value
}

# The EKS workflows run only when this configuration created the EKS parts.
resource "github_actions_variable" "eks_sandbox" {
  repository    = var.github_repository
  variable_name = "AWS_EKS_SANDBOX"
  value         = "true"
}
