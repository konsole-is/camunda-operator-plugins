# A run that leaks resources shows as cost before the bill. In a member account
# of an organization, the management account must first enable "Linked account
# access" in its Cost Management preferences.
resource "aws_budgets_budget" "monthly" {
  count = length(var.alert_emails) > 0 ? 1 : 0

  name         = "cpo-sandbox-monthly"
  budget_type  = "COST"
  time_unit    = "MONTHLY"
  limit_amount = tostring(var.budget_limit_usd)
  limit_unit   = "USD"

  notification {
    notification_type          = "ACTUAL"
    comparison_operator        = "GREATER_THAN"
    threshold                  = 50
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = var.alert_emails
  }

  notification {
    notification_type          = "FORECASTED"
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = var.alert_emails
  }
}

# A cluster and a node that every removal of an EKS run missed cost about
# 6.90 USD a day.
resource "aws_budgets_budget" "daily" {
  count = length(var.alert_emails) > 0 ? 1 : 0

  name         = "cpo-sandbox-daily"
  budget_type  = "COST"
  time_unit    = "DAILY"
  limit_amount = tostring(var.daily_budget_limit_usd)
  limit_unit   = "USD"

  notification {
    # AWS updates the cost data of a budget up to three times a day,
    # typically 8 to 12 hours apart.
    notification_type          = "ACTUAL"
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    subscriber_email_addresses = var.alert_emails
  }
}
