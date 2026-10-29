output "ci_role_arn" {
  description = "The role of the CI jobs, AWS_ROLE_ARN in both environments."
  value       = aws_iam_role.ci.arn
}

output "issuer_bucket" {
  description = "The bucket of the run issuers, AWS_ISSUER_BUCKET in both environments."
  value       = aws_s3_bucket.issuer.bucket
}

output "issuer_host" {
  description = "The host of each run issuer, https://<host>/<run-id>."
  value       = aws_s3_bucket.issuer.bucket_regional_domain_name
}

output "boundary_arn" {
  description = "The permissions boundary of every role of a run."
  value       = aws_iam_policy.boundary.arn
}
