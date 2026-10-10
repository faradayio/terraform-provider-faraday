resource "faraday_cohort" "customers" {
  name        = "customers"
  stream_name = "orders"
}
