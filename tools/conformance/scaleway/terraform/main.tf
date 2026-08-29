# Conformance fixture: the real Scaleway Terraform provider, pointed at the emulator.
#
# api_url is a first-class provider attribute, so every product except Object Storage can be
# redirected without touching DNS. Object Storage hardcodes https://s3.<region>.scw.cloud in the
# provider source, which is why it is out of scope here (see docs/limits.md).

terraform {
  required_version = ">= 1.7.0"

  required_providers {
    scaleway = {
      source = "scaleway/scaleway"
      # Exact, not `~>`: v2.81.0 was published on 2026-08-17 and reads private
      # NICs through /instance/v2alpha1/private-network-interfaces, so a float
      # turned every Terraform leg red the hour it shipped, with no repository
      # change (#257). Same doctrine the workflow already applies to the
      # Terraform binary itself — a suite whose client changes under it reports
      # a difference nobody made.
      #
      # And the pin is 2.81.0 rather than the 2.80.0 that made CI green again,
      # because that is what the comment this replaces asked for: "moving this
      # pin is the proof that a newer provider works". #260 serves the five
      # v2alpha1 operations, so 2.81.0 is the client that exercises them.
      #
      # Pinning 2.80.0 here would have been worse than a float: coverage/
      # evidence.json records those operations as driven by a real client, and
      # a suite running a client that never calls them would make that true on
      # the machine that regenerated the artefact and false on the runner. An
      # artefact claiming "a real client proved this" has to mean the client CI
      # actually runs.
      version = "2.81.0"
    }
  }
}

variable "endpoint" {
  type        = string
  description = "Base URL of the running feint emulator."
  default     = "http://127.0.0.1:4599"
}

# The value the second apply changes, and the only reason this fixture applies
# twice. Create-and-destroy was how every edit path in this repository stayed
# unproven: `block/v1` shipped with GetVolume and DeleteVolume alone, and the
# three instance renames were driven for the first time nine months after they
# were mounted (#174). A tag is the cheapest attribute a provider will PATCH
# rather than replace, so one changed tag is one PATCH per resource that
# supports one.
variable "phase" {
  type        = string
  description = "Marks which apply this is: the second one changes it, so the provider issues its update calls."
  default     = "one"
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

# The data source every third-party VPC stack evaluates before any resource, and the wall of #372:
# it answered 501 here, so the two published modules that walk the VPC surface died in two exchanges
# without reaching a single VPC path. It is first in this file because that is where it runs — a
# data source with no dependency is evaluated ahead of the graph, which is exactly what made one
# unserved route unreachable for a whole category of stack.
#
# The provider's own read (DataSourceAccountProjectRead) calls ListProjects when a name is given and
# then always GetProject on the id it resolved, so this one block drives both routes.
data "scaleway_account_project" "conformance" {
  name            = "default"
  organization_id = "11111111-1111-1111-1111-111111111111"
}

# The same data source on a project that is NOT the default one (#572), which is
# what the issue was about: the emulator held one project, so FindExact failed
# for every platform team whose project name is their own. The emulator this
# suite drives is started with `--projects default,platform-prod`, and this block
# is the only thing that proves the provider's own resolution walks a declared
# catalogue — the Go tests drive the routes, never the provider that filters
# their answer.
data "scaleway_account_project" "declared" {
  name            = "platform-prod"
  organization_id = "11111111-1111-1111-1111-111111111111"
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

# An explicit VPC, because the routes below need one the fixture owns: the API refuses to delete a
# default VPC, so a destroy could never take one down.
resource "scaleway_vpc" "conformance" {
  name           = "conformance-tf"
  region         = "fr-par"
  enable_routing = true
}

# A private network with an explicit block. The emulator validates it rather than storing it: the
# block is checked against its siblings for overlap, and it becomes a real bridge carrying that
# range, so the address the provider reads back below belongs to it.
resource "scaleway_vpc_private_network" "conformance" {
  name   = "conformance-tf"
  region = "fr-par"
  vpc_id = scaleway_vpc.conformance.id

  ipv4_subnet {
    subnet = "10.182.0.0/24"
  }
}

# An address booked before anything carries it: the scaleway_ipam_ip lifecycle
# (BookIP, GetIP, UpdateIP, ReleaseIP) failed on its first call before SW-4.
# The provider reads it back by matching source.subnet_id against the network's
# own subnets, so this line also proves the two doors publish the same subnet.
resource "scaleway_ipam_ip" "conformance" {
  address = "10.182.0.10"
  source {
    private_network_id = scaleway_vpc_private_network.conformance.id
  }
  tags = ["feint", "conformance"]
}

# The VPC's Network ACL, served since #343 and declined before it.
#
# It is here rather than in the CLI suite alone because the two operations
# behind it are a PUT and a GET on one path — SetACL and GetACL — and the only
# way a permanent diff shows itself is a client that plans twice. The provider
# reads the whole rule set back on every refresh and compares it field by field,
# so a rule this emulator reorders, retypes or drops surfaces here as a second
# plan that is not empty, and nowhere else.
#
# `phase` moves the description on the second apply, which is what makes the
# update a real PUT rather than a create: the resource has no PATCH, so an
# emulator that stored the first rule set and ignored the second would answer
# the old description and the plan would never converge.
resource "scaleway_vpc_acl" "conformance" {
  vpc_id         = scaleway_vpc.conformance.id
  is_ipv6        = false
  default_policy = "drop"

  rules {
    protocol      = "TCP"
    source        = "0.0.0.0/0"
    src_port_low  = 0
    src_port_high = 0
    destination   = "10.182.0.0/24"
    dst_port_low  = 443
    dst_port_high = 443
    action        = "accept"
    description   = "https, phase ${var.phase}"
  }

  rules {
    protocol      = "UDP"
    source        = "10.182.0.0/24"
    src_port_low  = 0
    src_port_high = 0
    destination   = "0.0.0.0/0"
    dst_port_low  = 53
    dst_port_high = 53
    action        = "accept"
    description   = "dns"
  }
}

# A route through the VPC, managed by the client: create, read back, destroy.
# The emulator stores it as a record and says so in docs/limits.md; what the
# fixture proves is that the provider's CRUD round-trips without a diff.
resource "scaleway_vpc_route" "conformance" {
  vpc_id                     = scaleway_vpc.conformance.id
  description                = "feint conformance route"
  destination                = "192.168.42.0/24"
  nexthop_private_network_id = scaleway_vpc_private_network.conformance.id
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

# Block Storage as a product a client declares, rather than as the fallback the
# server's root volume lands on.
#
# `scw` cannot drive this: every one of its block commands is pinned to
# block/v1alpha1, and the Terraform provider is pinned to v1 (measured, both
# ways). So the alpha was fully driven and v1 had GetVolume and DeleteVolume to
# its name — the two calls the root volume happens to make — while the nine
# operations a client uses to manage a volume of its own had never been driven
# by anything (#174).
resource "scaleway_block_volume" "conformance" {
  name       = "conformance-tf-block"
  iops       = 5000
  size_in_gb = 10
  zone       = "fr-par-1"
  tags       = ["feint", "conformance", var.phase]
}

# A snapshot of it, which is the second half of the product: the provider reads
# the snapshot back by id right after creating it, and refuses to finish while
# the state is not one it knows.
resource "scaleway_block_snapshot" "conformance" {
  name      = "conformance-tf-block-snap"
  volume_id = scaleway_block_volume.conformance.id
  zone      = "fr-par-1"
  tags      = ["feint", "conformance", var.phase]
}

# Reading by name rather than by id, which is a different call: the provider
# lists and filters, so these two lines are what drive ListVolumes and
# ListSnapshots. They also assert something a Get cannot — that a volume and a
# snapshot are findable by the name the client gave them, which is how a human
# looks for one.
data "scaleway_block_volume" "by_name" {
  name = scaleway_block_volume.conformance.name
  zone = "fr-par-1"
}

data "scaleway_block_snapshot" "by_name" {
  name = scaleway_block_snapshot.conformance.name
  zone = "fr-par-1"
}

# Placement groups (#285): the record without the effect, stated in
# docs/limits.md. At this provider pin the resource's CRUD runs through
# instance/v2alpha1 while policy_mode is written and policy_respected read
# through instance/v1 on the same ID — one apply exercises both doors, which is
# the mixed-halves shape that broke the NIC family on 2.81.0's release day.
# The surveyed terraform-talos stack (#262) is the client this unblocks: its
# controlplane and web tiers each create one of these and attach servers.
resource "scaleway_instance_placement_group" "conformance" {
  name        = "conformance-pg"
  policy_type = "max_availability"
  policy_mode = "enforced"
  zone        = "fr-par-1"
  # The phase tag is what drives instance/v2alpha1/API.UpdatePlacementGroup.
  # Provider 2.81.0 PATCHes this door for name, policy type and tags; the
  # fixture created the group and destroyed it without ever editing it, so the
  # route was mounted and unreachable — the route carried the reason in
  # Route.Undriven, and the reason named this exact fixture. One changed tag is
  # the cheapest edit the provider will PATCH rather than replace.
  tags = ["conformance", var.phase]
}

# The name lookup of the provider's data source reads the v2alpha1 list, the
# one operation of the family no resource lifecycle touches.
data "scaleway_instance_placement_group" "by_name" {
  name       = scaleway_instance_placement_group.conformance.name
  zone       = "fr-par-1"
  depends_on = [scaleway_instance_placement_group.conformance]
}

# Two reserved addresses the server below names in the opposite of their
# creation order — the sergelogvinov/terraform-talos ip_ids pattern (#320).
# Server.public_ips is a list the provider stores index by index, so an
# emulator answering it in store order made every such stack re-plan the same
# two-way swap for ever; the apply path is set-based UpdateIP calls and cannot
# reorder, so the diff never converged. The depends_on forces the creation
# order, so a regression is a deterministic second-plan diff rather than a
# coin-flip on two same-second timestamps.
resource "scaleway_instance_ip" "ordered_first" {
  zone = "fr-par-1"
}

resource "scaleway_instance_ip" "ordered_second" {
  zone       = "fr-par-1"
  depends_on = [scaleway_instance_ip.ordered_first]
}

resource "scaleway_instance_server" "conformance" {
  name = "conformance-tf"
  type = "DEV1-S"
  zone = "fr-par-1"

  # The membership travels on the server (CreateServer's placement_group), and
  # the read-back is the embedded object the provider branches on — a server
  # view without it would detach the group at every refresh.
  placement_group_id = scaleway_instance_placement_group.conformance.id

  # Named against the store: the second-created address first (#320).
  ip_ids = [scaleway_instance_ip.ordered_second.id, scaleway_instance_ip.ordered_first.id]

  # The path both defects lived on: the provider attaches it at create and
  # detaches it at destroy, through terminate rather than delete.
  additional_volume_ids = [scaleway_instance_volume.conformance.id]

  # The provider requires an image and resolves the label through the marketplace, which the
  # emulator serves from its fixed catalogue (internal/providers/scaleway/catalog.go). It returns a
  # stable image id on purpose: one that changed between runs would be a permanent diff.
  image             = "ubuntu_jammy"
  tags              = ["feint", "conformance"]
  security_group_id = scaleway_instance_security_group.conformance.id

  # The one input this fixture used to avoid, and the reason it was green while
  # `root_volume` had no usable value at all (#8, reported by @vde-dis).
  #
  # b_ssd will not plan from provider 2.79 on ("b_ssd volumes are not supported
  # anymore"), and sbs_volume sent the provider to
  # GET /block/v1/zones/fr-par-1/volumes/<id>, which nothing served: the apply
  # died on "waiting for Volume failed: http error 404 Not Found". So the way
  # through was to omit the block, which is what this fixture did — a test that
  # avoids the one input that breaks is a test that cannot fail.
  #
  # SW-3 serves block/v1 and honours the type, so the block belongs here now.
  # This is the line that proves GetUnknownVolume's fallback works end to end,
  # through the real provider rather than through our reading of it.
  root_volume {
    volume_type = "sbs_volume"
    size_in_gb  = 20
  }
}

# The attachment, which is where the addressing plan meets the machine: the address is the one
# booked above, carried into the NIC as ipam_ip_ids, and the provider reads it back through IPAM.
# On destroy the order matters and the emulator enforces it: the NIC detaches the booked address
# (it survives), then ReleaseIP frees it — a booked address deleted with its NIC would make this
# destroy fail on the scaleway_ipam_ip.
resource "scaleway_instance_private_nic" "conformance" {
  server_id          = scaleway_instance_server.conformance.id
  private_network_id = scaleway_vpc_private_network.conformance.id
  ipam_ip_ids        = [scaleway_ipam_ip.conformance.id]
  zone               = "fr-par-1"
  # The phase tag is what drives instance/v2alpha1/API.UpdatePrivateNetworkInterface,
  # the PATCH door provider 2.81.0 uses for an interface's tags. Same story as the
  # placement group above: mounted on 2026-08-17 so that the first apply editing a
  # tag would not meet a 501, then never edited by this fixture.
  tags = ["conformance", var.phase]
}

# The exact expression that killed the talos stack (#270): upstream always
# allocates exactly one IPv6 /64 per Private Network, so one() is the ordinary
# way to consume it — and against an emulator publishing no IPv6 subnet, one()
# yields null, this output dies on apply, and a stack already applied cannot
# even destroy. Keeping the expression here makes that a permanent regression.
output "ipv6_subnet" {
  value = one(scaleway_vpc_private_network.conformance.ipv6_subnets).subnet
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

output "ipam_ip_id" {
  value = scaleway_ipam_ip.conformance.id
}

# The two reserved addresses in the order the server named them, so the suite
# can ask the emulator — not the state file — whether public_ips serves the
# client's order (#320).
output "ordered_ip_ids" {
  value = [scaleway_instance_ip.ordered_second.id, scaleway_instance_ip.ordered_first.id]
}

output "route_id" {
  value = scaleway_vpc_route.conformance.id
}

# The VPC's own id, so the suite can ask the emulator for the ACL directly
# rather than trusting the state file to describe what the API holds.
output "vpc_id" {
  value = scaleway_vpc.conformance.id
}

output "block_volume_id" {
  value = scaleway_block_volume.conformance.id
}

output "block_snapshot_id" {
  value = scaleway_block_snapshot.conformance.id
}

# What the data sources found, so the suite asserts on the answer of the list
# call rather than on the state file agreeing with itself.
output "block_volume_id_by_name" {
  value = data.scaleway_block_volume.by_name.id
}

output "block_snapshot_id_by_name" {
  value = data.scaleway_block_snapshot.by_name.id
}

# The project the data source resolved by name, so the suite asserts on what the
# two account/v3 routes answered rather than on the state agreeing with itself.
output "account_project_id" {
  value = data.scaleway_account_project.conformance.project_id
}

output "account_project_name" {
  value = data.scaleway_account_project.conformance.name
}

# The declared project the provider resolved by name (#572). Its id is asserted
# to differ from the default one's: a catalogue answering the same identifier
# under two names would pass a name-only check while holding one project.
output "declared_project_id" {
  value = data.scaleway_account_project.declared.project_id
}

output "declared_project_name" {
  value = data.scaleway_account_project.declared.name
}

# The Load Balancer chain (#282), in the shape the surveyed stacks wrote it:
# kubic's standalone lb_ip, talos's balancer on a Private Network with a
# tcp backend, an HTTPS health check and a frontend carrying an inline ACL.
# The tag carrying var.phase is what makes the second apply exercise UpdateLB.
resource "scaleway_lb_ip" "conformance" {
  zone = "fr-par-1"
}

resource "scaleway_lb" "conformance" {
  name   = "conformance-lb"
  type   = "LB-S"
  ip_ids = [scaleway_lb_ip.conformance.id]
  zone   = "fr-par-1"
  tags   = ["conformance", var.phase]

  private_network {
    private_network_id = scaleway_vpc_private_network.conformance.id
  }
}

resource "scaleway_lb_backend" "conformance" {
  lb_id            = scaleway_lb.conformance.id
  name             = "conformance-backend"
  forward_protocol = "tcp"
  forward_port     = 6443
  server_ips       = ["10.42.0.11", "10.42.0.12"]

  health_check_timeout = "5s"
  health_check_delay   = "30s"
  health_check_https {
    uri  = "/readyz"
    code = 401
  }
}

resource "scaleway_lb_frontend" "conformance" {
  lb_id        = scaleway_lb.conformance.id
  backend_id   = scaleway_lb_backend.conformance.id
  name         = "conformance-frontend"
  inbound_port = 6443

  acl {
    name = "deny-all"
    action {
      type = "deny"
    }
    match {
      ip_subnet = ["0.0.0.0/0"]
    }
  }
}

# The Public Gateway chain (#282): the path terraform-talos and Scaleway's own
# VPC module walk — a gateway IP, the gateway, the connection with its
# IPAM-booked address and the pushed default route.
resource "scaleway_vpc_public_gateway_ip" "conformance" {
  zone = "fr-par-1"
}

resource "scaleway_ipam_ip" "gateway" {
  source {
    private_network_id = scaleway_vpc_private_network.conformance.id
  }
}

resource "scaleway_vpc_public_gateway" "conformance" {
  name  = "conformance-gateway"
  type  = "VPC-GW-S"
  ip_id = scaleway_vpc_public_gateway_ip.conformance.id
  zone  = "fr-par-1"
  tags  = ["conformance", var.phase]
}

resource "scaleway_vpc_gateway_network" "conformance" {
  gateway_id         = scaleway_vpc_public_gateway.conformance.id
  private_network_id = scaleway_vpc_private_network.conformance.id
  enable_masquerade  = true
  zone               = "fr-par-1"

  ipam_config {
    push_default_route = true
    ipam_ip_id         = scaleway_ipam_ip.gateway.id
  }
}

output "lb_ip_address" {
  value = scaleway_lb_ip.conformance.ip_address
}

output "lb_id" {
  value = scaleway_lb.conformance.id
}

output "gateway_ip_address" {
  value = scaleway_vpc_public_gateway_ip.conformance.address
}

output "gateway_network_id" {
  value = scaleway_vpc_gateway_network.conformance.id
}

# The two identifiers the second apply needs in order to prove its PATCH landed.
# Both families were mounted on 2026-08-17 so that a first apply editing a tag
# would not meet a 501, and both then sat undriven because this fixture created
# and destroyed without ever editing them — the reason was written at the route
# in Route.Undriven and named this file. The suite reads them back from the
# emulator after the second apply, so a 200 that stored nothing fails here.
output "placement_group_id" {
  value = scaleway_instance_placement_group.conformance.id
}

output "private_nic_id" {
  value = scaleway_instance_private_nic.conformance.id
}
