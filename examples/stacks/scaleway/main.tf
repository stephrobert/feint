# A three-tier platform on Scaleway, with a separate management VPC.
#
# Written the way a real project is written rather than the way a test fixture
# is: two VPCs that do not share a network, a network ACL on the workload one,
# a bastion that is the only public door, web and application tiers with their
# own security groups, a load balancer in front of the web tier, a public
# gateway carrying the app tier's way out, a placement group spreading the
# workers, data disks, a golden image cut from a snapshot, block-storage
# volumes with their own snapshots, an IAM SSH key, and addresses booked from
# IPAM before anything carries them.
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
# The SSH key the project holds. IAM is its own product with its own API
# (iam/v1alpha1), and the key family is the one part of it the emulator serves:
# a registered key reaches the cloud-init of every machine the project starts,
# which is what `mise run conformance:ssh` logs into. The scw CLI drives this
# family in conformance; this resource is its Terraform reader.
# ---------------------------------------------------------------------------

resource "scaleway_iam_ssh_key" "platform" {
  name       = "platform"
  public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD platform@example"
}

# ---------------------------------------------------------------------------
# Two VPCs. The workload one and the management one, which is the shape most
# platforms end up with and the one that makes isolation a real question.
# ---------------------------------------------------------------------------

resource "scaleway_vpc" "workload" {
  name = "platform-workload"
  tags = ["platform", "workload"]

  # Redundant since #497 was fixed on 2026-08-27, and kept because it is true:
  # the real cloud answers `routing_enabled: true` for a VPC created without
  # the field, and this emulator now does the same — a VPC created without
  # `enable_routing` routes, and its Private Networks are peered on the host.
  #
  # It was load-bearing when it was written, which is why the record stays.
  # The emulator stored the Go zero, so the same configuration read `false`
  # here and `true` on a real fr-par account, and under `--vm incus-ovn` the
  # consequence was not cosmetic: no `incus network peer` between the web and
  # app networks, their isolation sets rejecting each other, and
  # web → app:8080 refused by that rejection rather than by any group — so
  # `scaleway_vpc_route` and the app group's "accept 8080 from the web block"
  # described a path no packet could take. tools/conformance/functional.sh
  # asserts the pair that unblocks.
  #
  # The management VPC below still says nothing, deliberately: it is the half
  # of this stack that exercises the emulator's own default.
  enable_routing = true
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

# The workload VPC's network ACL. Two operations behind one path — SetACL is a
# PUT, GetACL the read-back — so the only place a defect can show is a second
# plan that is not empty: a rule the emulator reorders, retypes or drops
# surfaces as a permanent diff and nowhere else. The conformance fixture makes
# the same argument; this instance holds it in the stack a reader copies.
resource "scaleway_vpc_acl" "workload" {
  vpc_id         = scaleway_vpc.workload.id
  is_ipv6        = false
  default_policy = "accept"

  rules {
    protocol      = "TCP"
    source        = "0.0.0.0/0"
    src_port_low  = 0
    src_port_high = 0
    destination   = "10.30.2.0/24"
    dst_port_low  = 8080
    dst_port_high = 8080
    action        = "drop"
    description   = "the app tier is reached through the web tier, never directly"
  }
}

# ---------------------------------------------------------------------------
# The way out for machines that carry no address of their own: a public
# gateway on the app network, masquerading, with the default route pushed
# through an IPAM-booked address. terraform-talos and Scaleway's own VPC
# module walk exactly this chain, and the app tier below is the tier that
# needs it. The emulator records the gateway and refuses the wrong destroy
# order; it NATs nothing and its address is TEST-NET-1 on purpose —
# docs/limits.md states both halves.
# ---------------------------------------------------------------------------

resource "scaleway_vpc_public_gateway_ip" "egress" {}

resource "scaleway_ipam_ip" "gateway" {
  source {
    private_network_id = scaleway_vpc_private_network.app.id
  }
}

resource "scaleway_vpc_public_gateway" "egress" {
  name  = "platform-egress"
  type  = "VPC-GW-S"
  ip_id = scaleway_vpc_public_gateway_ip.egress.id
  tags  = ["platform", "egress"]
}

resource "scaleway_vpc_gateway_network" "egress" {
  gateway_id         = scaleway_vpc_public_gateway.egress.id
  private_network_id = scaleway_vpc_private_network.app.id
  enable_masquerade  = true

  ipam_config {
    push_default_route = true
    ipam_ip_id         = scaleway_ipam_ip.gateway.id
  }
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

  # No `packages:` here, deliberately (#507). A server created with a public IP
  # boots on a routed NIC with no NAT and no resolver (docs/limits.md, "A
  # machine's route out"), so a package step cannot complete: under a runtime,
  # cloud-init ends in `status: error` on every machine that declares one, in a
  # journal inside the guest that nobody opens. A stack declares what its
  # environment can hold — this user data exercises the same delivery path,
  # with steps that succeed offline.
  cloud_init = <<-EOT
    #cloud-config
    write_files:
      - path: /etc/motd
        content: |
          platform bastion — access is logged
    runcmd:
      - [sh, -c, "touch /var/lib/platform-bastion-ready"]
  EOT
}

resource "scaleway_instance_private_nic" "bastion" {
  server_id          = scaleway_instance_server.bastion.id
  private_network_id = scaleway_vpc_private_network.admin.id
}

# A user-data key beside cloud-init. The server's own `cloud_init` argument
# writes the reserved `cloud-init` key; this resource writes a named key of
# its own, which is a different door — SetServerUserData and GetServerUserData
# take the value as a raw body, not JSON, and a stack is the only client that
# reads a key back after writing it.
resource "scaleway_instance_user_data" "bastion_motd" {
  server_id = scaleway_instance_server.bastion.id
  key       = "motd"
  value     = "platform bastion - access is logged"
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

  # No `packages:` for the same reason as the bastion (#507): no route out on a
  # routed NIC, so `nginx` could never install. The web tier still gets a real
  # listener — python3 is guaranteed wherever cloud-init runs, since cloud-init
  # is python.
  #
  # Two things changed here on 2026-08-27 (#503), and both are load-bearing for
  # tools/conformance/functional.sh:
  #
  #   - the listener moved from 80 to 443, which is the port this tier's own
  #     security group opens. It used to serve the one port no rule named, so
  #     the group and the service documented two different machines;
  #   - `systemd-run` became an installed, enabled unit. A transient unit dies
  #     with the machine: measured on 2026-08-27, a poweroff/poweron cycle
  #     through the API left `platform-web` inactive and the port silent, on a
  #     stack whose apply, second plan and destroy were all clean. "A service
  #     is installed and active" and "a process happens to be running" are not
  #     the same claim, and only the second one survived a restart.
  #
  # The page is the machine's own hostname, which under a runtime is the name
  # the emulator recorded in `Runtime.machine`: an answer therefore says which
  # machine served it, which is what a balancer assertion needs and what a
  # fixed string cannot give.
  # The second listener is the app tier's pair, moved onto the public path
  # (#548): 443 is the port this tier's own group opens, 9090 is a metrics port
  # no rule of `scaleway_instance_security_group.web` names. Both on the
  # machine that carries a flexible IP, so the closed half can be probed on the
  # address a client actually dials — which is the half tools/conformance/
  # functional.sh skipped for as long as that address lived on a routed NIC
  # nothing could filter.
  cloud_init = <<-EOT
    #cloud-config
    write_files:
      - path: /etc/systemd/system/platform-web.service
        content: |
          [Unit]
          Description=platform web tier
          After=network-online.target
          [Service]
          WorkingDirectory=/var/www/html
          ExecStart=/usr/bin/python3 -m http.server 443
          Restart=always
          [Install]
          WantedBy=multi-user.target
      - path: /etc/systemd/system/platform-web-metrics.service
        content: |
          [Unit]
          Description=platform web metrics, private to the tier
          After=network-online.target
          [Service]
          WorkingDirectory=/var/www/html
          ExecStart=/usr/bin/python3 -m http.server 9090
          Restart=always
          [Install]
          WantedBy=multi-user.target
    runcmd:
      - [mkdir, -p, /var/www/html]
      - [sh, -c, "hostname > /var/www/html/index.html"]
      - [systemctl, daemon-reload]
      - [systemctl, enable, --now, platform-web.service]
      - [systemctl, enable, --now, platform-web-metrics.service]
  EOT
}

resource "scaleway_instance_private_nic" "web" {
  count = var.web_count

  server_id          = scaleway_instance_server.web[count.index].id
  private_network_id = scaleway_vpc_private_network.web.id
  ipam_ip_ids        = [scaleway_ipam_ip.web[count.index].id]
}

# ---------------------------------------------------------------------------
# The load balancer in front of the web tier. What each piece exercises is
# stated, because each is a distinct API family: the standalone address
# (CreateIP), the balancer joined to the web network (AttachPrivateNetwork),
# the backend whose pool travels through SetBackendServers, the frontend
# carrying an inline ACL (CreateACL), and a route that splits by SNI — the
# one lb resource no conformance fixture declares.
#
# What a green apply proves, honestly: the configuration round-trips, field
# for field. Nothing probes a backend and no packet is forwarded — the
# balancer's address is TEST-NET-2, and the stats operations are declined
# rather than invented (docs/limits.md, "A Scaleway load balancer and public
# gateway record their configuration").
# ---------------------------------------------------------------------------

resource "scaleway_lb_ip" "front" {}

resource "scaleway_lb" "front" {
  name   = "platform-front"
  type   = "LB-S" # from the emulated offer table; an unknown type fails client-side
  ip_ids = [scaleway_lb_ip.front.id]
  tags   = ["platform", "web"]

  private_network {
    private_network_id = scaleway_vpc_private_network.web.id
  }
}

resource "scaleway_lb_backend" "web" {
  lb_id            = scaleway_lb.front.id
  name             = "platform-web"
  forward_protocol = "tcp"
  forward_port     = 443

  # The IPAM-booked addresses of the web tier: the pool is reconciled through
  # SetBackendServers alone (the provider never edits it incrementally), so an
  # emulator that stored the list without answering it back would re-plan here
  # for ever.
  server_ips = [for ip in scaleway_ipam_ip.web : ip.address]

  health_check_timeout = "5s"
  health_check_delay   = "30s"
  health_check_https {
    uri  = "/healthz"
    code = 200
  }
}

resource "scaleway_lb_frontend" "https" {
  lb_id        = scaleway_lb.front.id
  backend_id   = scaleway_lb_backend.web.id
  name         = "platform-https"
  inbound_port = 443

  # An inline ACL, the way the provider models one: reconciled rule by rule
  # (CreateACL, ListACLs, UpdateACL, DeleteACL) — the bulk SetACLs call has no
  # client and the emulator declines it for exactly that reason.
  acl {
    name = "drop-documentation-range"
    action {
      type = "deny"
    }
    match {
      ip_subnet = ["192.0.2.0/24"]
    }
  }
}

# The SNI split, on the lb family's own route resource. It is here because no
# conformance fixture declares one: the route operations are driven by the CLI
# suite, and this is their first Terraform reader in the repository.
resource "scaleway_lb_route" "admin" {
  frontend_id = scaleway_lb_frontend.https.id
  backend_id  = scaleway_lb_backend.web.id
  match_sni   = "admin.platform.example"
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

# Spread the workers apart. At provider pin 2.81.0 this one resource walks two
# API doors on the same ID — CRUD through instance/v2alpha1, policy_mode
# through instance/v1 — so it exercises the mixed-halves path that broke the
# NIC family the day 2.81.0 released. The record is honest about its effect:
# nothing schedules hosts here, so the policy round-trips and places nothing
# (docs/limits.md, #285).
resource "scaleway_instance_placement_group" "app" {
  name        = "platform-app"
  policy_type = "max_availability"
  policy_mode = "enforced"
  tags        = ["platform", "app"]
}

resource "scaleway_instance_server" "app" {
  for_each = var.app_servers

  name              = "platform-app-${each.key}"
  type              = "DEV1-S"
  image             = "ubuntu_jammy"
  security_group_id = scaleway_instance_security_group.app.id
  tags              = ["platform", "app"]

  # The membership travels on the server (CreateServer's placement_group), and
  # the read-back is an embedded object: a server view without it would detach
  # the group at every refresh.
  placement_group_id = scaleway_instance_placement_group.app.id

  # No ip_id at all: this tier is unreachable from outside, which is the point.

  # Two listeners, and the pair is the whole reason this tier gained a user
  # data (#503): 8080 is the port `scaleway_instance_security_group.app` opens
  # to the web block, 9090 is a metrics port no rule names. A firewall check
  # needs both in one pass — a closed port measured alone cannot be told from a
  # dead service, and an open port measured alone cannot be told from a
  # firewall that filters nothing. Here the two ports live on the same machine,
  # behind the same group, probed from the same web machine, so the only thing
  # that differs between them is the rule.
  #
  # Installed units rather than `systemd-run`, for the reason the web tier's
  # comment gives: a transient unit does not survive the machine.
  cloud_init = <<-EOT
    #cloud-config
    write_files:
      - path: /etc/systemd/system/platform-app.service
        content: |
          [Unit]
          Description=platform application tier
          After=network-online.target
          [Service]
          WorkingDirectory=/var/www/html
          ExecStart=/usr/bin/python3 -m http.server 8080
          Restart=always
          [Install]
          WantedBy=multi-user.target
      - path: /etc/systemd/system/platform-app-metrics.service
        content: |
          [Unit]
          Description=platform application metrics, private to the tier
          After=network-online.target
          [Service]
          WorkingDirectory=/var/www/html
          ExecStart=/usr/bin/python3 -m http.server 9090
          Restart=always
          [Install]
          WantedBy=multi-user.target
    runcmd:
      - [mkdir, -p, /var/www/html]
      - [sh, -c, "hostname > /var/www/html/index.html"]
      - [systemctl, daemon-reload]
      - [systemctl, enable, --now, platform-app.service]
      - [systemctl, enable, --now, platform-app-metrics.service]
  EOT
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

# TEST-NET-2, and routed nowhere on purpose: see docs/limits.md. It is read
# here so the apply proves the balancer publishes an address at all.
output "front_address" {
  value = scaleway_lb_ip.front.ip_address
}

# TEST-NET-1, same argument, on the other product's address block: no two
# products of this emulator ever publish the same address.
output "egress_address" {
  value = scaleway_vpc_public_gateway_ip.egress.address
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
