resource "faraday_cohort" "customers" {
  name        = "customers"
  stream_name = "orders"
}

resource "faraday_scope" "export" {
  name    = "export"
  preview = true
  population = {
    cohort_ids = [faraday_cohort.customers.id]
  }
  payload = {}
}
