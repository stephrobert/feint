#!/usr/bin/env bash
# Conformance check: the emulated Exoscale private network is a real network.
#
# The assertions are about what the machines do with what the API said, not
# about the API's answers: the exo-cli suite already proves those. Three things
# are measured here. The range a client declares is the range its leases come
# from. The lease the API publishes is the address the machine carries. And two
# private networks are two separate segments — hard only when the runtime
# declares the isolation capability, skipped with the mode named otherwise,
# because a bridge-backed mode cannot deliver it (docs/limits.md) and claiming
# it anyway would be the half-truth this project exists to avoid.
#
# What is deliberately not here: firewall assertions. The Exoscale pack does
# not yet sync its security groups onto the machines, so a rule assertion would
# measure nothing — the Scaleway and Outscale suites own that ground for now.
#
# Requires a machine runtime: `FEINT_VM=incus mise run serve` at least, and
# `FEINT_VM=incus-ovn` for the isolation assertion to be hard. With --vm off
# the whole suite skips itself, which keeps `mise run conformance` runnable in
# CI.
#
# Usage: tools/conformance/exoscale/network.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a
# real account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../guard.sh"
guard_local "$ENDPOINT"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
set +a
export EXOSCALE_API_ENDPOINT=${ENDPOINT}/v2

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

command -v exo >/dev/null 2>&1 || { echo "FAIL: exo is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

# The blocks. Deliberately obscure, and distinct from every block the Scaleway
# and Outscale suites use on the same host: a collision would make the create
# fail, which is the emulator being honest about a range already in use.
NEAR_START="10.186.0.20"; NEAR_END="10.186.0.200"; NEAR_MASK="255.255.255.0"; NEAR_PREFIX="10.186.0."
FAR_START="10.187.0.20";  FAR_END="10.187.0.200";  FAR_MASK="255.255.255.0"

FEINT_BIN="${FEINT_BIN:-$SCRIPT_DIR/../../../feint}"
[ -x "$FEINT_BIN" ] || fail "no feint binary at $FEINT_BIN (build it: mise run build)"

echo "conformance: exoscale network against $ENDPOINT"

health="$(curl -sf "$ENDPOINT/_feint/health")"
MACHINES="$(printf '%s' "$health" | jq -r '.machines')"
if [ "$MACHINES" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); nothing to measure"
  exit 0
fi

# What this runtime can deliver, asked rather than inferred from its name: a
# suite that compares a mode string has to be edited every time a driver gains
# a capability, one that reads the declaration fails honestly when a mode
# claims something it stops delivering.
ISOLATION="$(printf '%s' "$health" | jq -r '.capabilities.isolation')"

sweep_runtime() {
  local out
  if ! out="$("$FEINT_BIN" clean --vm "${FEINT_VM:-incus}" 2>&1)"; then
    echo "FAIL: the runtime sweep failed, resources are left behind: $out" >&2
    return 1
  fi
  printf '  sweep: %s\n' "${out%%$'\n'*}"
}

cleanup_ids=()
cleanup() {
  for id in "${cleanup_ids[@]:-}"; do
    [ -n "$id" ] || continue
    curl -sf -X DELETE "$ENDPOINT/v2/instance/$id" >/dev/null 2>&1 || true
  done
  sweep_runtime || return
  if ! "$FEINT_BIN" clean --vm "${FEINT_VM:-incus}" 2>&1 | grep -q "nothing was left behind"; then
    echo "FAIL: the runtime is still not clean after the sweep" >&2
  fi
}
trap cleanup EXIT

sweep_runtime

echo "- a managed private network keeps the range it was given"
exo compute private-network create conformance-net \
  --start-ip "$NEAR_START" --end-ip "$NEAR_END" --netmask "$NEAR_MASK" >/dev/null \
  || fail "the emulator refused the range $NEAR_START-$NEAR_END (already in use on this host?)"
pn="$(exo -O json compute private-network show conformance-net)"
[ "$(printf '%s' "$pn" | jq -r '.start_ip')" = "$NEAR_START" ] \
  || fail "the range came back as $(printf '%s' "$pn" | jq -r '.start_ip'), expected $NEAR_START"
ok "conformance-net on $NEAR_START-$NEAR_END"

boot() { # name -> instance id
  local name="$1" id
  exo compute instance create "$name" --zone "$EXOSCALE_ZONE" \
    --template "Linux Ubuntu 24.04 LTS 64-bit" --instance-type standard.tiny >/dev/null \
    || fail "instance $name was not created"
  id="$(exo -O json compute instance list | jq -r --arg n "$name" '.[] | select(.name == $n) | .id')"
  [ -n "$id" ] || fail "instance $name is not in the list after create"
  cleanup_ids+=("$id")
  echo "$id"
}

echo "- two instances, attached to it"
guard_id="$(boot conformance-guard)"
probe_id="$(boot conformance-probe)"
exo compute instance private-network attach conformance-guard conformance-net >/dev/null \
  || fail "guard attach rejected"
exo compute instance private-network attach conformance-probe conformance-net >/dev/null \
  || fail "probe attach rejected"

# The addresses are resolved the way a client resolves them: off the network's
# leases, which is the only place their API publishes them.
leases="$(exo -O json compute private-network show conformance-net | jq -c '.leases')"
guard_ip="$(printf '%s' "$leases" | jq -r '.[] | select(.instance == "conformance-guard") | .ip_address')"
probe_ip="$(printf '%s' "$leases" | jq -r '.[] | select(.instance == "conformance-probe") | .ip_address')"
[ -n "$guard_ip" ] && [ -n "$probe_ip" ] || fail "an attach produced no lease: $leases"
ok "guard $guard_ip, probe $probe_ip"

case "$guard_ip" in
  "$NEAR_PREFIX"*) ok "the lease belongs to the declared range" ;;
  *) fail "$guard_ip is outside ${NEAR_PREFIX}0/24" ;;
esac
[ "$guard_ip" != "$probe_ip" ] || fail "both instances received $guard_ip"

# From here the assertions look inside the machines, which is the point: the
# control plane is not a witness for itself.
machine() { echo "feint-exo-$1"; }
if ! command -v incus >/dev/null 2>&1; then
  skip "incus client not available; cannot verify what the machines carry"
  exit 0
fi
sleep 3

echo "- the machine carries the lease the API published"
carried="$(incus query "/1.0/instances/$(machine "$guard_id")/state" 2>/dev/null \
           | jq -r '[.network[]?.addresses[]? | select(.family=="inet") | .address] | join(" ")')"
case " $carried " in
  *" $guard_ip "*) ok "guard carries $guard_ip" ;;
  *) fail "guard carries '${carried:-nothing}', the API published $guard_ip" ;;
esac

echo "- an instance of the same private network is reachable"
incus exec "$(machine "$guard_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &
sleep 2
if incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$guard_ip" 80 >/dev/null 2>&1; then
  ok "the probe reaches the guard on their shared network ($guard_ip)"
else
  fail "$guard_ip is unreachable inside one private network; the segment is broken, not isolated"
fi

echo "- an instance of another private network is unreachable"
exo compute private-network create conformance-far \
  --start-ip "$FAR_START" --end-ip "$FAR_END" --netmask "$FAR_MASK" >/dev/null \
  || fail "the far network was not created"
far_id="$(boot conformance-far-worker)"
exo compute instance private-network attach conformance-far-worker conformance-far >/dev/null \
  || fail "far attach rejected"
far_ip="$(exo -O json compute private-network show conformance-far \
          | jq -r '.leases[] | select(.instance == "conformance-far-worker") | .ip_address')"
[ -n "$far_ip" ] || fail "the far attach produced no lease"
sleep 3

incus exec "$(machine "$far_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &
sleep 2

# Upstream, every private network is its own VXLAN segment, so this assertion
# has no same-VPC exception the way Scaleway's does. On a runtime that declares
# isolation it is hard: a declared capability that does not deliver is worse
# than an absent one. On any other it is a skip that names the mode, never a
# silent pass — docs/limits.md records why bridges cannot deliver it.
if incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$far_ip" 80 >/dev/null 2>&1; then
  if [ "$ISOLATION" = "true" ]; then
    fail "$far_ip is reachable from another private network, but $MACHINES declares isolation"
  fi
  skip "$far_ip is reachable from another private network: $MACHINES does not isolate segments (see docs/limits.md)"
else
  ok "an instance of another private network is unreachable ($far_ip)"
fi

echo "- detach, delete, and the records go"
exo compute instance private-network detach conformance-probe conformance-net >/dev/null \
  || fail "probe detach rejected"
exo compute instance private-network detach conformance-guard conformance-net >/dev/null \
  || fail "guard detach rejected"
exo compute instance private-network detach conformance-far-worker conformance-far >/dev/null \
  || fail "far detach rejected"
exo -Q compute private-network delete conformance-net --force >/dev/null \
  || fail "network delete rejected"
exo -Q compute private-network delete conformance-far --force >/dev/null \
  || fail "far network delete rejected"
ok "networks deleted"

echo "conformance: exoscale network passed"
