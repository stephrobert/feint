#!/usr/bin/env bash
# The dataplane witness gate (#486): what the API claims, the runtime shows.
#
# #475, #481, #483 and #484 were four green runs hiding a runtime that did not
# hold what the API described — no rule set behind a security group, a
# capability answered for a pack that hands nothing over, a balancer registered
# and distributing nothing, an instance `running` that never started. Each was
# found by a person reading the host. This gate is that person, as a program:
#
#   claim (per provider, from the emulator)      witness (from the runtime)
#   ------------------------------------------   -------------------------------
#   a machine-kind resource is `running`         `incus list` holds its machine,
#                                                and the machine is Running (#484)
#   enforced.firewall names the pack, and a      the machine's NICs carry a rule
#   machine wears a restrictive group            set the emulator wrote (#475)
#   enforced.balancing names the pack, and a     the network holds a balancer
#   balancer has a listener and a backend        with a backend and a port (#483)
#
# And the fourth verdict, the one that makes this a gate rather than a demand:
# a pack that does NOT claim a dataplane is skipped with a message and exit 0.
# `capabilities.*` says what the runtime can do; `enforced.*` says which pack
# hands work to it; an undeclared half counts as absent and nothing is asserted
# that nobody promised (#481).
#
# The population is the example stacks — the same configurations every pull
# request applies — brought up here under a machine runtime with
# `feint up --runtime incus-ovn`, each on this suite's own port. Exoscale is
# absent from that population on purpose: since #525 no Terraform is pointed at
# the Exoscale pack (the published provider splits an apply between the
# emulator and a paying account — docs/limits.md), so its stack cannot be
# driven here. Its firewall handoff is witnessed on the host by
# tools/conformance/exoscale/network.sh instead.
#
# Where this runs, and why not elsewhere: it needs a machine runtime, so it
# belongs on the same terms as conformance:ssh and the environment suite —
# `mise run conformance:witness` by hand, and the incus-ovn leg of
# .github/workflows/runtime-proof.yml. It must never join a gate the CI runs
# without OVN: there it could not look, and a check that cannot look says so
# rather than passing.
#
# The verdict functions live in witnesslib.sh, held by witness_test.go against
# planted defects — each reader proves it can find before it judges, and each
# FAIL names the provider and the object it did not obtain.
#
# Usage: tools/conformance/witness.sh [stack ...]     (default: scaleway outscale)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ADDR="${FEINT_WITNESS_ADDR:-127.0.0.1:4597}"
ENDPOINT="http://$ADDR"
RUNTIME="${FEINT_WITNESS_RUNTIME:-incus-ovn}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/witnesslib.sh"

guard_local "$ENDPOINT"

echo "conformance: the dataplane witness gate on $ENDPOINT, runtime $RUNTIME"

# ---- doorstep: can anybody look? -------------------------------------------
#
# "No witness because nobody could look" and "no witness" are two different
# verdicts. A host without incus, or without the runtime this gate asks for,
# is the first one: said out loud, nothing measured, exit 0 — the same answer
# CapabilitiesOf gives for a silent driver.
for tool in jq curl; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed; this gate cannot read anything without it"
done
if ! command -v incus >/dev/null 2>&1; then
  skip "the incus client is not on PATH: nobody can look at the runtime, so NOTHING WAS MEASURED"
  exit 0
fi

FEINT="$(feint_binary)"
[ -x "$FEINT" ] || fail "no feint binary at $FEINT; run \`mise run build\` first"

if ! "$FEINT" doctor --vm "$RUNTIME" >/dev/null 2>&1; then
  echo "--- feint doctor --vm $RUNTIME ---" >&2
  "$FEINT" doctor --vm "$RUNTIME" >&2 || true
  skip "this host cannot deliver the $RUNTIME runtime (doctor above): nobody can look, so NOTHING WAS MEASURED"
  exit 0
fi

# The machines this gate boots must be able to answer, and a host still holding
# an earlier run's blocks would fail minutes in with a message that blames the
# network — both guards live in guard.sh and are asked at the doorstep, where
# the answer is cheap (#335, #375).
if ! "$FEINT" images --check --vm "$RUNTIME" >&2; then
  fail "the $RUNTIME runtime is missing images this gate boots. Run: $FEINT images --vm $RUNTIME"
fi
guard_leftovers_for "$RUNTIME" doorstep

# ---- the readers prove they can find before anything is judged --------------
echo "- the readers find planted witnesses"
witness_machine_reader_control
witness_acl_reader_control
witness_balancer_reader_control

# And one live round trip for the rule-set transport: a rule set planted on the
# host, read back through the same query and ownership test the firewall
# verdict uses, then removed. This is the control the false #475 was missing —
# a filter proven on synthetic input can still sit behind a broken transport.
live_acl_control() {
  local name="wtn-gate-control" doc
  incus network acl create "$name" >/dev/null 2>&1 \
    || fail "cannot look: planting the control rule set failed; the firewall verdict would be void"
  if ! incus query -X PUT /1.0/network-acls/"$name" \
      --data '{"description":"feint security group (gate control)","ingress":[],"egress":[],"config":{}}' >/dev/null 2>&1; then
    incus network acl delete "$name" >/dev/null 2>&1
    fail "cannot look: describing the control rule set failed; the firewall verdict would be void"
  fi
  doc="$(incus query /1.0/network-acls/"$name" 2>/dev/null)" \
    || { incus network acl delete "$name" >/dev/null 2>&1; fail "cannot look: reading the control rule set back failed"; }
  if ! printf '%s' "$doc" | jq -e '(.description // "") | startswith("feint security group")' >/dev/null; then
    incus network acl delete "$name" >/dev/null 2>&1
    fail "the ownership reader does not recognise a rule set the description marks as ours; every firewall absence it reported would be an instrument failure"
  fi
  incus network acl delete "$name" >/dev/null 2>&1 \
    || fail "the control rule set $name could not be removed; delete it by hand before rerunning"
  ok "a planted rule set was read back off the host, recognised, and removed"
}
live_acl_control

# ---- live transports for the verdicts ---------------------------------------
live_read_instance() { incus query "/1.0/instances/$1" 2>/dev/null; }
live_read_acl()      { incus query "/1.0/network-acls/$1" 2>/dev/null; }

# ---- claims: what each provider's own API says ------------------------------
#
# The claims come from the emulator because they are the subject; the witness
# never does. A provider this gate has no claims reader for must fail loudly
# the day it starts claiming — a gate that silently reads zero claims passes
# on the exact population it was built to judge.

api() { # path -> stdout, or the function fails the run
  curl -sf -H "X-Auth-Token: feint-witness" "$ENDPOINT$1" \
    || fail "cannot look: the emulator did not answer GET $1"
}
osc() { # action -> stdout
  curl -sf -X POST -H 'Content-Type: application/json' -d '{}' "$ENDPOINT/api/v1/$1" \
    || fail "cannot look: the emulator did not answer POST /api/v1/$1"
}

# machine_of <kind> <id> reads the backing machine's name from /_feint/state —
# the one place Runtime lives, kept out of every client-facing answer on
# purpose. STATE_JSON is captured per stack below.
machine_of() { # kind id
  jq -r --arg k "$1" --arg id "$2" \
    '[.resources[] | select(.Kind == $k and .ID == $id) | .Runtime.machine // ""] | first // ""' \
    <"$STATE_JSON"
}

claims_running() { # provider out_file
  local provider="$1" out="$2" id doc
  : >"$out"
  # `doc` is captured before jq reads it: `fail` inside `$( )` exits only the
  # subshell, so `for id in $(api … | jq …)` would turn a transport error into
  # an empty claim set — the exact two-outcome reader this gate forbids.
  case "$provider" in
    scaleway)
      doc="$(api /instance/v1/zones/fr-par-1/servers)" || exit 1
      for id in $(printf '%s' "$doc" | jq -r '.servers[] | select(.state == "running") | .id'); do
        printf '%s\t%s\n' "$id" "$(machine_of "instance/server" "$id")" >>"$out"
      done ;;
    outscale)
      doc="$(osc ReadVms)" || exit 1
      for id in $(printf '%s' "$doc" | jq -r '.Vms[] | select(.State == "running") | .VmId'); do
        printf '%s\t%s\n' "$id" "$(machine_of "vm" "$id")" >>"$out"
      done ;;
    *) fail "this gate has no running-machines claims reader for $provider; teach it before trusting that pack" ;;
  esac
}

# claims_firewall writes `<resource_id>\t<machine>` for every running machine
# wearing a group that restricts anything. A permissive group attaches nothing
# by design (FirewallSpec.EnforcesNothing), so it claims nothing here.
claims_firewall() { # provider out_file
  local provider="$1" out="$2" id sg restrictive
  : >"$out"
  local doc group rules
  case "$provider" in
    scaleway)
      declare -A verdict=()
      doc="$(api /instance/v1/zones/fr-par-1/servers)" || exit 1
      while IFS=$'\t' read -r id sg; do
        [ -n "$id" ] || continue
        if [ -z "${verdict[$sg]:-}" ]; then
          group="$(api "/instance/v1/zones/fr-par-1/security_groups/$sg")" || exit 1
          restrictive="$(printf '%s' "$group" \
            | jq -r '.security_group | ((.inbound_default_policy // "accept") != "accept") or ((.outbound_default_policy // "accept") != "accept")')"
          if [ "$restrictive" != "true" ]; then
            rules="$(api "/instance/v1/zones/fr-par-1/security_groups/$sg/rules")" || exit 1
            restrictive="$(printf '%s' "$rules" \
              | jq -r '[.rules[]? | select(.action == "drop")] | length > 0')"
          fi
          verdict[$sg]="$restrictive"
        fi
        if [ "${verdict[$sg]}" = "true" ]; then
          printf '%s\t%s\n' "$id" "$(machine_of "instance/server" "$id")" >>"$out"
        fi
      done < <(printf '%s' "$doc" \
        | jq -r '.servers[] | select(.state == "running") | [.id, .security_group.id] | @tsv') ;;
    outscale)
      # Every Outscale group is restrictive on the runtime: the pack derives
      # its spec with DefaultIngress drop unconditionally (firewall.go), the
      # provider's own inbound-deny-by-default semantics.
      doc="$(osc ReadVms)" || exit 1
      for id in $(printf '%s' "$doc" \
          | jq -r '.Vms[] | select(.State == "running" and ((.SecurityGroups // []) | length > 0)) | .VmId'); do
        printf '%s\t%s\n' "$id" "$(machine_of "vm" "$id")" >>"$out"
      done ;;
    *) fail "this gate has no firewall claims reader for $provider; teach it before trusting that pack's enforced.firewall" ;;
  esac
}

# claims_balancing writes one balancer name per line, for balancers the API
# lists as placed, listening and holding at least one registered backend —
# what less than that promises no dataplane.
claims_balancing() { # provider out_file
  local provider="$1" out="$2" doc
  : >"$out"
  case "$provider" in
    outscale)
      doc="$(osc ReadLoadBalancers)" || exit 1
      printf '%s' "$doc" | jq -r '.LoadBalancers[]?
        | select(((.Listeners // []) | length > 0)
             and ((.BackendVmIds // []) | length > 0)
             and ((.Subnets // []) | length > 0))
        | .LoadBalancerName' >>"$out" ;;
    *) fail "this gate has no balancer claims reader for $provider; teach it before trusting that pack's enforced.balancing" ;;
  esac
}

# ---- one stack: up, claim, witness, down ------------------------------------

WORK=""
UP=""
cleanup() {
  # Down runs on the failure path too: a leftover emulator holds the port and
  # its machines, and the next run measures the leftovers instead of the code.
  if [ -n "$UP" ] && [ -n "$WORK" ]; then
    (cd "$WORK" && "$FEINT" down >/dev/null 2>&1)
  fi
  [ -n "$WORK" ] && rm -rf "$WORK"
  WORK=""
  UP=""
}
trap cleanup EXIT INT TERM

run_stack() { # name
  local name="$1"
  local src="$ROOT/examples/stacks/$name"
  [ -d "$src" ] || fail "no stack at $src"

  local provider machine_kind floor
  provider="$(awk '/^  provider:/ {print $2; exit}' "$src/feint.yaml")"
  case "$provider" in
    scaleway) machine_kind="instance/server" ;;
    outscale) machine_kind="vm" ;;
    *) fail "this gate does not know the machine kind of provider '$provider'" ;;
  esac
  # The population floor comes from the stack's own declaration: a claims
  # reader that finds fewer running machines than the stack's ready conditions
  # promise is a broken reader, not an empty cloud.
  floor="$(grep -o "resource:$machine_kind:[0-9]*" "$src/feint.yaml" | head -n1 | awk -F: '{print $NF}')"
  [ -n "$floor" ] || fail "the $name stack declares no resource:$machine_kind ready condition; the claims reader would have no floor to be held to"

  echo "- $name: feint up --runtime $RUNTIME"
  WORK="$(mktemp -d)"
  cp "$src"/*.tf "$src/feint.yaml" "$WORK/"
  if [ -d "$src/modules" ]; then
    cp -R "$src/modules" "$WORK/"
  fi
  sed -i "s|127.0.0.1:4599|$ADDR|g" "$WORK/feint.yaml"

  local up_log="$WORK/up.log"
  (cd "$WORK" && "$FEINT" up --runtime "$RUNTIME" --timeout 420s) >"$up_log" 2>&1 \
    || { tail -n 40 "$up_log" >&2; fail "$name: feint up failed (log above)"; }
  UP="yes"
  ok "up, applied, every ready condition confirmed"

  local health
  health="$(curl -sf "$ENDPOINT/_feint/health")" || fail "$name: the emulator does not answer /_feint/health"
  [ "$(printf '%s' "$health" | jq -r '.machines // "none"')" != "none" ] \
    || fail "$name: up was asked for --runtime $RUNTIME and health says no machine runtime; a gate that went on would witness nothing"

  STATE_JSON="$WORK/state.json"
  curl -sf "$ENDPOINT/_feint/state" >"$STATE_JSON" || fail "$name: the emulator does not answer /_feint/state"

  # The witness side, captured once per stack, transport errors their own
  # verdict: `incus list | reader` would let a failed list read as an empty
  # host.
  local instances_json="$WORK/instances.json"
  incus list -f json >"$instances_json" 2>/dev/null \
    || fail "cannot look: incus list failed; no witness because nobody could look is not 'no witness'"
  incus network list -f json >"$WORK/networks.json" 2>/dev/null \
    || fail "cannot look: incus network list failed; no witness because nobody could look is not 'no witness'"

  echo "- $name: every resource the API calls running has a Running machine"
  local claims="$WORK/claims-running.tsv"
  claims_running "$provider" "$claims"
  local found
  found="$(wc -l <"$claims")"
  [ "$found" -ge "$floor" ] \
    || fail "$name: the claims reader found $found running $machine_kind resource(s) where the stack's own ready conditions promise at least $floor — the reader is the suspect, not the cloud"
  witness_running_has_machines "$provider" "$claims" "$instances_json"

  echo "- $name: a claimed firewall leaves rule sets on the host"
  if [ "$(printf '%s' "$health" | jq -r '.capabilities.firewall // false')" != "true" ]; then
    skip "$provider: this runtime does not declare firewall; nothing was promised here, nothing is demanded"
  elif ! printf '%s' "$health" | witness_enforced "$provider" firewall; then
    skip "$provider does not declare enforced.firewall — a property it never promised is not demanded of it (#481)"
  else
    claims_firewall "$provider" "$claims"
    [ -s "$claims" ] \
      || fail "$name: $provider claims enforced.firewall and the claims reader found no running machine wearing a restrictive group, on a stack that attaches them — the reader is the suspect"
    witness_firewalled_machines "$provider" "$claims" live_read_instance live_read_acl
  fi

  echo "- $name: a claimed balancer distributes on the host"
  if [ "$(printf '%s' "$health" | jq -r '.capabilities.balancing // false')" != "true" ]; then
    skip "$provider: this runtime does not declare balancing; a balancer here records its configuration and that is the documented degraded mode"
  elif ! printf '%s' "$health" | witness_enforced "$provider" balancing; then
    skip "$provider does not declare enforced.balancing — a property it never promised is not demanded of it (#481)"
  else
    claims_balancing "$provider" "$claims"
    [ -s "$claims" ] \
      || fail "$name: $provider claims enforced.balancing and the claims reader found no placed, listening balancer with a backend, on a stack that builds one — the reader is the suspect"
    local balancers="$WORK/balancers.tsv" net doc
    : >"$balancers"
    for net in $(jq -r --arg p "$provider" \
        '.[] | select((.config["user.feint.provider"] // "") == $p) | .name' <"$WORK/networks.json"); do
      doc="$(incus query "/1.0/networks/$net/load-balancers?recursion=1" 2>/dev/null)" \
        || fail "cannot look: reading the balancers of $net failed"
      printf '%s' "$doc" | witness_balancers >>"$balancers"
    done
    witness_balancers_delivered "$provider" "$claims" "$balancers"
  fi

  echo "- $name: feint down"
  (cd "$WORK" && "$FEINT" down) >"$WORK/down.log" 2>&1 \
    || { tail -n 20 "$WORK/down.log" >&2; fail "$name: feint down failed"; }
  UP=""
  if curl -sf --max-time 2 "$ENDPOINT/_feint/health" >/dev/null 2>&1; then
    fail "$name: down returned and something still answers on $ADDR"
  fi
  ok "down, nothing answers on $ADDR"
  rm -rf "$WORK"
  WORK=""
}

STACKS=("$@")
[ "${#STACKS[@]}" -gt 0 ] || STACKS=(scaleway outscale)
for stack in "${STACKS[@]}"; do
  run_stack "$stack"
done

echo "conformance: every claimed dataplane had its witness on the runtime, and every unclaimed one was skipped by name"
