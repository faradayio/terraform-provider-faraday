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

resource "faraday_target" "csv" {
  name     = "csv"
  scope_id = faraday_scope.export.id
  representation = {
    identified = {
      aggregate = "person"
    }
  }
  options = {
    hosted_csv = {}
  }
}
