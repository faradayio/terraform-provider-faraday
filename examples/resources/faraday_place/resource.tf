resource "faraday_place" "territory" {
  name = "territory"
  geojson = jsonencode({
    type = "Polygon"
    coordinates = [
      [[-73.0, 44.4], [-73.1, 44.4], [-73.1, 44.5], [-73.0, 44.5], [-73.0, 44.4]],
    ]
  })
}
