# Terraform Provider for Faraday

Manage Faraday resources with Terraform. The provider is published at `registry.terraform.io/faradayio/faraday`.

## Usage

```hcl
terraform {
  required_providers {
    faraday = {
      source  = "faradayio/faraday"
      version = "0.2.0"
    }
  }
}

variable "faraday_api_key" {
  type      = string
  sensitive = true
}

provider "faraday" {
  api_key = var.faraday_api_key
}
```

`api_key` is required. `base_url` defaults to `https://api.faraday.ai/v1`.

## Deleting a resource archives it

`terraform destroy` archives the resource. It does not delete it. An archived resource stays in Faraday and drops out of Terraform state. Terraform will not archive a resource that still has dependents. Remove those first.

A resource that never built cannot be archived. Destroy deletes that one resource, because nothing was built from it.

## This repository is generated

The provider is generated from Faraday's API and published from a private repository. Issues are welcome. Pull requests are not accepted, because the next release replaces the contents of this repository.
