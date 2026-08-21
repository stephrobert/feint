#!/usr/bin/env bash
# Refuse to run a client that is not pointed at a local emulator.
#
# This exists because it happened. A test script called `eval "$(feint env
# scaleway)"`, the command was broken and printed nothing, the eval silently
# succeeded on an empty string, and `scw instance server create` fell back to the
# operator's real ~/.config/scw/config.yaml. A DEV1-S server and a flexible IP
# were created on a paying account, and the command exited 0 the whole way.
#
# That is the exact failure this project exists to prevent — a client leaving for
# the real cloud because an address was missing — produced by the tool meant to
# prevent it. The lesson is not "be careful with eval". It is that **an empty
# environment must fail loudly rather than fall through to a real account**, and
# nothing but an explicit check can enforce that: every official client is
# designed to find credentials elsewhere when the environment has none.
#
# Source this at the top of any script that drives a real client:
#
#   . "$(dirname "${BASH_SOURCE[0]}")/guard.sh"
#   guard_local "$ENDPOINT"
#   guard_no_real_profile SCW_API_URL scw
#   guard_images "$ENDPOINT"          # suites that log into a machine
#   guard_leftovers "$ENDPOINT"       # suites that take an address block
#
# Usage of the functions is deliberately separate: the first checks where the
# script intends to go, the second checks that the client cannot go anywhere
# else, the third checks that the emulator can keep the promise the suite is
# about to make, and the fourth checks that the host is not already holding the
# blocks it is about to ask for. All four live here rather than in each suite
# for the reason CLAUDE.md gives about the shared layer: a control copied into
# three scripts is a control the fourth forgets.

# guard_local refuses an endpoint that is not on this machine.
#
# A conformance suite pointed at a remote address is either a mistake or an
# attack on somebody's account; there is no legitimate third case. Loopback and
# a bare host with no dots are what a local emulator looks like.
guard_local() {
  local endpoint="${1:-}"
  case "$endpoint" in
    http://127.0.0.1:*|http://localhost:*|http://\[::1\]:*|http://0.0.0.0:*) return 0 ;;
    "")
      echo "FAIL: no endpoint given; refusing to run a client that would find its own" >&2
      exit 1 ;;
    *)
      echo "FAIL: endpoint $endpoint is not local; this suite drives an emulator, never a real cloud" >&2
      exit 1 ;;
  esac
}

# guard_no_real_profile refuses to run when the variable that redirects a client
# is unset, because every one of these clients falls back to a stored profile.
#
# The variable name and the client are passed in rather than listed here: the
# core of this project knows no provider and neither does this file.
guard_no_real_profile() {
  local var="$1" client="${2:-the client}"
  if [ -z "${!var:-}" ]; then
    cat >&2 <<EOF
FAIL: $var is not set.

$client falls back to its stored credentials when the environment says nothing,
so running it now would drive a real account. This is not hypothetical: it is how
a server and a flexible IP were once created on a paying account by a test of
this emulator.

Set $var to the local emulator, or use a client flag that pins the endpoint.
EOF
    exit 1
  fi
  case "${!var}" in
    http://127.0.0.1:*|http://localhost:*|http://\[::1\]:*|http://0.0.0.0:*) return 0 ;;
    *)
      echo "FAIL: $var is ${!var}, which is not local; refusing to run $client" >&2
      exit 1 ;;
  esac
}

# feint_binary answers where the binary under test is, as a path somebody can
# paste. FEINT_BIN wins when it is set; otherwise it is the repository root
# beside this file.
#
# Normalised, and that is not tidiness: both guards below print this path inside
# a command they are asking an operator to run. The suites derive FEINT_BIN from
# their own directory, so it arrives as `.../scaleway/../../../feint` and the
# remedy comes out unpasteable — which is #375's own lesson one size down, a
# remedy nobody runs being the same as no remedy. `cd` failing leaves the
# candidate alone rather than inventing a path, so an unreadable directory still
# reaches the "no feint binary at" refusal with the name it was given.
feint_binary() {
  local candidate dir
  candidate="${FEINT_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)/feint}"
  if dir="$(cd "$(dirname "$candidate")" 2>/dev/null && pwd -P)"; then
    candidate="$dir/${candidate##*/}"
  fi
  printf '%s' "$candidate"
}

# guard_images refuses a suite whose machines cannot answer, and names the one
# command that fixes it (#335).
#
# The emulator boots its own images because no upstream image carries an ssh
# daemon (#203). When it holds none, it falls back to the upstream one and says
# so in its log, once per boot:
#
#   WARN no image of ours for this system, booting the upstream one ...
#        fix="feint images"
#
# That fallback used to be a degradation. Since #202 gave a machine exactly the
# one address its provider's API publishes, on a routed NIC with no NAT, it is
# not: the machine has no route to a package repository, cloud-init's
# `apt-get install openssh-server` dies on DNS, and nothing ever listens on
# port 22. Measured on 2026-08-20 by hiding the five feint/* aliases on a host
# that had them: the same suite passed in 21s with the images and failed in 93s
# without, on the emulator's own message, "no ssh daemon answered ... the
# published address is a promise nobody keeps".
#
# runtime-proof.yml had been red on that line for five consecutive scheduled
# nights, with the fix printed in its own log the whole time and nothing reading
# it (#335, blocking the streak #125 counts). So this reads it: the suite either
# has its images or refuses here, naming them, rather than starting and failing
# thirty lines later on an ssh error that blames the network.
#
# TestTheImageGuardRefusesASuiteWhoseMachinesCannotAnswer fails without it, and
# tools/falsify/specs/ssh-suite-needs-its-images.json replays that.
guard_images() {
  local endpoint="${1:-}"
  local health machines binary

  health="$(curl -sf "$endpoint/_feint/health" || true)"
  if [ -z "$health" ]; then
    echo "FAIL: $endpoint answered no health payload, so nothing here knows what its machines are" >&2
    exit 1
  fi
  machines="$(printf '%s' "$health" | jq -r '.machines // "none"')"

  # With no runtime the login step is skipped by design, and there is nothing to
  # boot an image from. Said out loud rather than returned in silence: a
  # precondition that passes quietly on the very case it exists for is how a
  # check gets read as green when it never ran.
  if [ "$machines" = "none" ] || [ "$machines" = "null" ] || [ -z "$machines" ]; then
    echo "  images: not needed, $endpoint runs no machine runtime" >&2
    return 0
  fi

  binary="$(feint_binary)"
  if [ ! -x "$binary" ]; then
    cat >&2 <<EOF
FAIL: no feint binary at $binary.

This suite needs it to ask what images the $machines runtime holds. Build it
(mise run build, or go build -o feint ./cmd/feint), or point FEINT_BIN at one.
Skipping the question instead would let the suite start without its images,
which is the failure this check exists to name.
EOF
    exit 1
  fi

  if ! "$binary" images --check --vm "$machines" >&2; then
    cat >&2 <<EOF

FAIL: the $machines runtime holds none of the images this suite boots.

Without them the emulator falls back to an upstream image, which carries no ssh
daemon and cannot install one: since #202 a machine holds exactly the address
its provider publishes, on a routed NIC with no NAT, so cloud-init has no route
to a package repository. The machine will run, the API will publish its address,
and nothing will ever answer on port 22.

Run:  $binary images --vm $machines

It builds them once, takes minutes, and leaves them on the host.
EOF
    exit 1
  fi
  echo "  images: the $machines runtime holds every image this suite boots" >&2
}

# guard_leftovers refuses a run whose host already holds a DHCP service an
# earlier run left behind and this user cannot end (#375).
#
# The measurement. The runtime leg of `mise run evidence:update` failed three
# times in a row, in the same place each time: `network.sh` swept the runtime,
# the sweep found a dnsmasq whose interface had outlived its network, named it
# exactly — and exited 1, because the process belongs to the `incus` user and
# the operator running the suite may not signal it. The printed remedy was
# right. Nothing ran it. Every run died there until a human read the log and
# retyped a pid, and a gate whose only remedy is a manual step somebody has to
# notice is a gate that gets worked around, which is how `--no-verify` is
# learned.
#
# What it must never become is the obvious shortcut. **Nothing here escalates.**
# A conformance suite that acquired the right to end a daemon it did not start
# would be a worse defect than the one it works around: it is the question
# `mustOwn` asks of the driver, one layer up, and a process nobody here created
# is not ours to end. So this refuses, names the pid, and names one command the
# operator runs with their own hands.
#
# The refusal is a doorstep and not a verdict thirty lines in, for the reason
# #335 gave for the image guard. The sweep that meets this state is the twelfth
# step of `mise run conformance`, so the leg burned every client suite before
# saying anything, and the answer had not changed since second zero.
#
# What it does not do is prevent one appearing. Measured on 2026-08-21: the
# machines-on leg produced a leftover of its own, mid-run, in bridge mode, from
# a network it had created minutes earlier — so this refusal caught it at the
# next suite's doorstep rather than at the start of the leg, and the leg still
# failed. That is the honest outcome and it is a different defect from this
# one: what the doorstep makes cheap is the diagnosis and the remedy, not the
# birth of the leftover.
#
# It refuses on any such leftover rather than only on one holding a block this
# run happens to need, and that is deliberate three times over. A leftover that
# gets this far is this emulator's own debris by construction — an `fnt-` name
# it derives, a network the runtime no longer knows, a service running off the
# runtime's own state directory — so it is never somebody's working service
# caught in the way. The blocks a run needs are not knowable here: they come
# from the clients, from three FEINT_TEST_* variables and from fixtures, and a
# guard that guessed that set would pass on the one block it forgot. And a
# doorstep more permissive than the sweep behind it changes nothing: `feint
# clean` fails on any leftover it cannot end, so the run would simply die where
# it used to.
#
# TestTheLeftoverGuardRefusesAHostItCannotClean fails without it, and
# tools/falsify/specs/unkillable-dhcp-orphan.json replays that.
guard_leftovers() {
  local endpoint="${1:-}" health machines

  health="$(curl -sf "$endpoint/_feint/health" || true)"
  if [ -z "$health" ]; then
    echo "FAIL: $endpoint answered no health payload, so nothing here knows what its machines are" >&2
    exit 1
  fi
  machines="$(printf '%s' "$health" | jq -r '.machines // "none"')"
  guard_leftovers_for "$machines"
}

# guard_leftovers_for is the same refusal for a caller that has no emulator to
# ask yet: `mise run conformance` runs it before it starts anything, from the
# mode it is about to pass to `feint start`.
guard_leftovers_for() {
  local machines="${1:-off}" binary

  # With no machine runtime nothing will take an address block, so the question
  # does not apply. Said out loud rather than returned in silence, per the rule
  # the image guard above states: a precondition that passes quietly on the very
  # case it exists for reads as green when it never ran. This is also the leg
  # that runs most often — `FEINT_VM=off` is the default and the whole of CI's
  # conformance matrix — so a guard that fired here would be a brand new failure
  # on the path nothing was wrong with.
  if [ "$machines" = "none" ] || [ "$machines" = "off" ] || [ "$machines" = "null" ] || [ -z "$machines" ]; then
    echo "  leftovers: not asked, this run starts no machine" >&2
    return 0
  fi

  binary="$(feint_binary)"
  if [ ! -x "$binary" ]; then
    cat >&2 <<EOF
FAIL: no feint binary at $binary.

This suite needs it to ask what a run left holding an address block on this
host. Build it (mise run build, or go build -o feint ./cmd/feint), or point
FEINT_BIN at one. Skipping the question instead would let the suite start on a
host whose blocks are already taken, which is the failure this check names.
EOF
    exit 1
  fi

  if ! "$binary" clean --check --vm "$machines" >&2; then
    cat >&2 <<EOF

FAIL: a DHCP service left behind by a run holds an address block on this host,
and this user cannot end it. Its pid, its block and the reason are named above.

Usually it belongs to the runtime's own user rather than to you: Incus starts
the DHCP service of every managed bridge under the incus account. Nothing in
this suite may signal it either way: a conformance run that escalated to end a
daemon it did not start would be a worse defect than the one it works around.

Run:  sudo $binary clean --vm $machines

That is this same sweep, elevated by you on purpose. It re-asks every ownership
question at the moment of the signal, so it ends only what this emulator can
prove is its own.
EOF
    exit 1
  fi
}

# Sourced by the suites above, and runnable for the one caller that has no suite
# to source it into: `mise run conformance` asks this before `feint start`, so a
# leg refuses at second zero instead of after every client suite has run.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  case "${1:-}" in
    leftovers) guard_leftovers_for "${2:-off}" ;;
    *)
      echo "usage: tools/conformance/guard.sh leftovers <machine runtime>" >&2
      echo "  (the other guards take an endpoint and are sourced, not run)" >&2
      exit 2 ;;
  esac
fi
