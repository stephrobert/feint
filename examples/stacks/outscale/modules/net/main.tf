# A Net and its subnets, as a reusable module.
#
# The shape is chimere-eu/ztiac's (surveyed.md): every substantial third-party
# Outscale stack the survey applied wraps "a Net plus its subnets spread over
# subregions" in a module and instantiates it per environment. The house stack
# used to inline all of it, which meant the module resolution path — a child
# module with its own required_providers, for_each over typed objects — was
# never exercised against the emulator.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    # Without this block Terraform resolves the unqualified "outscale" of a
    # child module to hashicorp/outscale, which does not exist. kalisio/kaabah
    # died exactly there (surveyed.md, annex).
    outscale = {
      source = "outscale/outscale"
    }
  }
}

variable "name" {
  type = string
}

variable "ip_range" {
  type = string
}

variable "subnets" {
  # subregion_name stays optional: a subnet that does not name one takes the
  # region default, and both paths must read back what was written (#268, #269).
  type = map(object({
    ip_range       = string
    subregion_name = optional(string)
  }))
}

resource "outscale_net" "this" {
  ip_range = var.ip_range

  tags {
    key   = "Name"
    value = var.name
  }
}

resource "outscale_subnet" "this" {
  for_each = var.subnets

  net_id         = outscale_net.this.net_id
  ip_range       = each.value.ip_range
  subregion_name = each.value.subregion_name

  tags {
    key   = "Name"
    value = "${var.name}-${each.key}"
  }
}

output "net_id" {
  value = outscale_net.this.net_id
}

output "subnet_ids" {
  value = { for name, subnet in outscale_subnet.this : name => subnet.subnet_id }
}
