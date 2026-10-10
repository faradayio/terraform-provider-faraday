resource "faraday_cohort" "customers" {
  name        = "customers"
  stream_name = "orders"
}

resource "faraday_outcome" "repeat_purchase" {
  name                 = "repeat_purchase"
  attainment_cohort_id = faraday_cohort.customers.id
}

resource "faraday_scope" "export" {
  name    = "export"
  preview = true
  population = {
    cohort_ids = [faraday_cohort.customers.id]
  }
  payload = {}
}

resource "faraday_market_opportunity_analysis" "market" {
  name       = "market"
  scope_id   = faraday_scope.export.id
  outcome_id = faraday_outcome.repeat_purchase.id
}
