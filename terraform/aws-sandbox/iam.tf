data "aws_caller_identity" "current" {}

data "aws_partition" "current" {}

locals {
  iam          = "arn:${data.aws_partition.current.partition}:iam::${data.aws_caller_identity.current.account_id}"
  boundary_arn = "${local.iam}:policy/cpo-sandbox-boundary"
  run_roles    = "${local.iam}:role/cpo-ci-*"
  run_policies = "${local.iam}:policy/cpo-ci-*"
  # The kind cluster of a run has its OIDC issuer under the issuer bucket.
  run_providers = "${local.iam}:oidc-provider/${aws_s3_bucket.issuer.bucket_regional_domain_name}/*"
  run_buckets   = ["arn:${data.aws_partition.current.partition}:s3:::cpo-ci-*", "arn:${data.aws_partition.current.partition}:s3:::cpo-ci-*/*"]
  kms_regions   = [var.region, var.kms_replica_region]
  keys          = [for r in local.kms_regions : "arn:${data.aws_partition.current.partition}:kms:${r}:${data.aws_caller_identity.current.account_id}:key/*"]
  aliases       = [for r in local.kms_regions : "arn:${data.aws_partition.current.partition}:kms:${r}:${data.aws_caller_identity.current.account_id}:alias/cpo-ci-*"]
  issuer_documents = [
    "${aws_s3_bucket.issuer.arn}/*/.well-known/openid-configuration",
    "${aws_s3_bucket.issuer.arn}/*/openid/v1/jwks",
  ]

  github_oidc_provider_arn = coalesce(var.github_oidc_provider_arn, one(aws_iam_openid_connect_provider.github[*].arn))
}

# KMS creates this role at the first multi-Region key, and the boundary does
# not allow a run to create it.
resource "aws_iam_service_linked_role" "kms_multi_region" {
  aws_service_name = "mrk.kms.amazonaws.com"
}

resource "aws_iam_openid_connect_provider" "github" {
  count = var.github_oidc_provider_arn == null ? 1 : 0

  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
}

# The S3 and KMS grants. The boundary and the permissions of the CI role both
# hold them, because a role can do only what both allow.
data "aws_iam_policy_document" "storage" {
  statement {
    actions   = ["s3:*"]
    resources = local.run_buckets
  }
  statement {
    actions   = ["s3:PutObject", "s3:GetObject", "s3:DeleteObject"]
    resources = local.issuer_documents
  }
  statement {
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.issuer.arn]
  }

  # A key in a custom key store costs by the hour.
  statement {
    actions   = ["kms:CreateKey"]
    resources = ["*"]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["false"]
    }
    condition {
      test     = "StringEquals"
      variable = "kms:KeyOrigin"
      values   = ["AWS_KMS"]
    }
  }
  # CreateKey with tags also needs TagResource, before the key has a tag.
  statement {
    actions   = ["kms:TagResource"]
    resources = local.keys
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions   = ["kms:*"]
    resources = local.keys
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions   = ["kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"]
    resources = concat(local.aliases, local.keys)
  }
}

# The most that the CI role and every role of a run can do. IAM is allowed
# wide on cpo-ci-* names here, and the denies hold every limit: a role of a run
# gets only the policies that the run writes, so only the boundary binds it.
data "aws_iam_policy_document" "boundary" {
  source_policy_documents = [data.aws_iam_policy_document.storage.json]

  statement {
    actions = [
      "sts:GetCallerIdentity",
      "iam:ListRoles",
      "iam:ListPolicies",
      "iam:ListOpenIDConnectProviders",
      "kms:ListKeys",
      "kms:ListAliases",
      "s3:ListAllMyBuckets",
    ]
    resources = ["*"]
  }
  statement {
    actions   = ["iam:*", "sts:AssumeRole", "sts:TagSession"]
    resources = [local.run_roles, local.run_policies, local.run_providers]
  }
  statement {
    actions   = ["iam:GetPolicy", "iam:GetPolicyVersion"]
    resources = [local.boundary_arn]
  }

  # A role of a run gets the boundary, and a policy goes only on such a role.
  statement {
    effect = "Deny"
    actions = [
      "iam:CreateRole",
      "iam:PutRolePermissionsBoundary",
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
    ]
    resources = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "iam:PermissionsBoundary"
      values   = [local.boundary_arn]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["iam:AttachRolePolicy"]
    resources = ["*"]
    condition {
      test     = "ArnNotLike"
      variable = "iam:PolicyARN"
      values   = [local.run_policies]
    }
  }
  # The sweep finds what a run created by this tag.
  statement {
    effect = "Deny"
    actions = [
      "iam:CreateRole",
      "iam:CreatePolicy",
      "iam:CreateOpenIDConnectProvider",
      "kms:CreateKey",
    ]
    resources = ["*"]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["true"]
    }
  }
  statement {
    effect = "Deny"
    actions = [
      "iam:CreateRole",
      "iam:CreatePolicy",
      "iam:CreateOpenIDConnectProvider",
      "iam:TagRole",
      "iam:TagPolicy",
      "iam:TagOpenIDConnectProvider",
      "kms:CreateKey",
      "kms:TagResource",
    ]
    resources = ["*"]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["false"]
    }
    condition {
      test     = "StringNotLike"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["cpo-ci-?*"]
    }
  }
  # cpo-ci-* also matches a path, and the sweep lists roles and policies by name.
  statement {
    effect    = "Deny"
    actions   = ["iam:CreateRole", "iam:CreatePolicy"]
    resources = ["${local.iam}:role/*/*", "${local.iam}:policy/*/*"]
  }
  statement {
    effect    = "Deny"
    actions   = ["iam:UntagRole", "iam:UntagPolicy", "iam:UntagOpenIDConnectProvider", "kms:UntagResource"]
    resources = ["*"]
    condition {
      test     = "ForAnyValue:StringEqualsIgnoreCase"
      variable = "aws:TagKeys"
      values   = ["cpo-ci-run"]
    }
  }
  statement {
    effect = "Deny"
    actions = [
      "iam:DeleteRolePermissionsBoundary",
      "iam:CreateUser",
      "iam:CreateAccessKey",
      "iam:CreateLoginProfile",
      "iam:UpdateAccessKey",
    ]
    resources = ["*"]
  }
  statement {
    effect = "Deny"
    not_actions = [
      "iam:GetPolicy",
      "iam:GetPolicyVersion",
      "s3:PutObject",
      "s3:GetObject",
      "s3:DeleteObject",
      "s3:ListBucket",
    ]
    resources = [
      "${local.iam}:policy/cpo-sandbox-*",
      "${local.iam}:role/cpo-sandbox-*",
      "arn:${data.aws_partition.current.partition}:s3:::cpo-sandbox-*",
    ]
  }
  statement {
    effect      = "Deny"
    not_actions = ["iam:*", "sts:*", "kms:*"]
    resources   = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = [var.region]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["kms:*"]
    resources = ["*"]
    condition {
      test     = "StringNotEquals"
      variable = "aws:RequestedRegion"
      values   = local.kms_regions
    }
  }
  # A key policy can shut out the sweep.
  statement {
    effect    = "Deny"
    actions   = ["kms:PutKeyPolicy"]
    resources = ["*"]
  }
  statement {
    effect    = "Deny"
    actions   = ["kms:CreateKey"]
    resources = ["*"]
    condition {
      test     = "Bool"
      variable = "kms:BypassPolicyLockoutSafetyCheck"
      values   = ["true"]
    }
  }
  statement {
    effect    = "Deny"
    actions   = ["kms:ScheduleKeyDeletion"]
    resources = ["*"]
    condition {
      test     = "NumericNotEquals"
      variable = "kms:ScheduleKeyDeletionPendingWindowInDays"
      values   = ["7"]
    }
  }
  # A new bucket blocks public access, and this keeps it so. Object Lock or a
  # bucket policy can keep the sweep from deleting the objects.
  statement {
    effect = "Deny"
    actions = [
      "s3:PutBucketAcl",
      "s3:PutObjectAcl",
      "s3:PutBucketOwnershipControls",
      "s3:PutBucketPublicAccessBlock",
      "s3:PutBucketPolicy",
      "s3:PutBucketObjectLockConfiguration",
      "s3:PutObjectRetention",
      "s3:PutObjectLegalHold",
    ]
    resources = local.run_buckets
  }
}

# The IAM grants of the CI role, one by one, within the boundary.
data "aws_iam_policy_document" "ci_iam" {
  statement {
    actions = [
      "sts:GetCallerIdentity",
      "iam:ListRoles",
      "iam:ListPolicies",
      "iam:ListOpenIDConnectProviders",
      "kms:ListKeys",
      "kms:ListAliases",
      "s3:ListAllMyBuckets",
    ]
    resources = ["*"]
  }

  statement {
    actions   = ["iam:CreateRole", "iam:CreatePolicy", "iam:TagRole", "iam:TagPolicy"]
    resources = [local.run_roles, local.run_policies]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions = [
      "iam:PutRolePermissionsBoundary",
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
      "iam:DeleteRole",
      "iam:TagRole",
      "iam:UntagRole",
      "iam:UpdateAssumeRolePolicy",
      "iam:UpdateRole",
      "iam:UpdateRoleDescription",
      "iam:DeletePolicy",
      "iam:CreatePolicyVersion",
      "iam:DeletePolicyVersion",
      "iam:TagPolicy",
      "iam:UntagPolicy",
    ]
    resources = [local.run_roles, local.run_policies]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  # Reads carry no tag condition: a read of a role that does not exist must
  # answer NoSuchEntity, not AccessDenied.
  statement {
    actions = [
      "iam:GetRole",
      "iam:GetRolePolicy",
      "iam:ListRolePolicies",
      "iam:ListAttachedRolePolicies",
      "iam:ListRoleTags",
      "iam:GetPolicy",
      "iam:GetPolicyVersion",
      "iam:ListPolicyVersions",
      "iam:ListPolicyTags",
      "sts:AssumeRole",
      "sts:TagSession",
    ]
    resources = [local.run_roles, local.run_policies]
  }
  statement {
    actions   = ["iam:GetPolicy", "iam:GetPolicyVersion"]
    resources = [local.boundary_arn]
  }

  statement {
    actions   = ["iam:CreateOpenIDConnectProvider", "iam:TagOpenIDConnectProvider"]
    resources = [local.run_providers]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions   = ["iam:DeleteOpenIDConnectProvider"]
    resources = [local.run_providers]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    actions   = ["iam:GetOpenIDConnectProvider", "iam:ListOpenIDConnectProviderTags"]
    resources = [local.run_providers]
  }
}

resource "aws_iam_policy" "boundary" {
  name        = "cpo-sandbox-boundary"
  description = "Boundary of the CI role of the AWS plugins and of every role of a CI run. Only cpo-ci-* resources."
  policy      = data.aws_iam_policy_document.boundary.minified_json

  lifecycle {
    precondition {
      condition     = length(data.aws_iam_policy_document.boundary.minified_json) <= 6144
      error_message = "The boundary is longer than 6144 characters, the limit of a managed policy. A boundary is one policy, so move a grant to a policy of the CI role instead."
    }
  }
}

resource "aws_iam_policy" "ci" {
  for_each = {
    iam     = data.aws_iam_policy_document.ci_iam.minified_json
    storage = data.aws_iam_policy_document.storage.minified_json
  }

  name        = "cpo-sandbox-ci-${each.key}"
  description = "Permissions of the CI role of the AWS plugins, within cpo-sandbox-boundary."
  policy      = each.value

  lifecycle {
    precondition {
      condition     = length(each.value) <= 6144
      error_message = "The policy is longer than 6144 characters, the limit of a managed policy. Split it into one more policy."
    }
  }
}

resource "aws_iam_role" "ci" {
  name                 = "cpo-sandbox-ci"
  description          = "GitHub Actions of the plugins repository. Bounded to cpo-ci-* resources."
  max_session_duration = 3600
  permissions_boundary = aws_iam_policy.boundary.arn
  assume_role_policy   = data.aws_iam_policy_document.ci_trust.json
}

resource "aws_iam_role_policy_attachment" "ci" {
  for_each = aws_iam_policy.ci

  role       = aws_iam_role.ci.name
  policy_arn = each.value.arn
}

# Runs of the trusted user, in aws-sandbox, need no approval. Every other run
# uses aws-sandbox-review, where GitHub waits for an approval. GitHub gives a
# pull request from a fork no token.
data "aws_iam_policy_document" "ci_trust" {
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
      values   = ["${local.github_subject}:environment:${local.environments.trusted}:actor_id:${data.github_user.trusted.id}"]
    }
  }
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
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${local.github_subject}:environment:${local.environments.review}:actor_id:*"]
    }
  }
}
