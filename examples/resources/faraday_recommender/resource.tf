resource "faraday_recommender" "next_product" {
  name                 = "next_product"
  stream_name          = "orders"
  stream_property_name = "product"
}
