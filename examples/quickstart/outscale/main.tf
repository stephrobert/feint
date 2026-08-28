# The first thirty seconds: one Vm and one public address, on Outscale.
#
# The same shape as examples/quickstart/scaleway in Outscale's own vocabulary: a
# Vm rather than a server, an OMI rather than an image, and a public IP that is
# created and then *linked* rather than attached at creation — which is the one
# thing about this API a Scaleway reader gets wrong first.
#
# `examples/stacks/outscale` is the other thing (#593): two peered Nets, a NAT
# service, route tables, a balancer, machines spread over two subregions. It is
# there to try to break the emulator; this is here to be read.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    outscale = {
      source = "outscale/outscale"
      # The floor is 1.7, the generation that reads its endpoint path from the
      # value (OSC_ENDPOINT_API carries /api/v1) where 1.1.x appends the path
      # itself. A resolution below it would silently change how this example
      # must be pointed at the emulator.
      version = "~> 1.7"
    }
  }
}

# Overridden by `feint up`, which passes the address it started the emulator on
# (iac.vars in feint.yaml).
variable "endpoint" {
  type    = string
  default = "http://127.0.0.1:4599"
}

# Their documentation gives the endpoint as
# `https://api.<region>.outscale.com/api/v1`, so the version segment belongs to
# the value rather than being appended by the provider. Getting it wrong is not
# a warning: the emulator answers 404 and names the missing prefix.
provider "outscale" {
  access_key_id = "AAAAAAAAAAAAAAAAAAAA"
  secret_key_id = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

  api {
    endpoint = "${var.endpoint}/api/v1"
    region   = "eu-west-2"
  }
}

resource "outscale_vm" "quickstart" {
  # A catalogue OMI: ami-00000003 is one of the identifiers the emulator can
  # really boot. An unknown one applies fine with no machine runtime and is
  # refused the moment somebody turns one on, which is a first example teaching
  # a habit that breaks later.
  image_id = "ami-00000003"
  vm_type  = "tinav6.c1r1p2"
}

resource "outscale_public_ip" "quickstart" {}

resource "outscale_public_ip_link" "quickstart" {
  vm_id     = outscale_vm.quickstart.vm_id
  public_ip = outscale_public_ip.quickstart.public_ip
}

output "address" {
  description = "The public address the emulator published for the Vm."
  value       = outscale_public_ip.quickstart.public_ip
}
