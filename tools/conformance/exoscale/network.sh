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
# What used to stand here, and what it cost. Until 2026-08-27 this header said
# that firewall assertions were deliberately absent "because the Exoscale pack
# does not yet sync its security groups onto the machines, so a rule assertion
# would measure nothing". That stopped being true at a344f8d (#494/#475), when
# the packs began handing their groups to the host — and the sentence stayed,
# read as a live fact, steering every reader of this file away from the
# firewall. It is the exact shape CLAUDE.md names: a control's obituary read as
# a measurement.
#
# What it hid is #574. The pack applied the `default` group's rule set to every
# interface, the private-network NIC included, where Exoscale states the
# opposite in as many words — "Security group rules do not apply to traffic
# inside private networks". The group carries no ingress rule, so the NIC ended
# up with a drop default and two instances of one segment could not reach each
# other under `--vm incus`: 0/10 probes, and 10/10 with the rule set stripped.
# The reachability assertion below is what fails when that comes back, and it
# fails only under the bridge — under OVN the same wrong rule is outranked by
# the sender's catch-all egress allow (#491), so a green there proves nothing
# about the rule being absent.
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

# Shared assertions about what a machine carries. See the file for why the
# comparison is not written three times.
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/addresses.sh"
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/verdicts.sh"
# Waiting for a condition instead of for a duration (#459). The file states the
# one rule that decides where these may stand, and why the waits still written
# `sleep` below cannot become polls.
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/waiting.sh"

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

# The list lives in a file rather than in an array, and that is the fix for a
# sweep that swept nothing (#219): boot() ends with `echo "$id"` and every caller
# writes `id="$(boot …)"`, which is a subshell — so `cleanup_ids+=("$id")` inside
# it updated a copy that died with the subshell, and the trap ran on an empty
# list. Every machine the suite created stayed on the operator's host, and the
# suite reported a clean exit.
cleanup_init
cleanup() {
  while read -r id; do
    [ -n "$id" ] || continue
    curl -sf -X DELETE "$ENDPOINT/v2/instance/$id" >/dev/null 2>&1 || true
  done < <(cleanup_list)
  cleanup_done
  sweep_runtime || return
  if ! "$FEINT_BIN" clean --vm "${FEINT_VM:-incus}" 2>&1 | grep -q "nothing was left behind"; then
    echo "FAIL: the runtime is still not clean after the sweep" >&2
  fi
}
trap cleanup EXIT

# And the host must not already be holding a block this suite is about to ask
# for (#375). The sweep below meets that state and cannot fix it — the leftover
# belongs to the runtime's user — so the answer is given here, before the run,
# rather than as a sweep failure whose remedy nobody runs.
#
# After the trap, deliberately, and that ordering was measured: put before it,
# the refusal exits without the EXIT trap installed, so the sweep that removes
# this run's own labelled objects never happens and the operator is left with a
# host dirtier than the old failure left it. Refusing must cost the host
# nothing.
guard_leftovers "$ENDPOINT"

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
  # --public-ip none, because this suite measures a private network and an
  # instance that also carries a public address carries two, which is faithful
  # to the cloud and not what is being measured here. The real Exoscale assigns
  # one unless asked otherwise (public-ip-assignment defaults to inet4, and the
  # CLI's own default says so), so asking is the client's job rather than the
  # emulator's to skip.
  exo compute instance create "$name" --zone "$EXOSCALE_ZONE" \
    --template "Linux Ubuntu 24.04 LTS 64-bit" --instance-type standard.tiny \
    --public-ip none >/dev/null \
    || fail "instance $name was not created"
  id="$(exo -O json compute instance list | jq -r --arg n "$name" '.[] | select(.name == $n) | .id')"
  [ -n "$id" ] || fail "instance $name is not in the list after create"
  cleanup_add "$id"
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
echo "- the machine carries the lease the API published"
# Asked until the lease is on the interface rather than after a guess at how
# long a container takes to configure one. The verdict below is unchanged.
wait_until 60 machine_carries "$(machine "$guard_id")" "$guard_ip" || true
carried="$(incus query "/1.0/instances/$(machine "$guard_id")/state" 2>/dev/null \
           | jq -r '[.network[]?.addresses[]? | select(.family=="inet") | .address] | join(" ")')"
case " $carried " in
  *" $guard_ip "*) ok "guard carries $guard_ip" ;;
  *) fail "guard carries '${carried:-nothing}', the API published $guard_ip" ;;
esac

echo "- the machine carries no address the API does not publish"
# Exoscale publishes an instance's public address as ip_address and each private
# network lease under private-networks[].ip. Nothing else may exist on the
# machine: an instance carrying two addresses where the API named one is what
# this suite let through until #202.
# ip_address is the public one; the private-network lease is $guard_ip, which
# this suite already resolved from the network's own lease list a few lines up.
# Reading it back out of `instance show` was a detour and a wrong one: the CLI
# renders private_networks as a list of names, not of objects, so the jq for
# `.ip` failed with "Cannot index string with string".
exo_public="$(exo -O json compute instance show "$guard_id" 2>/dev/null \
  | jq -r '.ip_address // empty' | grep -v '^-$' || true)"
# shellcheck disable=SC2086 # the published list is several arguments on purpose
assert_only_published "$(machine "$guard_id")" $exo_public "$guard_ip"

echo "- an instance of the same private network is reachable"
incus exec "$(machine "$guard_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &
# No separate wait for the responder: the verdict below is a REACH, so polling
# it covers the listener coming up as well, and it probes once per attempt
# instead of twice. Reachable within thirty seconds satisfies the property this
# check states, where the fixed two seconds would have failed a segment that
# took three.
near_reach() { incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$guard_ip" 80 >/dev/null 2>&1; }
if wait_until 30 near_reach; then
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
# The attach has to reach the machine before a responder started in it means
# anything, and both are questions: the machine answers `incus exec`, then the
# responder is bound. The positive control below is the verdict either way.
wait_until 60 incus exec "$(machine "$far_id")" -- true || true

incus exec "$(machine "$far_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &

# Upstream, every private network is its own VXLAN segment, so this assertion
# has no same-VPC exception the way Scaleway's does. On a runtime that declares
# isolation it is hard: a declared capability that does not deliver is worse
# than an absent one. On any other it is a skip that names the mode, never a
# silent pass — docs/limits.md records why bridges cannot deliver it.
# The positive control, before the negative verdict: see the Scaleway suite and
# #219. A listener that never started refuses a connection exactly as isolation
# does, so it is proved live on its own loopback first.
assert_listening_within 30 "$(machine "$far_id")" 80 "the instance of the other private network"

if incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$far_ip" 80 >/dev/null 2>&1; then
  if [ "$ISOLATION" = "true" ]; then
    fail "$far_ip is reachable from another private network, but $MACHINES declares isolation"
  fi
  skip "$far_ip is reachable from another private network: $MACHINES does not isolate segments (see docs/limits.md)"
else
  ok "an instance of another private network is unreachable ($far_ip)"
fi

# The closing condition of EXO-7 (#232), and the only one a unit test cannot
# reach: scaling a pool moves the number of machines the **runtime** holds, not
# only the number the API reports. A pool answering `size: 3` while one container
# exists is the exact failure that batch was deferred over, and the question is
# asked of incus rather than of the emulator.
echo "- scaling a pool moves the machines the runtime holds"
pool_machines() { # -> how many containers of this pool exist
  local n=0 member
  for member in $(exo -O json compute instance-pool show conformance-pool 2>/dev/null \
                  | jq -r '.instances[]? // empty'); do
    # `show` renders members by name; the machine is named from the instance id,
    # so the id is resolved through the instance list rather than guessed.
    local id
    id="$(exo -O json compute instance list | jq -r --arg n "$member" \
          '.[] | select(.name == $n or .id == $n) | .id' | head -1)"
    [ -n "$id" ] || continue
    incus info "$(machine "$id")" >/dev/null 2>&1 && n=$((n + 1))
  done
  echo "$n"
}

# The same template and type boot() names, spelled out rather than carried in a
# variable that does not exist: this suite has no inventory step, and inventing
# one here would be a second source for what boot() already decided.
exo -Q compute instance-pool create conformance-pool --size 2 \
  --template "Linux Ubuntu 24.04 LTS 64-bit" --instance-type standard.tiny \
  --disk-size 10 >/dev/null \
  || fail "instance-pool create rejected"
for id in $(exo -O json compute instance list \
            | jq -r '.[] | select(.name | startswith("conformance-pool-")) | .id'); do
  cleanup_add "$id"
done
# The count is the condition, so the count is what is waited on. Every verdict
# below is unchanged and still fails on a pool whose machines never appear or
# never go; what goes is the four fixed waits that paid the worst case on every
# run. `pool_machines` costs two `exo` calls per member, which is why the
# question is asked through one function rather than inlined.
pool_holds() { [ "$(pool_machines)" = "$1" ]; }
wait_until 60 pool_holds 2 || true
[ "$(pool_machines)" = "2" ] \
  || fail "a pool of size 2 is backed by $(pool_machines) machine(s): the control plane promised what the runtime does not hold"
ok "two members, two machines"

exo -Q compute instance-pool scale conformance-pool 3 --force >/dev/null || fail "scale up rejected"
for id in $(exo -O json compute instance list \
            | jq -r '.[] | select(.name | startswith("conformance-pool-")) | .id'); do
  cleanup_add "$id"
done
wait_until 60 pool_holds 3 || true
[ "$(pool_machines)" = "3" ] \
  || fail "after scaling to 3 the runtime holds $(pool_machines) machine(s)"
ok "scaled up, and the third machine really started"

exo -Q compute instance-pool scale conformance-pool 1 --force >/dev/null || fail "scale down rejected"
wait_until 60 pool_holds 1 || true
[ "$(pool_machines)" = "1" ] \
  || fail "after scaling to 1 the runtime still holds $(pool_machines) machine(s): a scale down that leaves containers behind is a bill nobody asked for"
ok "scaled down, and the machines went with the members"

exo -Q compute instance-pool delete conformance-pool --force >/dev/null || fail "pool delete rejected"
wait_until 60 pool_holds 0 || true
[ "$(pool_machines)" = "0" ] || fail "the deleted pool left $(pool_machines) machine(s) running"
ok "deleted, and the runtime holds nothing of it"

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
