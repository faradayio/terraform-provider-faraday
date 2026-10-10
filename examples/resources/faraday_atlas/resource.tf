resource "faraday_atlas" "stores" {
  name = "stores"
  options = {
    hosted_csv = {
      upload_directory = "stores"
    }
  }
  output_to_locations = {
    name                    = "store_name"
    reference_key           = "store_id"
    house_number_and_street = ["street"]
    city                    = "city"
    state                   = "state"
    postcode                = "postcode"
  }
}
