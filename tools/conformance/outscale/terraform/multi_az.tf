# Multi-AZ, the way real stacks do it — the permanent regression for #268 and
# #269, found by applying third-party stacks rather than by any test here.
#
# Two shapes, both reduced from published stacks:
#
#   - davmartini/ocp_outscale asks the API where it may put things:
#     `data "outscale_subregions"` then `subregions[1].subregion_name`. Against
#     the one-zone catalogue that index was "Invalid index: list of object
#     with 1 element", zero resources created — the stack that did it right
#     was the one that could not even plan (#269).
#
#   - michaelcourcy/kasten-on-outscale places a worker explicitly:
#     `placement_subregion_name = "eu-west-2b"` beside its subnet. CreateVms
#     answered 200 and every read answered the pack's constant eu-west-2a, so
#     the second plan re-planned the same in-place change for ever (#268).
#
# The fixture keeps both: the subnet's zone comes from the data source at
# index [1] (the ocp pattern), and the Vm names the same zone explicitly (the
# kasten pattern). The existing `outscale_vm.conformance` lives in the default
# zone, so the suite now spreads machines across two subregions — and the
# suite's own "second plan is empty" gate is what bites if any read answers a
# constant again.

data "outscale_subregions" "all" {}

resource "outscale_subnet" "azb" {
  net_id         = outscale_net.conformance.net_id
  ip_range       = "10.70.2.0/24"
  subregion_name = data.outscale_subregions.all.subregions[1].subregion_name
}

resource "outscale_vm" "azb" {
  image_id  = "ami-00000003"
  vm_type   = "tinav6.c1r1p2"
  subnet_id = outscale_subnet.azb.subnet_id

  placement_subregion_name = data.outscale_subregions.all.subregions[1].subregion_name
  placement_tenancy        = "default"
}

output "azb_vm_id" {
  value = outscale_vm.azb.vm_id
}

# The zone the data source handed out, so the suite can assert the emulator
# reads the Vm back in the very zone the API told the stack to use.
output "azb_subregion" {
  value = data.outscale_subregions.all.subregions[1].subregion_name
}
