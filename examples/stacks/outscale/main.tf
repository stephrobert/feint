# Two peered Nets on Outscale, with a public tier spread over two subregions,
# a private tier behind a NAT service, and a shared-services Net — the shape a
# platform reaches when one account holds more than one environment.
#
# Written as a real project would be, not as a fixture: the Nets come from a
# module the way ztiac builds its VPCs, the availability zones are asked from
# the API the way ocp_outscale asks (never hardcoded), the web machines are
# spread over two subregions the way kasten-on-outscale places its workers, an
# internet service and a route table serve the public side, a NAT service and
# its own route table serve the private side, a peering carries the route
# between the two Nets, a load balancer fronts the web tier the way three of
# the five surveyed stacks front theirs, the Net wears a DHCP options set of
# its own, the app machine carries a second interface attached explicitly,
# and tags ride on almost every call because the provider sends them on
# almost every call. Each of those motifs is lifted from a stack in
# examples/stacks/surveyed.md written by someone who never saw this
# repository.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    outscale = {
      source = "outscale/outscale"
      # The floor is 1.7, the same one the conformance fixture pins: the 1.7+
      # generation reads its endpoint path from the value (OSC_ENDPOINT_API
      # carries /api/v1), where 1.1.x appends the path itself — the boundary is
      # measured, per generation, in examples/stacks/surveyed.md. A resolution
      # below 1.7 would silently change how this stack must be pointed at the
      # emulator.
      version = "~> 1.7"
    }
  }
}

variable "endpoint" {
  type    = string
  default = "http://127.0.0.1:4599"
}

variable "vm_type" {
  type        = string
  description = "The Vm type every machine uses. Must exist in the emulated catalogue."
  default     = "tinav5.c1r1p2"
}

# The endpoint carries the whole API path — their documentation gives the shape
# `https://api.eu-west-2.outscale.com/api/v1` — so the version segment belongs to
# the value rather than being appended by the provider. Getting it wrong is not a
# warning: the emulator answers 404 and names the missing prefix, which is how
# this file came to be written correctly.
provider "outscale" {
  access_key_id = "AAAAAAAAAAAAAAAAAAAA"
  secret_key_id = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

  api {
    endpoint = "${var.endpoint}/api/v1"
    region   = "eu-west-2"
  }
}

# ---------------------------------------------------------------------------
# Where machines may go is asked, not assumed. davmartini/ocp_outscale plans
# its subnets over `data "outscale_subregions"` and indexes past [0]; against
# the one-zone catalogue that index was "Invalid index", zero resources
# created (#269). Keeping the same read here keeps the catalogue honest.
# ---------------------------------------------------------------------------

data "outscale_subregions" "all" {}

locals {
  az_a = data.outscale_subregions.all.subregions[0].subregion_name
  az_b = data.outscale_subregions.all.subregions[1].subregion_name
}

# ---------------------------------------------------------------------------
# The workload Net: a public subnet per subregion, and a private one. The
# services Net beside it. Both come from the same module, which is how ztiac
# and pli01/terraform-outscale-k3s shape every Net they build.
# ---------------------------------------------------------------------------

module "workload" {
  source = "./modules/net"

  name     = "platform-workload"
  ip_range = "10.50.0.0/16"

  subnets = {
    public-a = { ip_range = "10.50.1.0/24", subregion_name = local.az_a }
    public-b = { ip_range = "10.50.3.0/24", subregion_name = local.az_b }
    private  = { ip_range = "10.50.2.0/24", subregion_name = local.az_a }
  }
}

module "services" {
  source = "./modules/net"

  name     = "platform-services"
  ip_range = "10.60.0.0/16"

  # No subregion named: this subnet takes the region default, and the read
  # back of that default is the other half of what #269 measured.
  subnets = {
    services = { ip_range = "10.60.1.0/24" }
  }
}

# ---------------------------------------------------------------------------
# The workload Net's DHCP options. On `outscale_net` itself the set is
# computed-only, so this pair is UpdateNet's one client-reachable shape: the
# dedicated set, and the attributes resource that points the Net at it. The
# proof is that a *different* set than the Net's default is retained and read
# back — pointing a Net at its own default proves only that the call decodes.
# ---------------------------------------------------------------------------

resource "outscale_dhcp_option" "workload" {
  domain_name         = "platform.internal"
  domain_name_servers = ["192.0.2.53", "192.0.2.54"]
  ntp_servers         = ["192.0.2.123"]

  tags {
    key   = "Name"
    value = "platform-dopt"
  }

  # Destroy order, pinned: the provider first re-points every Net wearing this
  # set at the `default` keyword (ReadNets filtered on the set, then UpdateNet),
  # and only then deletes it — so the Net must still be alive when that runs.
  depends_on = [module.workload]
}

resource "outscale_net_attributes" "workload" {
  net_id              = module.workload.net_id
  dhcp_options_set_id = outscale_dhcp_option.workload.dhcp_options_set_id
}

resource "outscale_net_peering" "workload_to_services" {
  accepter_net_id = module.services.net_id
  source_net_id   = module.workload.net_id
}

resource "outscale_net_peering_acceptation" "workload_to_services" {
  net_peering_id = outscale_net_peering.workload_to_services.net_peering_id
}

# ---------------------------------------------------------------------------
# The public door: an internet service, a route table, and the default route.
# ---------------------------------------------------------------------------

resource "outscale_internet_service" "main" {}

resource "outscale_internet_service_link" "main" {
  internet_service_id = outscale_internet_service.main.internet_service_id
  net_id              = module.workload.net_id
}

resource "outscale_route_table" "public" {
  net_id = module.workload.net_id

  tags {
    key   = "Name"
    value = "platform-public"
  }
}

resource "outscale_route" "default" {
  route_table_id       = outscale_route_table.public.route_table_id
  destination_ip_range = "0.0.0.0/0"
  gateway_id           = outscale_internet_service.main.internet_service_id

  depends_on = [outscale_internet_service_link.main]
}

# The route that makes the peering useful, which is the half people forget —
# and the half the emulator once refused (#249).
resource "outscale_route" "to_services" {
  route_table_id       = outscale_route_table.public.route_table_id
  destination_ip_range = "10.60.0.0/16"
  net_peering_id       = outscale_net_peering.workload_to_services.net_peering_id

  depends_on = [outscale_net_peering_acceptation.workload_to_services]
}

resource "outscale_route_table_link" "public" {
  for_each = toset(["public-a", "public-b"])

  route_table_id = outscale_route_table.public.route_table_id
  subnet_id      = module.workload.subnet_ids[each.key]
}

# The Net's *main* route table, re-pointed at the public table: a subnet added
# tomorrow without an explicit link then routes like the public tier, which is
# what "default route table" means in practice. It is also the one resource
# that sends UpdateRouteTableLink — `outscale_route_table_link` never does,
# both its attributes force a replacement — and its destroy drives the same
# operation a second time when the provider moves the main link back onto the
# default table.
resource "outscale_main_route_table_link" "workload" {
  net_id         = module.workload.net_id
  route_table_id = outscale_route_table.public.route_table_id
}

# ---------------------------------------------------------------------------
# The private door out: a NAT service with its own public IP, and the private
# route table whose default route goes through it. Every substantial surveyed
# stack builds this (ztiac one per AZ, ocp_outscale two, kasten one, pli01
# one); the conformance fixture creates a NAT service but never routes through
# it, so this route is the only place a client drives that target.
# ---------------------------------------------------------------------------

resource "outscale_public_ip" "nat" {}

resource "outscale_nat_service" "main" {
  subnet_id    = module.workload.subnet_ids["public-a"]
  public_ip_id = outscale_public_ip.nat.public_ip_id

  # A NAT service belongs in a subnet that already routes to an internet
  # service — same ordering the conformance fixture states.
  depends_on = [outscale_route_table_link.public]
}

resource "outscale_route_table" "private" {
  net_id = module.workload.net_id

  tags {
    key   = "Name"
    value = "platform-private"
  }
}

resource "outscale_route" "private_default" {
  route_table_id       = outscale_route_table.private.route_table_id
  destination_ip_range = "0.0.0.0/0"
  nat_service_id       = outscale_nat_service.main.nat_service_id
}

resource "outscale_route_table_link" "private" {
  route_table_id = outscale_route_table.private.route_table_id
  subnet_id      = module.workload.subnet_ids["private"]
}

# ---------------------------------------------------------------------------
# Security groups, one per tier.
# ---------------------------------------------------------------------------

resource "outscale_security_group" "web" {
  description         = "platform web tier"
  security_group_name = "platform-web"
  net_id              = module.workload.net_id
}

resource "outscale_security_group_rule" "web_https" {
  flow              = "Inbound"
  security_group_id = outscale_security_group.web.security_group_id
  from_port_range   = 443
  to_port_range     = 443
  ip_protocol       = "tcp"
  ip_range          = "0.0.0.0/0"
}

# Port 80 for the load balancer below: its listener and its health check both
# speak HTTP, and a group that only opened 443 would document a balancer no
# packet could reach.
resource "outscale_security_group_rule" "web_http" {
  flow              = "Inbound"
  security_group_id = outscale_security_group.web.security_group_id
  from_port_range   = 80
  to_port_range     = 80
  ip_protocol       = "tcp"
  ip_range          = "0.0.0.0/0"
}

resource "outscale_security_group" "app" {
  description         = "platform application tier"
  security_group_name = "platform-app"
  net_id              = module.workload.net_id
}

resource "outscale_security_group_rule" "app_from_web" {
  flow              = "Inbound"
  security_group_id = outscale_security_group.app.security_group_id
  from_port_range   = 8080
  to_port_range     = 8080
  ip_protocol       = "tcp"
  ip_range          = "10.50.1.0/24"
}

# ---------------------------------------------------------------------------
# The keypair the machines boot with. Every surveyed stack creates or requires
# one (osc-k8s-rke-cluster creates three), and a Vm naming a keypair that does
# not exist is refused with upstream's own 5063 — creating it here is the only
# order that applies.
# ---------------------------------------------------------------------------

resource "outscale_keypair" "platform" {
  keypair_name = "platform"
  public_key   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD platform@example"
}

# ---------------------------------------------------------------------------
# A golden image, from a volume and its snapshot.
# ---------------------------------------------------------------------------

resource "outscale_volume" "golden" {
  subregion_name = local.az_a
  size           = 10
}

resource "outscale_snapshot" "golden" {
  volume_id = outscale_volume.golden.volume_id
}

resource "outscale_image" "golden" {
  image_name = "platform-golden"

  block_device_mappings {
    device_name = "/dev/sda1"
    bsu {
      snapshot_id = outscale_snapshot.golden.snapshot_id
    }
  }
}

# ---------------------------------------------------------------------------
# The machines. The web tier is spread over both subregions, and each Vm names
# its placement explicitly beside its subnet — the kasten-on-outscale shape
# that found #268: CreateVms answered 200 and every read answered a constant
# eu-west-2a, so the second plan re-planned the same change for ever. The
# "second plan is empty" gate below is what bites if a read does that again.
# ---------------------------------------------------------------------------

data "outscale_images" "ubuntu" {
  filter {
    name   = "image_names"
    values = ["Ubuntu-24.04-2025.01"]
  }
}

locals {
  web_tier = {
    a = { subnet = "public-a", subregion = local.az_a }
    b = { subnet = "public-b", subregion = local.az_b }
  }
}

resource "outscale_vm" "web" {
  for_each = local.web_tier

  image_id           = data.outscale_images.ubuntu.images[0].image_id
  vm_type            = var.vm_type
  keypair_name       = outscale_keypair.platform.keypair_name
  subnet_id          = module.workload.subnet_ids[each.value.subnet]
  security_group_ids = [outscale_security_group.web.security_group_id]

  placement_subregion_name = each.value.subregion
  placement_tenancy        = "default"

  tags {
    key   = "Name"
    value = "platform-web-${each.key}"
  }
}

resource "outscale_public_ip" "web" {
  for_each = local.web_tier
}

resource "outscale_public_ip_link" "web" {
  for_each = local.web_tier

  vm_id     = outscale_vm.web[each.key].vm_id
  public_ip = outscale_public_ip.web[each.key].public_ip
}

# ---------------------------------------------------------------------------
# The LBU in front of the web tier — the family three of the five surveyed
# stacks carry, and the one this stack lacked. The trio is the shape the
# provider models: the balancer with its listeners inline, the backend Vms as
# their own attachment, the health check as an attributes resource.
#
# The pack holds the delete algebra under it: a subnet or a security group
# under a standing balancer refuses to go, so the destroy order this graph
# implies is itself part of what an apply-destroy cycle proves.
#
# The backends here span the two public subnets on purpose — the exact ztiac
# shape that found #457. Under `--vm incus-ovn` the host serves a dataplane
# only for a balancer whose backends share its own subnet (four measurements,
# docs/limits.md): this configuration is recorded, described and WARNed about,
# and the runtime declines its dataplane by name rather than half-serving it.
# With machines off it is a record that round-trips. Both are honest, and the
# stack keeps the shape so the next run keeps checking the refusal.
# ---------------------------------------------------------------------------

resource "outscale_load_balancer" "front" {
  load_balancer_name = "platform-front"
  load_balancer_type = "internet-facing"
  subnets            = [module.workload.subnet_ids["public-a"]]
  security_groups    = [outscale_security_group.web.security_group_id]

  listeners {
    backend_port           = 80
    backend_protocol       = "HTTP"
    load_balancer_protocol = "HTTP"
    load_balancer_port     = 80
  }

  tags {
    key   = "Name"
    value = "platform-front"
  }

  # A balancer belongs in a subnet that already routes to an internet service —
  # the same ordering the NAT service states above.
  depends_on = [outscale_route_table_link.public]
}

resource "outscale_load_balancer_vms" "front" {
  load_balancer_name = outscale_load_balancer.front.load_balancer_name
  backend_vm_ids     = [for vm in outscale_vm.web : vm.vm_id]
}

resource "outscale_load_balancer_attributes" "front" {
  load_balancer_name = outscale_load_balancer.front.load_balancer_name

  # Settings only: they must round-trip because the provider plans on them.
  # No health *state* exists until something probes a backend, and with
  # machines off nothing does.
  health_check {
    healthy_threshold   = 2
    unhealthy_threshold = 5
    check_interval      = 30
    port                = 80
    protocol            = "HTTP"
    path                = "/healthz"
    timeout             = 5
  }
}

resource "outscale_vm" "app" {
  # The catalogue image, not outscale_image.golden above. Same measurement as
  # the Scaleway stack's web tier: this emulator keeps records, not disk
  # contents, so an image the client registered has no bytes to boot. With a
  # machine runtime configured the Vm is refused at boot and stays `stopped`,
  # and the stack's own "the second plan is empty" assertion then fails with
  # `state = "stopped" -> "running"` — which is the emulator telling the truth,
  # not a defect.
  #
  # docs/limits.md carries the decision (#83): booting the source's base image
  # instead would silently drop whatever the client baked in, and a
  # golden-image workflow is exactly where that difference is the point.
  #
  # The image is still built, and outscale_image.golden below still proves the
  # snapshot → image chain. Only the boot moves.
  image_id           = data.outscale_images.ubuntu.images[0].image_id
  vm_type            = var.vm_type
  keypair_name       = outscale_keypair.platform.keypair_name
  subnet_id          = module.workload.subnet_ids["private"]
  security_group_ids = [outscale_security_group.app.security_group_id]

  tags {
    key   = "Name"
    value = "platform-app"
  }
}

# A NIC of its own on the services Net — the case that needs an interface
# created separately rather than the one a Vm is born with, and the resource
# whose tags once vanished on read (#250).
resource "outscale_nic" "app_services" {
  subnet_id = module.services.subnet_ids["services"]

  tags {
    key   = "Name"
    value = "platform-app-services"
  }
}

# A second interface for the app Vm, in its own subnet, attached explicitly:
# LinkNic and UnlinkNic are only ever driven by an interface created apart
# from its machine, since the one a Vm is born with never travels through
# them. Device 1, because device 0 is the machine's own.
resource "outscale_nic" "app_data_plane" {
  subnet_id          = module.workload.subnet_ids["private"]
  security_group_ids = [outscale_security_group.app.security_group_id]
  description        = "platform app data plane"

  tags {
    key   = "Name"
    value = "platform-app-nic"
  }
}

resource "outscale_nic_link" "app_data_plane" {
  device_number = "1"
  vm_id         = outscale_vm.app.vm_id
  nic_id        = outscale_nic.app_data_plane.nic_id
}

# Secondary addresses on that interface — the LinkPrivateIps door, which no
# other resource of this stack or of the conformance fixtures opens from
# Terraform. High in the /24 on purpose, clear of anything the subnet hands
# out on its own.
resource "outscale_nic_private_ip" "app_data_plane" {
  nic_id      = outscale_nic.app_data_plane.nic_id
  private_ips = ["10.50.2.200", "10.50.2.201"]
}

resource "outscale_volume" "app_data" {
  subregion_name = local.az_a
  size           = 20

  tags {
    key   = "Name"
    value = "platform-app-data"
  }
}

resource "outscale_volume_link" "app_data" {
  device_name = "/dev/xvdb"
  volume_id   = outscale_volume.app_data.volume_id
  vm_id       = outscale_vm.app.vm_id
}

output "web_public_ips" {
  value = { for name, ip in outscale_public_ip.web : name => ip.public_ip }
}

output "web_subregions" {
  # What the API reads back, not what the plan asked — the #268 round-trip.
  value = { for name, vm in outscale_vm.web : name => vm.placement_subregion_name }
}

output "nat_public_ip" {
  value = outscale_public_ip.nat.public_ip
}

# What the surveyed stacks feed to templatefile() and local_file: it must be a
# stable, well-formed name, or the second plan drifts.
output "front_dns_name" {
  value = outscale_load_balancer.front.dns_name
}

output "machines" {
  value = length(outscale_vm.web) + 1
}
