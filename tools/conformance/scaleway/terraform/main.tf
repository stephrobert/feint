# Conformance fixture: the real Scaleway Terraform provider, pointed at the emulator.
#
# api_url is a first-class provider attribute, so every product except Object Storage can be
# redirected without touching DNS. Object Storage hardcodes https://s3.<region>.scw.cloud in the
# provider source, which is why it is out of scope here (see docs/limits.md).

terraform {
  required_version = ">= 1.7.0"

  required_providers {
    scaleway = {
      source  = "scaleway/scaleway"
      version = "~> 2.79"
    }
  }
}

variable "endpoint" {
  type        = string
  description = "Base URL of the running feint emulator."
  default     = "http://127.0.0.1:4599"
}

provider "scaleway" {
  api_url         = var.endpoint
  access_key      = "SCWXXXXXXXXXXXXXXXXX"
  secret_key      = "11111111-1111-1111-1111-111111111111"
  project_id      = "11111111-1111-1111-1111-111111111111"
  organization_id = "11111111-1111-1111-1111-111111111111"
  region          = "fr-par"
  zone            = "fr-par-1"
}

# Inline rules are how the provider models a security group: it sends the whole list through
# SetSecurityGroupRules on every change, so the emulator must answer with the resulting state and
# not with what it was asked. A rule that comes back in another order, or without the id it was
# given, is a permanent diff.
resource "scaleway_instance_security_group" "conformance" {
  name                    = "conformance-tf"
  description             = "feint conformance"
  zone                    = "fr-par-1"
  inbound_default_policy  = "drop"
  outbound_default_policy = "accept"

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 22
    ip_range = "10.0.0.0/8"
  }

  inbound_rule {
    action   = "accept"
    protocol = "TCP"
    port     = 443
  }

  outbound_rule {
    action   = "drop"
    protocol = "UDP"
    port     = 53
  }
}

# A private network with an explicit block. The emulator validates it rather than storing it: the
# block is checked against its siblings for overlap, and it becomes a real bridge carrying that
# range, so the address the provider reads back below belongs to it.
resource "scaleway_vpc_private_network" "conformance" {
  name   = "conformance-tf"
  region = "fr-par"

  ipv4_subnet {
    subnet = "10.182.0.0/24"
  }
}

# The volume the server carries besides its root. This resource is here because
# its absence cost two audits: an attached volume answering a state VolumeState
# does not declare made `apply` hang for five minutes on WaitForVolume, and a
# terminate that did not detach made `destroy` fail with "volume is still
# attached to a server" on every retry. Both were invisible to unit tests, which
# read JSON, and to this fixture, which had no volume in it.
resource "scaleway_instance_volume" "conformance" {
  name       = "conformance-tf-data"
  type       = "b_ssd"
  size_in_gb = 10
  zone       = "fr-par-1"
}

resource "scaleway_instance_server" "conformance" {
  name = "conformance-tf"
  type = "DEV1-S"
  zone = "fr-par-1"

  # The path both defects lived on: the provider attaches it at create and
  # detaches it at destroy, through terminate rather than delete.
  additional_volume_ids = [scaleway_instance_volume.conformance.id]

  # The provider requires an image and resolves the label through the marketplace, which the
  # emulator serves from its fixed catalogue (internal/providers/scaleway/catalog.go). It returns a
  # stable image id on purpose: one that changed between runs would be a permanent diff.
  image             = "ubuntu_jammy"
  tags              = ["feint", "conformance"]
  security_group_id = scaleway_instance_security_group.conformance.id
}

# The attachment, which is where the addressing plan meets the machine: the address comes from the
# private network's own block, and the provider reads it back on the NIC.
resource "scaleway_instance_private_nic" "conformance" {
  server_id          = scaleway_instance_server.conformance.id
  private_network_id = scaleway_vpc_private_network.conformance.id
  zone               = "fr-par-1"
}

output "server_id" {
  value = scaleway_instance_server.conformance.id
}

output "private_network_id" {
  value = scaleway_vpc_private_network.conformance.id
}

output "security_group_id" {
  value = scaleway_instance_security_group.conformance.id
}

output "volume_id" {
  value = scaleway_instance_volume.conformance.id
}
