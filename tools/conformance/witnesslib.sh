#!/usr/bin/env bash
# The dataplane witness verdicts (#486), shared between the gate and its tests.
#
# Four issues in one evening — #475, #481, #483, #484 — were four instances of
# one absent rule: when the emulator claims a resource has a dataplane, a
# witness of that resource must be observable on the runtime, and the claim is
# per provider, never per runtime. These functions are that rule, written once.
#
# Three disciplines, each paid for before it was written down:
#
#   1. Every reader proves it can find before it judges. An ACL reader
#      filtering on the *network* prefix reported 0 rule sets on a run that
#      carried six; filed as written, that would have been a false #475
#      against Scaleway. So each reader ships a `*_control` that plants a
#      witness the reader must find — and a near-miss it must not.
#   2. Three outcomes, never two. A transport error (`incus` absent, a query
#      refused) is "nobody could look", which is a different verdict from
#      "no witness". Every caller of a live read checks the call's own exit
#      code before reading its output as an absence.
#   3. The witness is read from the runtime, never from the API. The claims
#      come from the emulator — they are the subject — and the witness comes
#      from `incus`. Asking the emulator whether the emulator is right is the
#      failure #486 exists to remove.
#
# Sourced by tools/conformance/witness.sh and driven, with a stub `incus`, by
# tools/conformance/witness_test.go. Every function expects `ok`, `fail` and
# `skip` to be defined by the caller; readers are filters over stdin so the
# tests can hold them against planted input without any host at all.

# ---- readers: filters over stdin, no side effects ---------------------------

# witness_machine_status reads `incus list -f json` from stdin and prints the
# status of exactly the named instance, or "absent".
#
# Exactly: `.name == $n`, never a substring. `incus list | grep -q "$name"` was
# wrong twice in this repository's history (#219): a leftover machine from
# another run satisfied it, and 10.1.1.4 was found inside 10.1.1.44.
witness_machine_status() { # name
  jq -r --arg n "$1" '[.[] | select(.name == $n) | .status] | first // "absent"'
}

# witness_instance_acls reads one instance (`incus query /1.0/instances/<m>`)
# from stdin and prints every rule-set name its NICs carry, one per line.
# security.acls is a comma-separated list on the wire.
witness_instance_acls() {
  jq -r '[.expanded_devices // {} | to_entries[]
          | select(.value.type == "nic")
          | (.value["security.acls"] // "")
          | split(",")[] | select(. != "")] | .[]?'
}

# witness_balancers reads one network's load balancers
# (`incus query /1.0/networks/<net>/load-balancers?recursion=1`) from stdin and
# prints one line per balancer the emulator owns: `<name>\t<backends>\t<ports>`.
#
# Ownership is the description EnsureBalancer writes — "feint load balancer
# <name>" — never a name pattern: a balancer is keyed by listen address on the
# host, and the description is the only place the emulated name survives.
witness_balancers() {
  jq -r '.[] | select((.description // "") | startswith("feint load balancer "))
         | [(.description | ltrimstr("feint load balancer ")),
            ((.backends // []) | length), ((.ports // []) | length)] | @tsv'
}

# witness_enforced reads /_feint/health from stdin and answers 0 when the
# provider claims the capability, 1 otherwise.
#
# `// []` on purpose: a build older than `enforced` never claimed anything, and
# an undeclared capability counts as absent — the check must skip, not assert
# what nobody promised (#481).
witness_enforced() { # provider capability
  jq -e --arg p "$1" --arg c "$2" \
    '(.enforced[$c] // []) | index($p) != null' >/dev/null
}

# ---- the controls: each reader finds a planted witness, and only it ---------
#
# Run before any verdict. A reader that cannot find its planted witness voids
# every absence it would report, so a failed control is a FAIL about the
# instrument, in as many words — never a green, never an absence.

witness_machine_reader_control() {
  local found
  found="$(printf '%s' '[{"name":"feint-wtn-control","status":"Running"},{"name":"feint-wtn-contro","status":"Stopped"}]' \
    | witness_machine_status feint-wtn-control)"
  [ "$found" = "Running" ] \
    || fail "the machine reader cannot find a planted witness (got '$found'); every absence it reported would be an instrument failure, not a measurement"
  # The near-miss: a name one character short must stay absent, or a substring
  # match would let another run's machine answer for this one.
  found="$(printf '%s' '[{"name":"feint-wtn-control-2","status":"Running"}]' \
    | witness_machine_status feint-wtn-control)"
  [ "$found" = "absent" ] \
    || fail "the machine reader matched 'feint-wtn-control' inside 'feint-wtn-control-2'; a substring verdict is another run's machine answering for this one"
  ok "the machine reader finds a planted witness, and only it"
}

witness_acl_reader_control() {
  local out
  out="$(printf '%s' '{"expanded_devices":{"eth0":{"type":"nic","security.acls":"osc-planted1,osc-planted2"},"root":{"type":"disk"}}}' \
    | witness_instance_acls | sort | tr '\n' ' ')"
  [ "$out" = "osc-planted1 osc-planted2 " ] \
    || fail "the rule-set reader cannot find planted witnesses on a NIC (got '$out'); a 0 it reported would be a false #475"
  ok "the rule-set reader finds planted rule sets on a NIC"
}

witness_balancer_reader_control() {
  local out
  out="$(printf '%s' '[{"listen_address":"10.0.0.9","description":"feint load balancer wtn-control","backends":[{}],"ports":[{}]},{"listen_address":"10.0.0.8","description":"an operator balancer"}]' \
    | witness_balancers)"
  [ "$out" = "$(printf 'wtn-control\t1\t1')" ] \
    || fail "the balancer reader cannot find a planted witness, or reads a foreign one (got '$out'); an absence it reported would be a false #483"
  ok "the balancer reader finds a planted balancer, and leaves the operator's alone"
}

# ---- the verdicts -----------------------------------------------------------

# witness_running_has_machines is #484 as a gate: every resource the API calls
# running has a machine on the runtime, and that machine is Running.
#
# claims_file: one line per claim, `<resource_id>\t<machine_name>`.
# instances_json: the file `incus list -f json` was captured into — captured by
# the caller so a transport failure is its own verdict, never an absence.
witness_running_has_machines() { # provider claims_file instances_json
  local provider="$1" claims="$2" instances="$3"
  local id name status count=0
  while IFS=$'\t' read -r id name; do
    [ -n "$id" ] || continue
    count=$((count + 1))
    if [ -z "$name" ]; then
      fail "$provider says $id is running and recorded no machine for it — a claimed dataplane with nothing behind it (#484)"
    fi
    status="$(witness_machine_status "$name" <"$instances")"
    if [ "$status" = "absent" ]; then
      fail "$provider says $id is running, but no machine $name exists on the runtime (#484)"
    fi
    if [ "$status" != "Running" ]; then
      fail "$provider says $id is running, but machine $name is $status on the runtime — the API state and the host disagree (#484)"
    fi
  done <"$claims"
  ok "$provider: $count running resource(s), each with a Running machine on the host"
}

# witness_firewalled_machines is #475 as a gate: a provider that claims
# enforced.firewall hands a rule set to the host for every machine whose group
# restricts anything.
#
# claims_file: `<resource_id>\t<machine_name>`, already filtered by the caller
# to machines wearing a restrictive group — a permissive group attaches
# nothing on purpose (machine.FirewallSpec.EnforcesNothing), and demanding a
# witness for it would demand what nobody promised.
#
# read_instance is the transport: a function/command printing
# `incus query /1.0/instances/<m>` to stdout, injected so the tests can stub
# it and so a real transport failure is told apart from an absence.
witness_firewalled_machines() { # provider claims_file read_instance read_acl
  local provider="$1" claims="$2" read_instance="$3" read_acl="$4"
  local id name doc acls acl owned count=0
  while IFS=$'\t' read -r id name; do
    [ -n "$id" ] || continue
    [ -n "$name" ] || fail "$provider claims enforced.firewall and $id has no machine to inspect; run the running-machines verdict first"
    count=$((count + 1))
    if ! doc="$("$read_instance" "$name")"; then
      fail "cannot look: reading machine $name off the runtime failed; no witness because nobody could look is not 'no witness'"
    fi
    acls="$(printf '%s' "$doc" | witness_instance_acls)"
    if [ -z "$acls" ]; then
      fail "$provider claims enforced.firewall, and machine $name (resource $id) carries no rule set on any NIC — the API describes a closed port the host answers on (#475)"
    fi
    # At least one attached set must be the emulator's own, read from the
    # description EnsureFirewall writes. An operator's hand-made ACL on the
    # same NIC must not answer for the pack.
    owned=""
    while IFS= read -r acl; do
      [ -n "$acl" ] || continue
      if ! doc="$("$read_acl" "$acl")"; then
        fail "cannot look: reading rule set $acl off the runtime failed; no witness because nobody could look is not 'no witness'"
      fi
      if printf '%s' "$doc" | jq -e '(.description // "") | startswith("feint security group")' >/dev/null; then
        owned="$acl"
        break
      fi
    done <<<"$acls"
    if [ -z "$owned" ]; then
      fail "$provider claims enforced.firewall, and machine $name (resource $id) carries rule sets, but none the emulator wrote — the pack handed nothing over (#475)"
    fi
  done <"$claims"
  ok "$provider: $count machine(s) wearing a restrictive group, each carrying an emulator rule set on the host"
}

# witness_balancers_delivered is #483 as a gate: every balancer the API lists
# with a listener and a registered backend exists on the runtime and holds at
# least one backend and one port — registered-and-empty is the lie.
#
# claims_file: one balancer name per line. balancers_file: the concatenation of
# every provider-owned network's balancers, one `<name>\t<backends>\t<ports>`
# line each, captured by the caller through witness_balancers.
witness_balancers_delivered() { # provider claims_file balancers_file
  local provider="$1" claims="$2" balancers="$3"
  local wanted line found backends ports count=0
  while IFS= read -r wanted; do
    [ -n "$wanted" ] || continue
    count=$((count + 1))
    found=""
    while IFS=$'\t' read -r line backends ports; do
      [ "$line" = "$wanted" ] || continue
      found="yes"
      if [ "${backends:-0}" -lt 1 ] || [ "${ports:-0}" -lt 1 ]; then
        fail "$provider registered balancer $wanted and the runtime holds it empty — $backends backend(s), $ports port(s): registered and distributing nothing (#483)"
      fi
    done <"$balancers"
    if [ -z "$found" ]; then
      fail "$provider registered balancer $wanted with a listener and a backend, and the runtime holds no balancer for it (#483)"
    fi
  done <"$claims"
  ok "$provider: $count balancer(s) the API lists, each held by the runtime with a backend and a port"
}
