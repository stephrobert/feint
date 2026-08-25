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

# map_public_ip_on_launch is a variable so the suite can change it and apply
# again. That second apply is the point: create, read and delete of a Subnet were
# proven long before the *update* was served, and a plan that modifies a resource
# this emulator created used to die on an operation nobody had decided about
# (#172). An in-place change is what a user's second `terraform apply` is.
resource "outscale_subnet" "conformance" {
  net_id                  = outscale_net.conformance.net_id
  ip_range                = "10.70.1.0/24"
  map_public_ip_on_launch = var.map_public_ip
}

variable "map_public_ip" {
  type        = bool
  default     = false
  description = "Flipped by the suite's second apply, which is what proves UpdateSubnet."
}

# A second DHCP options set, created by the real provider. Until the DhcpOptions
# family landed (#172, second tranche), `outscale_net_attributes` below could
# only point the Net at its own default set — which proved UpdateNet was served
# and decoded, never that a *different* set was retained. This resource is the
# missing half.
#
# Its destroy is the interesting path: ResourceOutscaleDHCPOptionDelete first
# reads the Nets wearing the set (ReadNets filtered on DhcpOptionsSetIds), then
# re-points each at the `default` keyword through UpdateNet, and only then sends
# DeleteDhcpOptions. The depends_on pins the destroy order so the Net is still
# alive — and still wearing this set — when that sequence runs: without it,
# Terraform may destroy the two in either order and the detach path would only
# be driven on the runs that happened to pick this one first.
resource "outscale_dhcp_option" "conformance" {
  domain_name         = "conformance.feint"
  domain_name_servers = ["192.0.2.53", "192.0.2.54"]
  ntp_servers         = ["192.0.2.123"]

  # A tag, because the provider applies it through CreateTags with the dopt- id
  # it has just been handed — the same path issue #99 found broken one prefix
  # at a time.
  tags {
    key   = "Name"
    value = "conformance-dopt"
  }

  depends_on = [outscale_net.conformance]
}

# UpdateNet's one client-reachable shape. On `outscale_net` itself,
# dhcp_options_set_id is computed-only (provider schema, v1.8.0), so no change
# to the Net resource ever sends UpdateNet; this dedicated resource does, and on
# Create as well as Update (resource_net_attributes.go builds the same
# UpdateNetRequest in both).
#
# The value is the created set above, not the Net's own default: the apply
# therefore proves the emulator retained a *different* set, which is the proof
# the previous fixture explicitly recorded as owed.
resource "outscale_net_attributes" "conformance" {
  net_id              = outscale_net.conformance.net_id
  dhcp_options_set_id = outscale_dhcp_option.conformance.dhcp_options_set_id
}

variable "nic_description" {
  type        = string
  default     = "feint conformance nic"
  description = "Changed by the suite's second apply, which is what proves UpdateNic."
}

# A keypair, because a machine nobody can log into proves nothing — and because
# the provider addresses it by KeypairId on destroy while creating it by name.
# The emulator publishes both, and they are the same identity.
resource "outscale_keypair" "conformance" {
  keypair_name = "feint-conformance"
  public_key   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"
}

# A volume, because the provider creates one, reads it back and links it — and
# because nothing else in this repository drives CreateVolume. The octl suite
# never touches one, which is exactly why the routes were missing.
resource "outscale_volume" "conformance" {
  subregion_name = "eu-west-2a"
  size           = 10
}

resource "outscale_vm" "conformance" {
  # A catalogue OMI, and it matters beyond realism: ami-00000003 is one of the
  # three identifiers the emulator can actually boot (catalog.go maps it to a
  # machine image). The previous ami-12345678 resolved to nothing, which a
  # --vm off run never notices — and under a runtime the boot is then refused
  # (#83, no substitution), the Vm honestly never reaches "running", and the
  # empty-plan assertion fails. Measured on the first full client run under
  # FEINT_VM=incus, which #123's evidence regeneration was the first to do.
  image_id     = "ami-00000003"
  vm_type      = "tinav6.c1r1p2"
  subnet_id    = outscale_subnet.conformance.subnet_id
  keypair_name = outscale_keypair.conformance.keypair_name

  # The group from net_vm.tf, so the machine is created wearing an explicit
  # group rather than inheriting its Net's default. CreateVms refused this
  # argument outright until the group family was served; the apply is what
  # proves it is now read rather than dropped.
  security_group_ids = [outscale_security_group.net_vm.security_group_id]

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

# The created set, so the suite can ask the emulator whether the Net wears it —
# and whether it differs from the default one, which is the whole point.
output "dhcp_options_set_id" {
  value = outscale_dhcp_option.conformance.dhcp_options_set_id
}

# The suite asks the emulator directly whether the in-place change landed, so it
# needs the identifier the provider assigned rather than the one it asked for.
output "subnet_id" {
  value = outscale_subnet.conformance.subnet_id
}
