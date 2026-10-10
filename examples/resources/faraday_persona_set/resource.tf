resource "faraday_cohort" "customers" {
  name        = "customers"
  stream_name = "orders"
}

resource "faraday_persona_set" "personas" {
  name      = "personas"
  cohort_id = faraday_cohort.customers.id
}
