# Conformance fixture: the real Outscale Terraform provider, pointed at the emulator.
#
# The endpoint carries the whole API path. Their documentation gives the shape —
# `endpoint = "https://api.eu-west-2.outscale.com/api/v1"` — so the version
# segment belongs to the value rather than being appended by the provider. The
# top-level `region`, `endpoints` and `insecure` arguments are deprecated in
# favour of this `api` block; using them costs a warning on every command.
#
# Getting that wrong is not a warning, it is a six-minute wait: with the version
# segment missing, the provider retries with backoff until it times out, and the
# failure reads like a slow emulator rather than a misdirected client.

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

# Tags are here because the provider calls CreateTags on almost every resource,
# and because their order is a permanent diff waiting to happen: the emulator
# sorts them, so two reads of an unchanged Net are identical.
resource "outscale_net" "conformance" {
  ip_range = "10.70.0.0/16"

  tags {
    key   = "name"
    value = "feint-conformance"
  }
}

resource "outscale_subnet" "conformance" {
  net_id   = outscale_net.conformance.net_id
  ip_range = "10.70.1.0/24"
}

# A keypair, because a machine nobody can log into proves nothing — and because
# the provider addresses it by KeypairId on destroy while creating it by name.
# The emulator publishes both, and they are the same identity.
resource "outscale_keypair" "conformance" {
  keypair_name = "feint-conformance"
  public_key   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"
}

# A volume, because the provider creates one, reads it back and links it — and
# because nothing else in this repository drives CreateVolume. The oapi-cli suite
# never touches one, which is exactly why the routes were missing.
resource "outscale_volume" "conformance" {
  subregion_name = "eu-west-2a"
  size           = 10
}

resource "outscale_vm" "conformance" {
  image_id     = "ami-12345678"
  vm_type      = "tinav6.c1r1p2"
  subnet_id    = outscale_subnet.conformance.subnet_id
  keypair_name = outscale_keypair.conformance.keypair_name

  tags {
    key   = "name"
    value = "feint-conformance"
  }
}

output "vm_id" {
  value = outscale_vm.conformance.vm_id
}

output "net_id" {
  value = outscale_net.conformance.net_id
}

output "volume_id" {
  value = outscale_volume.conformance.volume_id
}

output "keypair_id" {
  value = outscale_keypair.conformance.keypair_id
}
