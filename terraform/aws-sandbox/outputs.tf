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

output "eks_role_arn" {
  description = "The role of the EKS proof workflow, AWS_EKS_ROLE_ARN in aws-eks."
  value       = aws_iam_role.eks_ci.arn
}

output "eks_vpc_id" {
  description = "The VPC of the EKS runs, AWS_EKS_VPC_ID in aws-eks."
  value       = aws_vpc.eks.id
}

output "eks_subnet_ids" {
  description = "The subnets of the EKS runs, comma-separated as AWS_EKS_SUBNET_IDS in aws-eks."
  value       = join(",", aws_subnet.eks[*].id)
}

output "eks_registry" {
  description = "The ECR registry of the images of the EKS runs, AWS_EKS_REGISTRY in aws-eks."
  value       = local.eks_registry
}
