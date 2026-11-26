variable "region" {
  description = "The region of every regional resource of the sandbox and of every CI run."
  type        = string
  default     = "eu-west-1"
}

variable "kms_replica_region" {
  description = "The one other region where a run can use KMS, for the replicas of its multi-Region keys."
  type        = string
  default     = "eu-central-1"
}

variable "github_owner" {
  description = "The owner of the repository whose workflows get the CI role."
  type        = string
  default     = "konsole-is"
}

variable "github_repository" {
  description = "The repository whose workflows get the CI role."
  type        = string
  default     = "camunda-operator-plugins"
}

variable "trusted_user" {
  description = "The GitHub login whose runs get the CI role without an approval. This user also approves every other run."
  type        = string
  default     = "sourcehawk"
}

variable "github_oidc_provider_arn" {
  description = "The ARN of an IAM OIDC provider for token.actions.githubusercontent.com that the account already has. Null creates one. An account holds only one."
  type        = string
  default     = null
}

variable "budget_limit_usd" {
  description = "The monthly cost budget of the account in USD."
  type        = number
  default     = 10
}

variable "alert_emails" {
  description = "The email addresses of the budget alerts. An empty list creates no budget."
  type        = list(string)
  default     = []
}

variable "eks_vpc_cidr" {
  description = "The CIDR block of the VPC of the EKS runs."
  type        = string
  default     = "10.42.0.0/16"
}

variable "eks_instance_types" {
  description = "The EC2 instance types that the node of an EKS run can have. The run uses the first."
  type        = list(string)
  default     = ["t3.xlarge", "t3a.xlarge"]
}

variable "eks_ami_owners" {
  description = "The owners of the node images that an EKS run can start, as ec2:Owner reads them: the alias amazon and the account of the EKS images."
  type        = list(string)
  default     = ["amazon", "602401143452"]
}

variable "eks_plugins_namespace" {
  description = "The namespace of the release aws in the EKS cluster of a run. The role cpo-sandbox-eks-plugins trusts its ServiceAccount aws there."
  type        = string
  default     = "camunda-operator-plugins"
}

variable "daily_budget_limit_usd" {
  description = "The daily cost budget of the account in USD."
  type        = number
  default     = 2
}
