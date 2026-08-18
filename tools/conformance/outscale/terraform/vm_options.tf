# The per-machine scalars of #276, asked with NON-default values on purpose:
# BootMode, Performance and VmInitiatedShutdownBehavior were accepted at
# create with a 200 while every read answered a constant of the pack
# (uefi/high/stop) — a stack setting any of them re-planned the same in-place
# change for ever, exactly #268 one field over. A fixture asking the defaults
# would prove nothing: a constant and a datum are indistinguishable there.
#
# The vm_type deliberately carries NO performance flag (the tinavW.cXrY
# spelling): upstream ignores the Performance parameter when the type spells
# one (osc-sdk-go client.gen.go:3059), so a p2 type beside performance=medium
# would legitimately read back "high" and re-plan for ever against the real
# cloud too.
#
# The suite's own "second plan is empty" gate is what bites if any of the
# three reads back a constant again.
resource "outscale_vm" "options" {
  image_id = "ami-00000002"
  vm_type  = "tinav6.c1r1"

  boot_mode                      = "legacy"
  performance                    = "medium"
  vm_initiated_shutdown_behavior = "restart"
}

output "options_vm_id" {
  value = outscale_vm.options.vm_id
}
