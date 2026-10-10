resource "faraday_cohort" "customers" {
  name        = "customers"
  stream_name = "orders"
}

resource "faraday_outcome" "repeat_purchase" {
  name                 = "repeat_purchase"
  attainment_cohort_id = faraday_cohort.customers.id
}
