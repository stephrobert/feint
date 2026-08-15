#!/usr/bin/env bash
# Shared conformance assertions about what a machine carries.
#
# Sourced by tools/conformance/<provider>/network.sh. It exists because the
# property below was written for one provider out of three, and the two gaps it
# left are exactly what shipped: an Exoscale instance carrying two addresses
# where its API published one, and a Scaleway server carrying an address from
# the runtime's default profile that no API field described.
#
# What varies is a parameter and what does not is shared, which is this
# repository's own rule for the packs and applies to their suites for the same
# reason: an assertion copied into three scripts is an assertion one script
# forgets, and a fourth provider never has at all.
#
# The caller supplies what its own API publishes, because that is the only
# provider-shaped half. The comparison, the reading of the host, and the verdict
# are here.
#
# Every function expects `ok`, `fail` and `skip` to be defined by the caller,
# which every suite already does.

# addresses_carried echoes every IPv4 address a machine carries, loopback
# excluded, read from the runtime rather than from the API — the control plane
# is not a witness for itself.
addresses_carried() {
  incus query "/1.0/instances/$1/state" 2>/dev/null \
    | jq -r '[.network | to_entries[] | select(.key != "lo")
             | .value.addresses[]? | select(.family == "inet") | .address] | join(" ")'
}

# assert_only_published <machine> <published...>
#
# Every address the machine carries must be one the provider's API publishes.
# The reverse is not checked here: an address published and not yet carried is a
# machine still booting, which the suites already assert on their own terms.
#
# A carried-but-unpublished address is a hard failure. It used to be a skip,
# tolerated because "a server still gets an interface from the runtime's default
# profile, and the API has no field describing it" — that sentence described the
# defect #202 removed, and keeping it as a skip afterwards would let the defect
# come back unnoticed.
assert_only_published() {
  local machine="$1"
  shift
  local published=" $* "
  local carried address unpublished=""

  carried="$(addresses_carried "$machine")"
  if [ -z "$carried" ]; then
    fail "$machine carries no address at all; the API published:$published"
  fi
  for address in $carried; do
    case "$published" in
      *" $address "*) ;;
      *) unpublished="$unpublished $address" ;;
    esac
  done
  if [ -n "$unpublished" ]; then
    fail "$machine carries$unpublished, which no API field publishes (carried: $carried)"
  fi
  ok "every address it carries is published ($carried)"
}
