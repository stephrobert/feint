# A platform on Exoscale: two private networks, a pool that scales, block
# storage, an elastic IP in front, a load balancer over the pool — and the
# per-id reads no published client makes, driven here through data sources.
#
# ---------------------------------------------------------------------------
# SUSPENDED — no Terraform for Exoscale until upstream #573 is fixed (#525)
# ---------------------------------------------------------------------------
#
# The published Exoscale provider cannot be pointed at a local emulator. It
# builds two clients: one honours EXOSCALE_API_ENDPOINT, the other has
# `.exoscale.com` compiled into its request path. An apply therefore does not
# fail — it **splits**, half against the emulator and half against a paying
# account with whatever credentials the environment holds. Filed upstream as
# exoscale/terraform-provider-exoscale#573.
#
# Since 2026-08-26, nothing runs this stack, and that is the decision rather
# than a gap. A pinned four-line fork used to close the split for a by-hand
# run — what it proved stays dated in docs/limits.md ("The patched provider,
# while upstream decides") — and then #525 measured what every path around
# the fork costs: a `feint down` in this directory, run without the fork's
# dev_overrides, resolved the published 0.70.0 and sent five signed requests
# to api-ch-*.exoscale.com. So today:
#
#   1. `feint up` and `feint down` refuse `iac.engine: terraform` (and
#      opentofu) for Exoscale at the doorstep, before anything starts, and
#      the emulator still refuses the provider by its user agent.
#   2. **This stack is run by nothing.** While it ran by hand it was how the
#      block-storage and instance-pool work of #12 and #232 got a second
#      reader; the exo CLI suites carry that alone now. The Scaleway and
#      Outscale stacks beside it are the ones the pull requests apply.
#
# The *.tf stays what it is — the platform shape this stack asserts — and
# becomes runnable again the day the published provider honours its endpoint
# for both clients.

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

# ---------------------------------------------------------------------------
# The Network Load Balancer in front of the pool (#345). This is the shape the
# one surveyed stack that reaches the family uses — PhilippeChepy/platform
# declares an `exoscale_nlb` and five `exoscale_nlb_service`, each pointing at
# an instance pool with an https health check.
#
# What a plan gets from it here: the configuration round-trips, the service
# names the pool's members as its backends, and not one of them carries a
# health verdict, because nothing in this emulator probes a backend. The
# balancer's own `ip_address` is a TEST-NET-1 address that routes nowhere —
# docs/limits.md says why, and why the internal dataplane the Outscale LBU has
# cannot exist for this family.
# ---------------------------------------------------------------------------

resource "exoscale_nlb" "front" {
  zone        = var.zone
  name        = "platform-front"
  description = "the application tier's entrypoint"
}

resource "exoscale_nlb_service" "app" {
  zone   = var.zone
  nlb_id = exoscale_nlb.front.id
  name   = "app"

  instance_pool_id = exoscale_instance_pool.app.id
  protocol         = "tcp"
  port             = 443
  target_port      = 8080
  strategy         = "round-robin"

  healthcheck {
    mode     = "https"
    port     = 8080
    uri      = "/healthz"
    tls_sni  = "platform.example"
    interval = 10
    timeout  = 5
    retries  = 2
  }
}

# ---------------------------------------------------------------------------
# Three reads no published client ever makes. GET /v2/load-balancer/{id},
# /v2/instance-pool/{id} and /v2/elastic-ip/{id} are served and probed, but
# `exo` resolves each of them by listing and filtering client-side, so the
# per-id read has no caller — docs/routes.md carries each reason. The
# provider's data sources look up by id, which makes this stack those routes'
# first client-shaped reader: an id, a field or a state the emulator invents
# on the per-id door and not on the list door surfaces as a diff between
# these reads and the resources above.
#
# Honest limit, the same one the whole stack carries: this runs through the
# patched fork, which is not the official client, so the `driven` axis of
# coverage/evidence.json does not move. The reads still exercise the routes.
# ---------------------------------------------------------------------------

data "exoscale_nlb" "front" {
  zone = var.zone
  id   = exoscale_nlb.front.id
}

data "exoscale_instance_pool" "app" {
  zone = var.zone
  id   = exoscale_instance_pool.app.id
}

data "exoscale_elastic_ip" "ingress" {
  zone = var.zone
  id   = exoscale_elastic_ip.ingress.id
}

output "ingress_address" {
  value = exoscale_elastic_ip.ingress.ip_address
}

# The per-id doors, read back beside the resources that created them: the two
# spellings must agree, or one of the two doors is answering an invention.
output "front_by_id" {
  value = data.exoscale_nlb.front.name
}

output "pool_size_by_id" {
  value = data.exoscale_instance_pool.app.size
}

output "ingress_address_by_id" {
  value = data.exoscale_elastic_ip.ingress.ip_address
}

output "pool_instances" {
  value = exoscale_instance_pool.app.virtual_machines
}

output "template_id" {
  # Resolved through an explicit visibility filter — the #271 read path.
  value = data.exoscale_template.ubuntu.id
}

output "nlb_address" {
  # TEST-NET-1, and routed nowhere on purpose: see docs/limits.md.
  value = exoscale_nlb.front.ip_address
}
