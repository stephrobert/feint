#!/usr/bin/env bash
# Conformance check: register a keypair through the Outscale API, boot a Vm, log into it.
#
# The Scaleway sibling proved the chain for one provider and left the other two asserted rather
# than driven: the login is a per-pack fact (root there, `outscale` here, Outscale's own
# documentation says so on their OMIs), and a good image with the wrong login is a machine nobody
# enters — nothing but a real ssh(1) can catch it.
#
# The address read is PublicIp — the address a real Outscale user logs into. LinkPublicIp routes
# the address to the machine here (it moved only the record for a long time), and it is the one
# plane that answers the host in both runtime modes: an OVN router SNATs the host away from
# subnet-internal addresses, so a suite reading PrivateIp would prove the login in bridge mode
# and read a closed port over a live sshd in OVN — measured.
#
# Requires a machine runtime (`feint serve --vm incus`); with --vm off a Vm has no address at all
# here, so the suite skips itself entirely rather than asserting on nothing.
#
# Usage: tools/conformance/outscale/ssh.sh [endpoint]
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

command -v octl >/dev/null 2>&1 || { echo "FAIL: octl is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

echo "conformance: outscale ssh round-trip against $ENDPOINT"

MACHINES="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.machines')"
if [ "$MACHINES" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); a Vm publishes no address without one"
  exit 0
fi

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# The endpoint comes from the argument, not from the credentials file: octl
# lets the environment override --config, so a pinned value there silently wins
# over the port this run was asked to measure.
# shellcheck disable=SC2034 # read by octl from the environment, not here
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a
guard_no_real_profile OSC_ENDPOINT_API octl

WORK="$(mktemp -d)"
KEY_NAME="feint-sshconf-osc"
vm_id=""
ip_id=""

# The trap is set before anything is created, not at the end of the file: an
# interrupted run never reaches a cleanup written as a final step, and the
# machines it leaves carry no label a sweep could recognise as somebody's probe.
cleanup() {
  # Every line tolerates absence: after a clean run everything is already
  # gone, and under `set -e` a failing delete inside this trap turns a passed
  # suite into exit 1 — measured, the task runner then never ran the next
  # suite while this one printed "passed".
  [ -n "$vm_id" ] && osc DeleteVms --VmIds "$vm_id" >/dev/null 2>&1 || true
  [ -n "$ip_id" ] && osc DeletePublicIp --PublicIpId "$ip_id" >/dev/null 2>&1 || true
  osc DeleteKeypair --KeypairName "$KEY_NAME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

cat > "$WORK/config.json" <<EOF
{
  "default": {
    "access_key": "$OSC_ACCESS_KEY",
    "secret_key": "$OSC_SECRET_KEY",
    "region": "$OSC_REGION",
    "protocol": "http",
    "endpoints": { "api": "$ENDPOINT/api/v1" }
  }
}
EOF
# See tools/conformance/outscale/octl.sh: the API rather than an alias, the
# API's own body rather than the CLI's rearrangement, and a request body that
# can only come from flags.
osc() { octl --config "$WORK/config.json" --no-upgrade -o raw iaas api "$@" </dev/null; }

echo "- generate a throwaway key pair"
ssh-keygen -q -t ed25519 -N '' -C feint-sshconf -f "$WORK/id" </dev/null
ok "$(cut -d' ' -f1,3 <"$WORK/id.pub")"

echo "- register it through CreateKeypair"
created="$(osc CreateKeypair --KeypairName "$KEY_NAME" --PublicKey "$(cat "$WORK/id.pub")")" \
  || fail "CreateKeypair rejected: $created"
fingerprint="$(printf '%s' "$created" | jq -r '.Keypair.KeypairFingerprint // empty')"
[ -n "$fingerprint" ] || fail "the keypair came back without a fingerprint: $created"
osc ReadKeypairs | jq -e --arg n "$KEY_NAME" 'any(.Keypairs[]; .KeypairName == $n)' >/dev/null \
  || fail "the keypair is missing from ReadKeypairs"
ok "keypair $KEY_NAME ($fingerprint)"

echo "- allocate a public IP"
created_ip="$(osc CreatePublicIp)" || fail "CreatePublicIp rejected: $created_ip"
ip_id="$(printf '%s' "$created_ip" | jq -r '.PublicIp.PublicIpId // empty')"
public="$(printf '%s' "$created_ip" | jq -r '.PublicIp.PublicIp // empty')"
[ -n "$ip_id" ] && [ -n "$public" ] || fail "no address in the create response: $created_ip"
ok "$public ($ip_id)"

echo "- boot a Vm carrying the keypair"
vm="$(osc CreateVms --ImageId ami-00000001 --VmType tinav6.c1r1p2 --KeypairName "$KEY_NAME")" \
  || fail "CreateVms rejected: $vm"
vm_id="$(printf '%s' "$vm" | jq -r '.Vms[0].VmId // empty')"
[ -n "$vm_id" ] || fail "no VmId in the create response: $vm"
ok "vm $vm_id"

echo "- link the public IP, and read it back where a client reads it"
osc LinkPublicIp --PublicIpId "$ip_id" --VmId "$vm_id" >/dev/null || fail "LinkPublicIp rejected"
published="$(osc ReadVms | jq -r --arg id "$vm_id" \
             '.Vms[] | select(.VmId == $id) | .PublicIp // empty')"
[ "$published" = "$public" ] \
  || fail "the Vm publishes '$published', the linked address is $public"
ok "the Vm publishes $public"

echo "- log in on the public address, as the provider's default user (outscale)"
# -F /dev/null ignores the operator's ~/.ssh/config: a ProxyJump matching a
# broad Host pattern also matches the machine runtime's private range, ssh then
# dials a bastion that cannot route there, and the connection dies on "timed
# out during banner exchange" while sshd is up and holding the right key.
logged_in=false
for _ in $(seq 1 45); do
  if ssh -F /dev/null -i "$WORK/id" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
       -o ConnectTimeout=3 -o BatchMode=yes "outscale@$public" 'echo alive' 2>/dev/null | grep -q alive; then
    logged_in=true
    break
  fi
  sleep 2
done
[ "$logged_in" = true ] \
  || fail "no ssh daemon answered for outscale@$public although the $MACHINES runtime is on: the published address is a promise nobody keeps"
# The assertions live in sshlogin.sh, shared by the three ssh suites: every
# one carries its message, and no grep ever decides an exit status (#501).
assert_login "outscale@$public" "$WORK/id" outscale "$WORK/id.pub"

echo "- clean up"
osc DeleteVms --VmIds "$vm_id" >/dev/null || fail "DeleteVms rejected"
vm_id=""
osc DeletePublicIp --PublicIpId "$ip_id" >/dev/null || fail "DeletePublicIp rejected"
ip_id=""
osc DeleteKeypair --KeypairName "$KEY_NAME" >/dev/null || fail "DeleteKeypair rejected"
ok "vm, public IP and keypair removed"

echo "conformance: outscale ssh round-trip passed"
