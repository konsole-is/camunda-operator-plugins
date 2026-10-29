variable "region" {
  description = "The region of every regional resource of the sandbox and of every CI run."
  type        = string
  default     = "eu-west-1"
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
