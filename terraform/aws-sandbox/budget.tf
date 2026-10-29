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
