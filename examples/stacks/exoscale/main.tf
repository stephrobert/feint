# A platform on Exoscale: two private networks, a pool that scales, block
# storage, and an elastic IP in front.
#
# ---------------------------------------------------------------------------
# READ THIS BEFORE RUNNING IT — this stack needs a patched provider
# ---------------------------------------------------------------------------
#
# The published Exoscale provider cannot be pointed at a local emulator. It
# builds two clients: one honours EXOSCALE_API_ENDPOINT, the other has
# `.exoscale.com` compiled into its request path. An apply therefore does not
# fail — it **splits**, half against the emulator and half against a paying
# account with whatever credentials the environment holds. Feint refuses that
# client by its user agent rather than serving half of it.
#
# Filed upstream as exoscale/terraform-provider-exoscale#573. Until it lands, a
# four-line fork carries the fix, and docs/limits.md pins the commit and the
# `dev_overrides` recipe:
#
#     docs/limits.md → "The patched provider, while upstream decides"
#
# Two consequences, both deliberate:
#
#   1. `FEINT_EXOSCALE_ALLOW_TERRAFORM=1 feint serve` is required. The refusal is
#      named rather than hidden, because a guard with no way past it gets worked
#      around by copying the emulator, which teaches nobody anything.
#   2. **This stack is not run by CI.** No gate here clones a third-party
#      repository — that would put somebody else's availability in this project's
#      pipeline — and a client this project patched is not the official client,
#      so it could not count towards conformance anyway. The Scaleway and
#      Outscale stacks beside it are the ones the pull requests apply.
#
# What it is worth in spite of that: it exercises the Exoscale pack through the
# client a user would actually reach for, and that is how the block-storage and
# instance-pool work of #12 and #232 gets a second reader.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    exoscale = {
      source = "exoscale/exoscale"
    }
  }
}

variable "zone" {
  type    = string
  default = "ch-dk-2"
}

variable "pool_size" {
  type    = number
  default = 2
}

# No endpoint attribute: the provider has none. It reads EXOSCALE_API_ENDPOINT,
# which `feint env exoscale` prints — and which only the patched build honours
# for both of its clients.
provider "exoscale" {
  key    = "EXOxxxxxxxxxxxxxxxxxxxx"
  secret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
}

# ---------------------------------------------------------------------------
# The key every machine boots with. The CLI registers one on its own before a
# create; a configuration has to say so.
# ---------------------------------------------------------------------------

resource "exoscale_ssh_key" "platform" {
  name       = "platform"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD platform@example"
}

# ---------------------------------------------------------------------------
# Two managed private networks. The range is declared, and the emulator hands
# out leases from it rather than from whatever it felt like — which is the
# assertion the network suite makes against a real runtime.
# ---------------------------------------------------------------------------

resource "exoscale_private_network" "front" {
  zone        = var.zone
  name        = "platform-front"
  description = "web tier"

  start_ip = "10.90.1.20"
  end_ip   = "10.90.1.200"
  netmask  = "255.255.255.0"
}

resource "exoscale_private_network" "back" {
  zone        = var.zone
  name        = "platform-back"
  description = "application tier"

  start_ip = "10.90.2.20"
  end_ip   = "10.90.2.200"
  netmask  = "255.255.255.0"
}

# ---------------------------------------------------------------------------
# Security groups, one per tier, each opening only what that tier needs.
# ---------------------------------------------------------------------------

resource "exoscale_security_group" "web" {
  name        = "platform-web"
  description = "web tier"
}

resource "exoscale_security_group_rule" "web_https" {
  security_group_id = exoscale_security_group.web.id
  type              = "INGRESS"
  protocol          = "TCP"
  cidr              = "0.0.0.0/0"
  start_port        = 443
  end_port          = 443
}

resource "exoscale_security_group" "app" {
  name        = "platform-app"
  description = "application tier"
}

# The rule a platform really writes: the application tier accepts the web tier
# and nobody else, named by group rather than by address.
resource "exoscale_security_group_rule" "app_from_web" {
  security_group_id      = exoscale_security_group.app.id
  type                   = "INGRESS"
  protocol               = "TCP"
  user_security_group_id = exoscale_security_group.web.id
  start_port             = 8080
  end_port               = 8080
}

resource "exoscale_anti_affinity_group" "app" {
  name        = "platform-app"
  description = "keep the application tier apart"
}

# ---------------------------------------------------------------------------
# The machines: one web instance holding the public address, and a pool for the
# application tier — which is the shape a pool exists for.
# ---------------------------------------------------------------------------

# The visibility filter is written out rather than left to its default.
# appuio/terraform-openshift4-exoscale resolves every template through this
# parameter, and its reads produced the #271 transcript: a filter the API
# document declares and the emulator dropped, answering the public catalogue
# to a private query. The declared parameter on this read keeps the served
# half of that fix under a real client; the private half — register a
# template, list it under `--visibility private` — needs a register call the
# pinned provider fork (0.70-based) has no resource for, so it lives in the
# exo CLI conformance suite instead.
data "exoscale_template" "ubuntu" {
  zone       = var.zone
  name       = "Linux Ubuntu 24.04 LTS 64-bit"
  visibility = "public"
}

resource "exoscale_elastic_ip" "ingress" {
  zone        = var.zone
  description = "platform ingress"

  # A managed EIP rather than a bare one: PhilippeChepy/terraform-exoscale-vault
  # — the only surveyed third-party stack that came out entirely green — fronts
  # its cluster with exactly this healthcheck shape. The block must read back
  # as sent or the second plan never converges.
  healthcheck {
    mode         = "tcp"
    port         = 443
    interval     = 10
    timeout      = 5
    strikes_ok   = 2
    strikes_fail = 3
  }
}

# ---------------------------------------------------------------------------
# A persistent data volume, attached to the web instance and snapshotted —
# the block-storage product, which is a different API from the instance disk.
# HealsCodes/ephemeral-devbox is built around exactly this motif (a machine
# that comes and goes, a data volume that survives it, a snapshot rotation);
# it drove the chain on the neighbouring provider, and this is the Terraform
# reader of the block-storage work of #12 and #232 that the exo CLI suite
# otherwise exercises alone.
# ---------------------------------------------------------------------------

resource "exoscale_block_storage_volume" "web_data" {
  zone = var.zone
  name = "platform-web-data"
  size = 20

  labels = {
    tier = "web"
  }
}

resource "exoscale_block_storage_volume_snapshot" "web_data" {
  zone = var.zone
  name = "platform-web-snap"

  volume = {
    id = exoscale_block_storage_volume.web_data.id
  }
}

resource "exoscale_compute_instance" "web" {
  zone               = var.zone
  name               = "platform-web"
  template_id        = data.exoscale_template.ubuntu.id
  type               = "standard.tiny"
  disk_size          = 10
  ssh_key            = exoscale_ssh_key.platform.name
  security_group_ids = [exoscale_security_group.web.id]
  elastic_ip_ids     = [exoscale_elastic_ip.ingress.id]

  block_storage_volume_ids = [exoscale_block_storage_volume.web_data.id]

  network_interface {
    network_id = exoscale_private_network.front.id
  }

  user_data = <<-EOT
    #cloud-config
    package_update: true
    packages:
      - nginx
  EOT
}

resource "exoscale_instance_pool" "app" {
  zone               = var.zone
  name               = "platform-app"
  description        = "application tier"
  template_id        = data.exoscale_template.ubuntu.id
  instance_type      = "standard.tiny"
  size               = var.pool_size
  disk_size          = 10
  key_pair           = exoscale_ssh_key.platform.name
  instance_prefix    = "platform-app"
  security_group_ids = [exoscale_security_group.app.id]

  affinity_group_ids = [exoscale_anti_affinity_group.app.id]

  network_ids = [exoscale_private_network.back.id]
}

output "ingress_address" {
  value = exoscale_elastic_ip.ingress.ip_address
}

output "pool_instances" {
  value = exoscale_instance_pool.app.virtual_machines
}

output "template_id" {
  # Resolved through an explicit visibility filter — the #271 read path.
  value = data.exoscale_template.ubuntu.id
}
