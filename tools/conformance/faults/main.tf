# The smallest Outscale stack that reaches ReadNets, so a 503 armed on that one
# operation lands inside a real `terraform apply`.
#
# Deliberately not the conformance fixture next door: that one applies fifteen
# resources, and a suite about retry behaviour wants one call, one rule, one
# assertion. The provider block is copied from it because the endpoint shape is
# the part that is easy to get wrong — the version segment belongs to the value,
# and without it the provider retries with backoff until it times out, which
# reads exactly like the fault this suite is injecting.

terraform {
  required_version = ">= 1.7.0"

  required_providers {
    outscale = {
      source  = "outscale/outscale"
      version = "~> 1.7"
    }
  }
}

variable "endpoint" {
  type        = string
  description = "Base URL of the running feint emulator, without the API path."
  default     = "http://127.0.0.1:4599"
}

provider "outscale" {
  access_key_id = "AAAAAAAAAAAAAAAAAAAA"
  secret_key_id = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

  api {
    endpoint = "${var.endpoint}/api/v1"
    region   = "eu-west-2"
  }
}

resource "outscale_net" "faulted" {
  ip_range = "10.71.0.0/16"

  tags {
    key   = "name"
    value = "feint-faults"
  }
}

output "net_id" {
  value = outscale_net.faulted.net_id
}
