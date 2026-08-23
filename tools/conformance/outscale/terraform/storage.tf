# OSC-4's closing condition (#13): the storage chain, driven by the real
# provider — volume, link onto the machine, snapshot of it, image cut from the
# snapshot.
#
# The dependency order is the point. Each resource here refuses something the
# one before it must have done first, and the refusals are what a real apply
# walks into when the emulator gets them wrong:
#
#   - a volume links only to a machine that exists, under a device name;
#   - a snapshot refuses a volume that has not settled — the emulator answers
#     the measured 409 InvalidVolumeState, so the provider's own wait is what
#     carries this across;
#   - an image needs a snapshot that exists, and inherits its size.

resource "outscale_volume_link" "conformance" {
  device_name = "/dev/xvdc"
  volume_id   = outscale_volume.conformance.volume_id
  vm_id       = outscale_vm.conformance.vm_id
}

resource "outscale_snapshot" "conformance" {
  volume_id   = outscale_volume.conformance.volume_id
  description = "feint conformance"
}

# Cut from the snapshot rather than from the machine, and still so after #378
# gave every machine a root volume: an image made from a VmId would carry an
# empty mapping list, because an image copies bytes and this emulator holds
# none, so it cuts no snapshot of the machine's disk. From a snapshot the
# provenance is real — the snapshot exists, has a size, and the image inherits
# it, which is what terraform.sh asserts below.
resource "outscale_image" "conformance" {
  image_name       = "feint-conformance-omi"
  description      = "cut from a conformance snapshot"
  root_device_name = "/dev/sda1"

  block_device_mappings {
    device_name = "/dev/sda1"

    bsu {
      snapshot_id = outscale_snapshot.conformance.snapshot_id
      volume_size = 10
      volume_type = "standard"
    }
  }
}

output "snapshot_id" {
  value = outscale_snapshot.conformance.snapshot_id
}

output "image_id" {
  value = outscale_image.conformance.image_id
}
