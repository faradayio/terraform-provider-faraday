variable "faraday_api_key" {
  type      = string
  sensitive = true
}

provider "faraday" {
  api_key = var.faraday_api_key
}
