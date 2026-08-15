# The Net peering pair, driven by the two provider resources that exist for it.
#
# `outscale_net_peering` creates the request and — measured in the provider's
# own source (resource_net_peering.go, v1.8.0) — does three things a unit test
# never sees: before creating, when no accepter_owner_id is given, it calls
# ReadNets with Filters.NetIds naming BOTH Nets and demands exactly two answers;
# after creating it polls ReadNetPeerings with Filters.NetPeeringIds until the
# state is pending-acceptance or active, and treats `failed` as an error carrying
# the state's own Message. `outscale_net_peering_acceptation` then drives
# AcceptNetPeering and reads the peering back through the same filter.
#
# The destroy order matters and is what this fixture proves on every run:
# Terraform deletes the peering before either Net, DeleteNetPeering leaves the
# record in the `deleted` state, and the Nets must still delete afterwards — a
# deleted-state peering naming a Net must not block it.
resource "outscale_net" "peer" {
  ip_range = "10.71.0.0/16"

  tags {
    key   = "name"
    value = "feint-conformance-peer"
  }
}

resource "outscale_net_peering" "conformance" {
  source_net_id   = outscale_net.conformance.net_id
  accepter_net_id = outscale_net.peer.net_id

  # Tagged so CreateTags runs against a pcx- identifier: the prefix table in
  # tags.go had to learn each prefix one defect at a time (#99), and this is
  # the line that keeps the newest one exercised by a real client.
  tags {
    key   = "name"
    value = "feint-conformance-pcx"
  }
}

resource "outscale_net_peering_acceptation" "conformance" {
  net_peering_id = outscale_net_peering.conformance.net_peering_id
}

output "net_peering_id" {
  value = outscale_net_peering.conformance.net_peering_id
}

# The accepted state, read back through the acceptation resource so the suite
# can assert `active` against the emulator's own answer rather than against
# Terraform's belief.
output "net_peering_state" {
  value = one(outscale_net_peering_acceptation.conformance.state[*].name)
}
