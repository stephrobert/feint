# The first thirty seconds: one server, one public address, on Scaleway.
#
# This is a *pedagogical* example, not a qualification stack. Its whole job is
# to be read whole before it is run, so it stops at what the Quick Start
# promises: the real Scaleway provider applies, the emulator answers, a second
# plan is empty and `feint down` destroys it.
#
# `examples/stacks/scaleway` is the other thing, and the split is deliberate
# (#593): 625 lines of two VPCs, ACLs, a bastion, a balancer, snapshots and a
# golden image, written to try to break the emulator. It found #249 and #250,
# which every other gate was blind to. It is a poor first read, and it was
# being asked to be both at once.

terraform {
  required_version = ">= 1.7.0"
  required_providers {
    scaleway = {
      source = "scaleway/scaleway"
      # Exact, and for the reason docs/clients.md gives at length: a floating
      # constraint is resolved by `terraform init -upgrade` on every run, so an
      # apply proves the emulator answered whatever was newest that morning and
      # nothing anybody can replay. `feint docs --check` refuses an applied
      # example that pins nothing.
      version = "2.81.0"
    }
  }
}

# Overridden by `feint up`, which passes the address it started the emulator on
# (iac.vars in feint.yaml). The default is what a hand-run `terraform apply`
# meets after `feint start`.
variable "endpoint" {
  type    = string
  default = "http://127.0.0.1:4599"
}

# The credentials are deliberately fake and deliberately public: the emulator
# accepts any, and `feint env scaleway` exports these same values.
provider "scaleway" {
  api_url         = var.endpoint
  access_key      = "SCWXXXXXXXXXXXXXXXXX"
  secret_key      = "11111111-1111-1111-1111-111111111111"
  project_id      = "11111111-1111-1111-1111-111111111111"
  organization_id = "11111111-1111-1111-1111-111111111111"
  region          = "fr-par"
  zone            = "fr-par-1"
}

resource "scaleway_instance_ip" "web" {}

resource "scaleway_instance_server" "web" {
  name  = "quickstart-web"
  type  = "DEV1-S"
  image = "ubuntu_jammy"
  ip_id = scaleway_instance_ip.web.id
  tags  = ["quickstart"]
}

output "address" {
  description = "The public address the emulator published for the server."
  value       = scaleway_instance_ip.web.address
}
