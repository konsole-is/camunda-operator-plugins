locals {
  sweep_environment = "aws-sandbox-sweep"
  sweep_function    = "cpo-sandbox-sweep"
  run_tag_resource = [
    { test = "StringLike", variable = "aws:ResourceTag/cpo-ci-run", values = ["cpo-ci-?*"] },
  ]
}

data "aws_iam_policy_document" "sweep" {
  statement {
    sid = "Lists"
    actions = [
      "sts:GetCallerIdentity", "tag:GetResources",
      "iam:ListRoles", "iam:ListPolicies", "iam:ListOpenIDConnectProviders",
      "s3:ListAllMyBuckets", "kms:ListKeys", "kms:ListAliases",
      "eks:ListClusters", "ec2:DescribeInstances", "ec2:DescribeVolumes",
      "ec2:DescribeNetworkInterfaces", "ec2:DescribeSecurityGroups",
      "scheduler:ListSchedules", "logs:DescribeLogGroups",
    ]
    resources = ["*"]
  }
  statement {
    sid = "ReadRunIAM"
    actions = [
      "iam:ListRoleTags", "iam:ListRolePolicies", "iam:ListAttachedRolePolicies",
      "iam:ListPolicyTags", "iam:ListPolicyVersions", "iam:GetOpenIDConnectProvider",
    ]
    resources = [local.run_roles, local.run_policies, local.run_providers]
  }
  statement {
    sid = "DeleteRunIAM"
    actions = [
      "iam:DeleteRolePolicy", "iam:DetachRolePolicy", "iam:DeleteRole",
      "iam:DeletePolicyVersion", "iam:DeletePolicy", "iam:DeleteOpenIDConnectProvider",
    ]
    resources = [local.run_roles, local.run_policies, local.run_providers]
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    sid       = "DeleteRunBuckets"
    actions   = ["s3:ListBucketVersions", "s3:DeleteObject", "s3:DeleteObjectVersion", "s3:DeleteBucket"]
    resources = local.run_buckets
  }
  statement {
    sid       = "ReadRunKeys"
    actions   = ["kms:DescribeKey", "kms:ListResourceTags"]
    resources = local.keys
    condition {
      test     = "Null"
      variable = "aws:ResourceTag/cpo-ci-run"
      values   = ["false"]
    }
  }
  statement {
    sid       = "DeleteRunAliases"
    actions   = ["kms:DeleteAlias"]
    resources = concat(local.aliases, local.keys)
  }
  statement {
    sid       = "ScheduleRunKeys"
    actions   = ["kms:ScheduleKeyDeletion"]
    resources = local.keys
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
  # A read of a cluster that is gone must answer ResourceNotFound.
  statement {
    sid       = "ReadClusters"
    actions   = ["eks:DescribeCluster"]
    resources = ["${local.eks}:cluster/*"]
  }
  statement {
    sid       = "DeleteRunClusters"
    actions   = ["eks:DeleteCluster"]
    resources = ["${local.eks}:cluster/*"]
    dynamic "condition" {
      for_each = local.run_tag_resource
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
    }
  }
  statement {
    sid       = "EndRunNodes"
    actions   = ["ec2:TerminateInstances", "ec2:DeleteVolume"]
    resources = ["${local.ec2}:instance/*", "${local.ec2}:volume/*"]
    dynamic "condition" {
      for_each = local.run_tag_resource
      content {
        test     = condition.value.test
        variable = condition.value.variable
        values   = condition.value.values
      }
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
    sid       = "RunImages"
    actions   = ["ecr:DescribeImages", "ecr:ListImages", "ecr:BatchDeleteImage"]
    resources = [for r in aws_ecr_repository.eks : r.arn]
  }
  statement {
    sid       = "RunSchedules"
    actions   = ["scheduler:GetSchedule", "scheduler:DeleteSchedule"]
    resources = ["arn:${local.partition}:scheduler:${var.region}:${local.account}:schedule/${aws_scheduler_schedule_group.eks.name}/cpo-ci-*"]
  }
  statement {
    sid       = "RunLogGroups"
    actions   = ["logs:DeleteLogGroup"]
    resources = ["arn:${local.partition}:logs:${var.region}:${local.account}:log-group:/aws/eks/cpo-ci-*"]
  }
}

resource "aws_iam_policy" "sweep" {
  name        = "cpo-sandbox-sweep"
  description = "The list and delete rights of the sweeps of the sandbox, on resources of runs only."
  policy      = data.aws_iam_policy_document.sweep.minified_json

  lifecycle {
    precondition {
      condition     = length(data.aws_iam_policy_document.sweep.minified_json) <= 6144
      error_message = "The policy is longer than 6144 characters, the limit of a managed policy. Split it into one more policy."
    }
  }
}

# The job sweep-eks, only from main, in an environment without a reviewer.
data "aws_iam_policy_document" "eks_sweep_trust" {
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
      values   = ["${local.github_subject}:environment:${local.sweep_environment}:actor_id:${data.github_user.trusted.id}"]
    }
  }
}

resource "aws_iam_role" "eks_sweep" {
  name                 = "cpo-sandbox-eks-sweep"
  description          = "The scheduled EKS sweep of the plugins repository. Lists and deletes resources of runs only."
  max_session_duration = 3600
  assume_role_policy   = data.aws_iam_policy_document.eks_sweep_trust.json
}

resource "aws_iam_role_policy_attachment" "eks_sweep" {
  role       = aws_iam_role.eks_sweep.name
  policy_arn = aws_iam_policy.sweep.arn
}

data "archive_file" "sweep" {
  type        = "zip"
  output_path = "${path.module}/.terraform/sweep.zip"

  source {
    content  = file("${path.module}/../../hack/aws-sandbox/sweeper/sweeper.py")
    filename = "sweeper.py"
  }
  source {
    content  = file("${path.module}/../../hack/aws-sandbox/sweep-rules.json")
    filename = "sweep-rules.json"
  }
}

data "aws_iam_policy_document" "sweep_function_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account]
    }
  }
}

resource "aws_iam_role" "sweep_function" {
  name               = local.sweep_function
  description        = "The function cpo-sandbox-sweep. Lists and deletes resources of runs only."
  assume_role_policy = data.aws_iam_policy_document.sweep_function_trust.json
}

resource "aws_cloudwatch_log_group" "sweep" {
  name              = "/aws/lambda/${local.sweep_function}"
  retention_in_days = 3
}

data "aws_iam_policy_document" "sweep_function_logs" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.sweep.arn}:*"]
  }
}

resource "aws_iam_role_policy" "sweep_function_logs" {
  name   = "logs"
  role   = aws_iam_role.sweep_function.id
  policy = data.aws_iam_policy_document.sweep_function_logs.json
}

resource "aws_iam_role_policy_attachment" "sweep_function" {
  role       = aws_iam_role.sweep_function.name
  policy_arn = aws_iam_policy.sweep.arn
}

# GitHub fires the schedule of the job sweep-eks only some times a day, so
# that job is the fallback. The account allows 10 concurrent executions and
# keeps them all unreserved, so the function reserves none.
resource "aws_lambda_function" "sweep" {
  function_name    = local.sweep_function
  description      = "Removes what CI runs left in the sandbox, once an hour."
  role             = aws_iam_role.sweep_function.arn
  runtime          = "python3.13"
  architectures    = ["arm64"]
  handler          = "sweeper.handler"
  filename         = data.archive_file.sweep.output_path
  source_code_hash = data.archive_file.sweep.output_base64sha256
  memory_size      = 128
  timeout          = 300

  environment {
    variables = {
      AWS_KMS_REPLICA_REGION = var.kms_replica_region
      AWS_EKS_VPC_ID         = aws_vpc.eks.id
      AWS_EKS_REPOSITORIES   = join(",", [for r in aws_ecr_repository.eks : r.name])
      AWS_EKS_SCHEDULE_GROUP = aws_scheduler_schedule_group.eks.name
    }
  }

  depends_on = [aws_cloudwatch_log_group.sweep, aws_iam_role_policy.sweep_function_logs]
}

data "aws_iam_policy_document" "sweep_scheduler_trust" {
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
    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_scheduler_schedule_group.sweep.arn]
    }
  }
}

resource "aws_iam_role" "sweep_scheduler" {
  name               = "${local.sweep_function}-scheduler"
  description        = "The hourly schedule of the function cpo-sandbox-sweep."
  assume_role_policy = data.aws_iam_policy_document.sweep_scheduler_trust.json
}

resource "aws_iam_role_policy" "sweep_scheduler" {
  name = "invoke"
  role = aws_iam_role.sweep_scheduler.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "lambda:InvokeFunction", Resource = aws_lambda_function.sweep.arn }]
  })
}

# The trust of the scheduler role can name a schedule group, not a schedule.
resource "aws_scheduler_schedule_group" "sweep" {
  name = local.sweep_function
}

resource "aws_scheduler_schedule" "sweep" {
  name                = local.sweep_function
  group_name          = aws_scheduler_schedule_group.sweep.name
  schedule_expression = "rate(1 hour)"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_lambda_function.sweep.arn
    role_arn = aws_iam_role.sweep_scheduler.arn
    retry_policy {
      maximum_retry_attempts = 0
    }
  }
}

# A pass that fails, and two hours without a pass, send an email to
# alert_emails, as the budgets do.
resource "aws_sns_topic" "alerts" {
  count = length(var.alert_emails) > 0 ? 1 : 0

  name = "cpo-sandbox-alerts"
}

resource "aws_sns_topic_subscription" "alerts" {
  for_each = length(var.alert_emails) > 0 ? toset(var.alert_emails) : toset([])

  topic_arn = aws_sns_topic.alerts[0].arn
  protocol  = "email"
  endpoint  = each.value
}

resource "aws_cloudwatch_metric_alarm" "sweep" {
  for_each = {
    errors = {
      metric      = "Errors"
      comparison  = "GreaterThanOrEqualToThreshold"
      missing     = "notBreaching"
      description = "A pass of the sweep of the sandbox failed. Read the log group /aws/lambda/cpo-sandbox-sweep."
    }
    no-pass = {
      metric      = "Invocations"
      comparison  = "LessThanThreshold"
      missing     = "breaching"
      description = "The sweep of the sandbox did not run for two hours. Check the schedule cpo-sandbox-sweep."
    }
  }

  alarm_name          = "${local.sweep_function}-${each.key}"
  alarm_description   = each.value.description
  namespace           = "AWS/Lambda"
  metric_name         = each.value.metric
  dimensions          = { FunctionName = aws_lambda_function.sweep.function_name }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = each.key == "errors" ? 1 : 2
  threshold           = 1
  comparison_operator = each.value.comparison
  treat_missing_data  = each.value.missing
  alarm_actions       = aws_sns_topic.alerts[*].arn
  ok_actions          = aws_sns_topic.alerts[*].arn
}
