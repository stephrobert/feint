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
# between the two Nets, and tags ride on almost every call because the
# provider sends them on almost every call. Each of those motifs is lifted
# from a stack in examples/stacks/surveyed.md written by someone who never saw
# this repository.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    outscale = {
      source  = "outscale/outscale"
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

output "machines" {
  value = length(outscale_vm.web) + 1
}
