resource "faraday_connection" "hosted" {
  name = "hosted"
  options = {
    hosted_csv = {}
  }
}

resource "faraday_dataset" "orders" {
  name          = "orders"
  connection_id = faraday_connection.hosted.id
  options = jsonencode({
    type             = "hosted_csv"
    upload_directory = "orders"
  })
  identity_sets = {
    primary = {
      email             = "email"
      person_first_name = "first_name"
      person_last_name  = "last_name"
    }
  }
}
