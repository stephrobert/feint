#!/usr/bin/env bash
# Conformance check: register an SSH key through the API, boot a server, log into it.
#
# This is the end of the chain the emulator exists for. Every step uses the official CLI, and the
# last one uses plain ssh(1): if it opens a shell, the emulated control plane really did provision
# a machine with the project's key on the provider's default account.
#
# Requires a machine runtime that boots a real system (`feint serve --vm incus` or --vm incus-vm).
# With --vm off nothing is started and the login step is skipped.
#
# Usage: tools/conformance/scaleway/ssh.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
ZONE="${ZONE:-fr-par-1}"
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

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
set +a
export SCW_API_URL="$ENDPOINT"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

echo "conformance: ssh round-trip against $ENDPOINT"

# Asked up front, because it decides the verdict at the end. With no runtime the
# control plane still publishes the flexible address — the documented degraded
# mode — so the login step can only be skipped. With a runtime, a login that
# fails is a failure: this suite used to skip there too, which made it unable to
# say the one thing it exists to say (#116 shipped under a green run).
MACHINES="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.machines')"

echo "- generate a throwaway key pair"
ssh-keygen -q -t ed25519 -N '' -C feint-conformance -f "$WORK/id" </dev/null
ok "$(cut -d' ' -f1,3 <"$WORK/id.pub")"

echo "- register it through the IAM API"
key_id="$(scw iam ssh-key create name=feint-conformance public-key="$(cat "$WORK/id.pub")" -o json | jq -r '.id')"
[ -n "$key_id" ] && [ "$key_id" != "null" ] || fail "the CLI did not get an id back"
scw iam ssh-key list -o json | jq -e --arg id "$key_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the key is missing from the list"
ok "key $key_id"

echo "- boot a server"
# A flexible IP, then the server that carries it. This is the path a real user
# takes, and the only one that yields an address a real client can read: the
# SDK says private_ip is "deprecated and always null when routed_ip_enabled is
# True", which is this emulator's default. Reading it here would have been
# testing the emulator against a field the API does not populate.
ip_id="$(scw instance ip create zone="$ZONE" -o json | jq -r '(.ip // .).id')"
[ -n "$ip_id" ] && [ "$ip_id" != null ] || fail "ip creation failed"

server_id="$(scw instance server create name=ssh-conformance type=DEV1-S zone="$ZONE" \
               ip="$ip_id" -o json | jq -r '.id')"
[ -n "$server_id" ] || fail "server creation failed"
scw instance server start "$server_id" zone="$ZONE" >/dev/null
ok "server $server_id"

echo "- wait for the address the API publishes"
ip=""
for _ in $(seq 1 60); do
  ip="$(scw instance server get "$server_id" zone="$ZONE" -o json \
        | jq -r '.public_ips[0].address // empty')"
  [ -n "$ip" ] && break
  sleep 2
done
[ -n "$ip" ] || fail "the server never published an address (is a machine runtime enabled?)"
ok "address $ip"

echo "- log in as the provider's default user (root on Scaleway)"
# -F /dev/null ignores the operator's ~/.ssh/config, and that is not tidiness.
# A ProxyJump matching a broad Host pattern also matches the machine runtime's
# private range: ssh then dials the bastion, which cannot route to 10.x, and the
# connection hangs at "timed out during banner exchange". The symptom reads as a
# missing ssh daemon while sshd is up, listening and holding the right key.
# Measured on an Incus VM with a global ProxyJump configured.
logged_in=false
for _ in $(seq 1 45); do
  if ssh -F /dev/null -i "$WORK/id" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
       -o ConnectTimeout=3 -o BatchMode=yes "root@$ip" 'echo alive' 2>/dev/null | grep -q alive; then
    logged_in=true
    break
  fi
  sleep 2
done

if [ "$logged_in" = true ]; then
  # The assertions live in sshlogin.sh, shared by the three ssh suites, and
  # every one of them names what it did not get. This block used to be a
  # single unguarded ssh whose last sub-command was `grep -c`, which exits 1
  # when the count is zero: under `set -euo pipefail` the script died right
  # here without a word, five scheduled nights in a row (#501).
  assert_login "root@$ip" "$WORK/id" root "$WORK/id.pub"
elif [ "$MACHINES" = "none" ]; then
  echo "  SKIP: no ssh daemon answered on $ip (expected with --vm off; use --vm incus)" >&2
else
  fail "no ssh daemon answered on $ip although the $MACHINES runtime is on: the published address is a promise nobody keeps"
fi

echo "- clean up"
scw instance server stop "$server_id" zone="$ZONE" >/dev/null
sleep 3
scw instance server delete "$server_id" zone="$ZONE" >/dev/null
scw iam ssh-key delete "$key_id" >/dev/null
ok "server and key removed"

echo "conformance: ssh round-trip passed"
