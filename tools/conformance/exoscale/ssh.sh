#!/usr/bin/env bash
# Conformance check: register an SSH key through the Exoscale API, boot an instance, log into it.
#
# Exoscale is the pack that motivated Boot.User existing at all: the login is not a property of
# the cloud but of the template, declared in its own default-user field. So this suite does not
# hardcode a login — it reads the field where the provider publishes it, and proves that the
# machine really provisions that account. A good image with the wrong login is a machine nobody
# enters, and only a real ssh(1) can catch it.
#
# The address logged into is an elastic IP, attached through the API: attaching routes it to the
# machine, and it is the one plane that answers the host in both runtime modes — an OVN router
# SNATs the host away from subnet-internal addresses, so a suite reading the instance's own
# public-ip would prove the login in bridge mode and read a closed port over a live sshd in OVN,
# measured. The instance's public-ip field is still asserted as published.
#
# Requires a machine runtime (`feint serve --vm incus`); with --vm off an instance has no address
# at all here, so the suite skips itself entirely rather than asserting on nothing.
#
# Usage: tools/conformance/exoscale/ssh.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../guard.sh"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../sshlogin.sh"
guard_local "$ENDPOINT"
# The images this suite boots have to exist before it registers a key and
# promises an address; without them nothing answers on port 22 (#335).
guard_images "$ENDPOINT"

command -v exo >/dev/null 2>&1 || { echo "FAIL: exo is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

echo "conformance: exoscale ssh round-trip against $ENDPOINT"

MACHINES="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.machines')"
if [ "$MACHINES" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); an instance publishes no address without one"
  exit 0
fi

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
set +a
export EXOSCALE_API_ENDPOINT=${ENDPOINT}/v2

WORK="$(mktemp -d)"
KEY_NAME="feint-sshconf-exo"
INSTANCE_NAME="feint-sshconf-exo"
instance_id=""
eip=""

# The trap is set before anything is created, not at the end of the file: an
# interrupted run never reaches a cleanup written as a final step, and the
# machines it leaves carry no label a sweep could recognise as somebody's probe.
cleanup() {
  # Every line tolerates absence: after a clean run everything is already
  # gone, and under `set -e` a failing delete inside this trap turns a passed
  # suite into exit 1 — measured, the task runner then never ran the next
  # suite while this one printed "passed".
  [ -n "$instance_id" ] && exo -Q compute instance delete "$instance_id" --force >/dev/null 2>&1 || true
  [ -n "$eip" ] && exo -Q compute elastic-ip delete "$eip" --force >/dev/null 2>&1 || true
  exo -Q compute ssh-key delete "$KEY_NAME" --force >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "- generate a throwaway key pair"
ssh-keygen -q -t ed25519 -N '' -C feint-sshconf -f "$WORK/id" </dev/null
ok "$(cut -d' ' -f1,3 <"$WORK/id.pub")"

echo "- register it through the SSH key API"
exo compute ssh-key register "$KEY_NAME" "$WORK/id.pub" >/dev/null || fail "ssh-key register rejected"
exo -O json compute ssh-key list | jq -e --arg n "$KEY_NAME" 'any(.[]; .name == $n)' >/dev/null \
  || fail "the key is missing from the list"
ok "key $KEY_NAME"

echo "- read the template, and the login it declares"
templates="$(exo -O json compute instance-template list)" || fail "template list rejected"
template_name="$(printf '%s' "$templates" | jq -r '.[0].name // empty')"
template_id="$(printf '%s' "$templates" | jq -r '.[0].id // empty')"
[ -n "$template_id" ] || fail "no template on offer: $templates"
# The login lives in the template's own default-user field — Exoscale declares
# it per template, not per cloud, and hardcoding one here would assert this
# suite's guess instead of the provider's answer.
user="$(curl -sf "$ENDPOINT/v2/template/$template_id" | jq -r '."default-user" // empty')"
[ -n "$user" ] || fail "the template $template_name declares no default-user"
type_name="$(exo -O json compute instance-type list | jq -r '"\(.[0].family).\(.[0].name)"')"
case "$type_name" in *null*|.) fail "no instance type on offer" ;; esac
ok "$template_name, login $user"

echo "- boot an instance carrying the key"
exo compute instance create "$INSTANCE_NAME" \
  --zone "$EXOSCALE_ZONE" --template "$template_name" --instance-type "$type_name" \
  --ssh-key "$KEY_NAME" >/dev/null || fail "instance create rejected"
instance_id="$(exo -O json compute instance list \
               | jq -r --arg n "$INSTANCE_NAME" '.[] | select(.name == $n) | .id')"
[ -n "$instance_id" ] || fail "the instance is not in the list after create"
ok "instance $instance_id"

echo "- wait for the address the API publishes (public-ip)"
ip=""
for _ in $(seq 1 60); do
  ip="$(exo -O json compute instance show "$instance_id" \
        | jq -r '.ip_address // .public_ip // empty')"
  # The CLI renders an absent address as the literal string "<nil>" — measured,
  # and read once by this suite as an address to ssh into. An instrument that
  # accepts its tool's own placeholder measures the placeholder.
  case "$ip" in ""|null|"<nil>") ip="" ;; esac
  [ -n "$ip" ] && break
  sleep 2
done
[ -n "$ip" ] || fail "the instance never published an address although the $MACHINES runtime is on"
ok "address $ip"

echo "- attach an elastic IP, the address a client logs into"
exo -O json compute elastic-ip create --description feint-sshconf >/dev/null \
  || fail "elastic-ip create rejected"
eip="$(exo -O json compute elastic-ip list | jq -r '.[0].ip_address // .[0].ip // empty')"
case "$eip" in ""|null|"<nil>") fail "no address on the created elastic IP" ;; esac
exo compute instance elastic-ip attach "$instance_id" "$eip" >/dev/null \
  || fail "elastic-ip attach rejected"
ok "elastic IP $eip attached"

echo "- log in on the elastic IP, as the template's default user ($user)"
# -F /dev/null ignores the operator's ~/.ssh/config: a ProxyJump matching a
# broad Host pattern also matches the machine runtime's private range, ssh then
# dials a bastion that cannot route there, and the connection dies on "timed
# out during banner exchange" while sshd is up and holding the right key.
logged_in=false
for _ in $(seq 1 45); do
  if ssh -F /dev/null -i "$WORK/id" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
       -o ConnectTimeout=3 -o BatchMode=yes "$user@$eip" 'echo alive' 2>/dev/null | grep -q alive; then
    logged_in=true
    break
  fi
  sleep 2
done
[ "$logged_in" = true ] \
  || fail "no ssh daemon answered for $user@$eip although the $MACHINES runtime is on: the published address is a promise nobody keeps"
# The assertions live in sshlogin.sh, shared by the three ssh suites: every
# one carries its message, and no grep ever decides an exit status (#501).
assert_login "$user@$eip" "$WORK/id" "$user" "$WORK/id.pub"

echo "- clean up"
exo -Q compute instance delete "$instance_id" --force >/dev/null || fail "instance delete rejected"
instance_id=""
exo -Q compute elastic-ip delete "$eip" --force >/dev/null || fail "elastic-ip delete rejected"
eip=""
exo -Q compute ssh-key delete "$KEY_NAME" --force >/dev/null || fail "ssh-key delete rejected"
ok "instance and key removed"

echo "conformance: exoscale ssh round-trip passed"
