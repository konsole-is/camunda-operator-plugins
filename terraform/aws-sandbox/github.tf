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
