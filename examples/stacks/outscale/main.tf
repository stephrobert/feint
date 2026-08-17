# Two peered Nets on Outscale, with a public tier, a private tier and a shared
# services Net — the shape a platform reaches when one account holds more than
# one environment.
#
# Written as a real project would be, not as a fixture: an internet service and
# a route table on the public side, a peering between the two Nets with the
# route that makes it useful, a NIC of its own on the application machine,
# volumes with snapshots, an image cut from one of them, and tags everywhere
# because the provider sends them on almost every call.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    outscale = {
      source  = "outscale/outscale"
      version = "~> 1.3"
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
# The workload Net: a public subnet and a private one.
# ---------------------------------------------------------------------------

resource "outscale_net" "workload" {
  ip_range = "10.50.0.0/16"

  tags {
    key   = "Name"
    value = "platform-workload"
  }
}

resource "outscale_subnet" "public" {
  net_id   = outscale_net.workload.net_id
  ip_range = "10.50.1.0/24"

  tags {
    key   = "Name"
    value = "platform-public"
  }
}

resource "outscale_subnet" "private" {
  net_id   = outscale_net.workload.net_id
  ip_range = "10.50.2.0/24"

  tags {
    key   = "Name"
    value = "platform-private"
  }
}

# ---------------------------------------------------------------------------
# The shared-services Net, peered with the workload one. Two Nets that must
# reach each other is the case a single-Net example never exercises.
# ---------------------------------------------------------------------------

resource "outscale_net" "services" {
  ip_range = "10.60.0.0/16"

  tags {
    key   = "Name"
    value = "platform-services"
  }
}

resource "outscale_subnet" "services" {
  net_id   = outscale_net.services.net_id
  ip_range = "10.60.1.0/24"

  tags {
    key   = "Name"
    value = "platform-services"
  }
}

resource "outscale_net_peering" "workload_to_services" {
  accepter_net_id = outscale_net.services.net_id
  source_net_id   = outscale_net.workload.net_id
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
  net_id              = outscale_net.workload.net_id
}

resource "outscale_route_table" "public" {
  net_id = outscale_net.workload.net_id

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

# The route that makes the peering useful, which is the half people forget.
resource "outscale_route" "to_services" {
  route_table_id       = outscale_route_table.public.route_table_id
  destination_ip_range = "10.60.0.0/16"
  net_peering_id       = outscale_net_peering.workload_to_services.net_peering_id

  depends_on = [outscale_net_peering_acceptation.workload_to_services]
}

resource "outscale_route_table_link" "public" {
  route_table_id = outscale_route_table.public.route_table_id
  subnet_id      = outscale_subnet.public.subnet_id
}

# ---------------------------------------------------------------------------
# Security groups, one per tier.
# ---------------------------------------------------------------------------

resource "outscale_security_group" "web" {
  description         = "platform web tier"
  security_group_name = "platform-web"
  net_id              = outscale_net.workload.net_id
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
  net_id              = outscale_net.workload.net_id
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
# A golden image, from a volume and its snapshot.
# ---------------------------------------------------------------------------

resource "outscale_volume" "golden" {
  subregion_name = "eu-west-2a"
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
# The machines.
# ---------------------------------------------------------------------------

data "outscale_images" "ubuntu" {
  filter {
    name   = "image_names"
    values = ["Ubuntu-24.04-2025.01"]
  }
}

resource "outscale_vm" "web" {
  count = var.web_count

  image_id           = data.outscale_images.ubuntu.images[0].image_id
  vm_type            = "tinav5.c1r1p2"
  subnet_id          = outscale_subnet.public.subnet_id
  security_group_ids = [outscale_security_group.web.security_group_id]

  tags {
    key   = "Name"
    value = "platform-web-${count.index}"
  }
}

resource "outscale_public_ip" "web" {
  count = var.web_count
}

resource "outscale_public_ip_link" "web" {
  count = var.web_count

  vm_id     = outscale_vm.web[count.index].vm_id
  public_ip = outscale_public_ip.web[count.index].public_ip
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
  vm_type            = "tinav5.c1r1p2"
  subnet_id          = outscale_subnet.private.subnet_id
  security_group_ids = [outscale_security_group.app.security_group_id]

  tags {
    key   = "Name"
    value = "platform-app"
  }
}

# A NIC of its own on the services Net — the case that needs an interface
# created separately rather than the one a Vm is born with.
resource "outscale_nic" "app_services" {
  subnet_id = outscale_subnet.services.subnet_id

  tags {
    key   = "Name"
    value = "platform-app-services"
  }
}

resource "outscale_volume" "app_data" {
  subregion_name = "eu-west-2a"
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
  value = outscale_public_ip.web[*].public_ip
}

output "machines" {
  value = var.web_count + 1
}
