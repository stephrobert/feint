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
#
# Usage of the functions is deliberately separate: the first checks where the
# script intends to go, the second checks that the client cannot go anywhere
# else, and the third checks that the emulator can keep the promise the suite is
# about to make. All three live here rather than in each suite for the reason
# CLAUDE.md gives about the shared layer: a control copied into three scripts is
# a control the fourth forgets.

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

  binary="${FEINT_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)/feint}"
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
