# A three-tier platform on Scaleway, with a separate management VPC.
#
# Written the way a real project is written rather than the way a test fixture
# is: two VPCs that do not share a network, a bastion that is the only public
# door, web and application tiers with their own security groups, data disks,
# a golden image cut from a snapshot, block-storage volumes with their own
# snapshots, and addresses booked from IPAM before anything carries them.
#
# It exists to be run against Feint with no cloud account. Everything here is
# ordinary Terraform: if it applies, re-plans empty and destroys, the emulator
# held up under something that looks like production rather than under a test.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    scaleway = {
      source = "scaleway/scaleway"
      # Exact for the same reason the conformance fixture is exact: a floating
      # constraint turned CI red the hour 2.81.0 was published, with no change
      # on this side (#257). 2.81.0 is the pin rather than the 2.80.0 that made
      # it green again — #260 serves the /instance/v2alpha1 routes that release
      # reads private NICs through, and a stack pinned below them would stop
      # exercising what the emulator now claims to serve.
      version = "2.81.0"
    }
  }
}

variable "endpoint" {
  type    = string
  default = "http://127.0.0.1:4599"
}

variable "web_count" {
  type    = number
  default = 2
}

# A typed map rather than a count, because that is what the better surveyed
# stacks do (sergelogvinov/terraform-talos drives every tier from typed maps):
# each worker is named, and carries its own data-disk size. The default applies
# as-is; terraform.tfvars.example shows what an override looks like.
variable "app_servers" {
  type = map(object({
    data_gb = optional(number, 20)
  }))
  default = {
    worker-a = {}
    worker-b = {}
    worker-c = { data_gb = 30 }
  }
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

# ---------------------------------------------------------------------------
# Two VPCs. The workload one and the management one, which is the shape most
# platforms end up with and the one that makes isolation a real question.
# ---------------------------------------------------------------------------

resource "scaleway_vpc" "workload" {
  name = "platform-workload"
  tags = ["platform", "workload"]
}

resource "scaleway_vpc" "management" {
  name = "platform-management"
  tags = ["platform", "management"]
}

resource "scaleway_vpc_private_network" "web" {
  name   = "platform-web"
  vpc_id = scaleway_vpc.workload.id
  ipv4_subnet {
    subnet = "10.30.1.0/24"
  }
}

resource "scaleway_vpc_private_network" "app" {
  name   = "platform-app"
  vpc_id = scaleway_vpc.workload.id
  ipv4_subnet {
    subnet = "10.30.2.0/24"
  }
}

resource "scaleway_vpc_private_network" "admin" {
  name   = "platform-admin"
  vpc_id = scaleway_vpc.management.id
  ipv4_subnet {
    subnet = "10.40.1.0/24"
  }
}

# A route a platform team really writes: reach the management range through the
# admin network.
resource "scaleway_vpc_route" "to_management" {
  vpc_id                     = scaleway_vpc.workload.id
  description                = "management range"
  destination                = "10.40.0.0/16"
  nexthop_private_network_id = scaleway_vpc_private_network.app.id
}

# ---------------------------------------------------------------------------
# Security groups, one per tier, each opening only what that tier needs.
# ---------------------------------------------------------------------------

resource "scaleway_instance_security_group" "bastion" {
  name                    = "platform-bastion"
  inbound_default_policy  = "drop"
  outbound_default_policy = "accept"

  inbound_rule {
    action = "accept"
    port   = 22
  }
}

resource "scaleway_instance_security_group" "web" {
  name                    = "platform-web"
  inbound_default_policy  = "drop"
  outbound_default_policy = "accept"

  inbound_rule {
    action = "accept"
    port   = 443
  }

  inbound_rule {
    action   = "accept"
    port     = 22
    ip_range = "10.40.1.0/24"
  }
}

resource "scaleway_instance_security_group" "app" {
  name                    = "platform-app"
  inbound_default_policy  = "drop"
  outbound_default_policy = "accept"

  inbound_rule {
    action   = "accept"
    port     = 8080
    ip_range = "10.30.1.0/24"
  }
}

# ---------------------------------------------------------------------------
# A golden image: a volume, a snapshot of it, an image cut from the snapshot.
# This is the sequence a platform builds once and boots from everywhere, and it
# is where an emulator that stores a snapshot without a parent falls over.
# ---------------------------------------------------------------------------

resource "scaleway_instance_volume" "golden" {
  name       = "platform-golden-src"
  type       = "b_ssd"
  size_in_gb = 10
}

resource "scaleway_instance_snapshot" "golden" {
  name      = "platform-golden-snap"
  volume_id = scaleway_instance_volume.golden.id
}

resource "scaleway_instance_image" "golden" {
  name           = "platform-golden"
  root_volume_id = scaleway_instance_snapshot.golden.id
  architecture   = "x86_64"
}

# Built, and deliberately not booted from — see the web tier below. The chain
# volume → snapshot → image is what this block exercises; booting the result is
# a different claim, and one the emulator refuses on purpose.

# ---------------------------------------------------------------------------
# The bastion: the only machine with a public address.
# ---------------------------------------------------------------------------

resource "scaleway_instance_ip" "bastion" {}

resource "scaleway_instance_server" "bastion" {
  name              = "platform-bastion"
  type              = "DEV1-S"
  image             = "ubuntu_jammy"
  ip_id             = scaleway_instance_ip.bastion.id
  security_group_id = scaleway_instance_security_group.bastion.id
  tags              = ["platform", "bastion"]

  cloud_init = <<-EOT
    #cloud-config
    package_update: true
    packages:
      - fail2ban
  EOT
}

resource "scaleway_instance_private_nic" "bastion" {
  server_id          = scaleway_instance_server.bastion.id
  private_network_id = scaleway_vpc_private_network.admin.id
}

# ---------------------------------------------------------------------------
# The web tier: addresses booked from IPAM before the NICs carry them, which is
# what a platform does when it wants stable addresses rather than whatever the
# cloud hands out.
# ---------------------------------------------------------------------------

resource "scaleway_ipam_ip" "web" {
  count   = var.web_count
  address = "10.30.1.${10 + count.index}"

  source {
    private_network_id = scaleway_vpc_private_network.web.id
  }

  tags = ["platform", "web"]
}

resource "scaleway_instance_volume" "web_data" {
  count      = var.web_count
  name       = "platform-web-data-${count.index}"
  type       = "b_ssd"
  size_in_gb = 10
}

resource "scaleway_instance_ip" "web" {
  count = var.web_count
}

resource "scaleway_instance_server" "web" {
  count = var.web_count

  name = "platform-web-${count.index}"
  type = "DEV1-S"

  # The catalogue image, not scaleway_instance_image.golden above, and the
  # reason is measured rather than stylistic: this emulator keeps records, not
  # disk contents, so an image the client registered has no bytes to boot. With
  # a machine runtime configured (FEINT_VM=incus and friends) a server created
  # from one is refused at boot and stays `stopped`, which is what the real
  # sequence would have produced here — Terraform then fails the apply with
  # "expected state running but found stopped".
  #
  # That refusal is a decision, not a gap: booting the source's base image
  # instead would silently drop whatever the client baked in, and the
  # golden-image workflow is exactly where that difference is the point.
  # docs/limits.md carries the measurement (#83).
  #
  # So the stack still builds the golden image, and boots from the catalogue.
  # It runs in both modes, which is what an example has to do.
  image                 = "ubuntu_jammy"
  ip_id                 = scaleway_instance_ip.web[count.index].id
  security_group_id     = scaleway_instance_security_group.web.id
  additional_volume_ids = [scaleway_instance_volume.web_data[count.index].id]
  tags                  = ["platform", "web"]

  cloud_init = <<-EOT
    #cloud-config
    package_update: true
    packages:
      - nginx
  EOT
}

resource "scaleway_instance_private_nic" "web" {
  count = var.web_count

  server_id          = scaleway_instance_server.web[count.index].id
  private_network_id = scaleway_vpc_private_network.web.id
  ipam_ip_ids        = [scaleway_ipam_ip.web[count.index].id]
}

# ---------------------------------------------------------------------------
# The application tier: no public address at all, and block-storage volumes
# rather than instance ones, which is the other volume product and a different
# API entirely.
# ---------------------------------------------------------------------------

resource "scaleway_block_volume" "app_data" {
  for_each   = var.app_servers
  name       = "platform-app-data-${each.key}"
  iops       = 5000
  size_in_gb = each.value.data_gb
  tags       = ["platform", "app"]
}

resource "scaleway_block_snapshot" "app_data" {
  for_each  = var.app_servers
  name      = "platform-app-snap-${each.key}"
  volume_id = scaleway_block_volume.app_data[each.key].id
  tags      = ["platform", "app"]
}

resource "scaleway_instance_server" "app" {
  for_each = var.app_servers

  name              = "platform-app-${each.key}"
  type              = "DEV1-S"
  image             = "ubuntu_jammy"
  security_group_id = scaleway_instance_security_group.app.id
  tags              = ["platform", "app"]

  # No ip_id at all: this tier is unreachable from outside, which is the point.
}

resource "scaleway_instance_private_nic" "app" {
  for_each = var.app_servers

  server_id          = scaleway_instance_server.app[each.key].id
  private_network_id = scaleway_vpc_private_network.app.id
}

# ---------------------------------------------------------------------------
# What a reader would check afterwards.
# ---------------------------------------------------------------------------

output "bastion_ip" {
  value = scaleway_instance_ip.bastion.address
}

output "web_addresses" {
  value = scaleway_ipam_ip.web[*].address
}

output "golden_image_id" {
  value = scaleway_instance_image.golden.id
}

# The /64 each private network carries beside its declared IPv4 subnet.
# sergelogvinov/terraform-talos consumes exactly this expression to build its
# machine configs, and it is the expression that found #270: against an
# emulator publishing no IPv6 subnet, one() yields null, this output dies on
# apply — and a stack already applied cannot even destroy. Keeping it here
# keeps that a permanent regression.
output "ipv6_subnets" {
  value = {
    web   = one(scaleway_vpc_private_network.web.ipv6_subnets).subnet
    app   = one(scaleway_vpc_private_network.app.ipv6_subnets).subnet
    admin = one(scaleway_vpc_private_network.admin.ipv6_subnets).subnet
  }
}

output "machines" {
  value = length(scaleway_instance_server.web) + length(scaleway_instance_server.app) + 1
}
