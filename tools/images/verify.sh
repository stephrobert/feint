#!/usr/bin/env bash
# Prove the built images answer on port 22 without ever reaching the network.
#
#   tools/images/verify.sh                 # every image built
#   tools/images/verify.sh ubuntu/24.04    # one of them
#
# This is the assertion #203 asks for, and it is deliberately harder than
# "openssh is installed": the machine is given a routed NIC carrying an address
# out of 203.0.113.0/24 (TEST-NET-3, which nothing on earth routes and nothing
# here masquerades), so it has no path to a package repository at all. A daemon
# that answers under those conditions is a daemon that came with the image.
#
# It also holds two properties that a baked-in daemon would quietly break:
#
#   - the host keys differ between two machines from the same image. Baked in,
#     every machine this emulator boots would present one fingerprint and a
#     user's known_hosts would accept any of them for any other.
#   - the machine carries exactly ONE address, which is the shape #202 is for.
set -euo pipefail

ALIAS_PREFIX=feint
BASE=203.0.113
NEXT_HOP=169.254.0.1
failures=0

log()  { printf '   %s\n' "$*"; }
ok()   { printf '   ok   %s\n' "$*"; }
bad()  { printf '   FAIL %s\n' "$*"; failures=$((failures + 1)); }

parent_iface() { ip -o -4 route show default | awk '{print $5; exit}'; }

# One machine from the image, with a single routed NIC and no way out.
boot_isolated() {
  local image=$1 name=$2 address=$3
  incus delete --force "$name" >/dev/null 2>&1 || true
  incus init "$image" "$name" >/dev/null
  incus config device add "$name" eth0 nic \
    nictype=routed parent="$(parent_iface)" \
    ipv4.address="$address" ipv4.host_address="$NEXT_HOP" >/dev/null
  # The netplan Incus documents for a routed NIC: the next hop is link-local and
  # on-link, because a /32 has no subnet to reach it through.
  incus config set "$name" cloud-init.network-config - <<YAML >/dev/null
network:
  version: 2
  ethernets:
    eth0:
      addresses:
        - $address/32
      routes:
        # 0.0.0.0/0 rather than the "default" keyword, and this is measured
        # rather than stylistic: Debian 12's cloud-init rejects "to: default"
        # with "Address default is not a valid ip address", aborts, and never
        # reaches its ssh module — so the host keys this image deliberately does
        # not carry are never regenerated and sshd cannot start. The failure
        # reads as "the image has no ssh daemon", which is the opposite of what
        # happened.
        - to: 0.0.0.0/0
          via: $NEXT_HOP
          on-link: true
YAML
  incus start "$name" >/dev/null
}

# Is anything listening on port 22, read from the kernel rather than from a tool?
#
# The first version ran `ss -ltn`, which busybox does not ship. Alpine had sshd
# running, netstat showed 0.0.0.0:22 LISTEN and rc-status said `started`, and
# this function timed out for 150 seconds anyway — the harness failing before the
# subject, which this repository has on record more than once.
#
# /proc/net/tcp needs no userspace at all: column 2 is HEXIP:HEXPORT and 22 is
# 0x0016, column 4 is the socket state and 0A is LISTEN. Every kernel has it.
listens_on_22() {
  incus exec "$1" -- sh -c \
    'grep -qiE "^ *[0-9]+: [0-9A-F]*:0016 .* 0A " /proc/net/tcp /proc/net/tcp6 2>/dev/null' \
    >/dev/null 2>&1
}

wait_for_ssh() {
  local name=$1 seconds=${2:-120}
  local i=0
  while [ "$i" -lt "$seconds" ]; do
    if listens_on_22 "$name"; then
      return 0
    fi
    sleep 2
    i=$((i + 2))
  done
  return 1
}

host_key() {
  incus exec "$1" -- sh -c \
    'cat /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null || cat /etc/ssh/ssh_host_rsa_key.pub 2>/dev/null' \
    2>/dev/null | awk '{print $2}'
}

verify_one() {
  local name=$1
  local image="$ALIAS_PREFIX/$name"
  # Incus instance names take alphanumerics and hyphens only, so the family and
  # version have to be flattened: ubuntu/24.04 -> ubuntu-24-04.
  local slug="${name//[^A-Za-z0-9]/-}"
  local a="verify-a-$slug"
  local b="verify-b-$slug"

  echo "== $image"
  if ! incus image info "$image" >/dev/null 2>&1; then
    bad "$image is not built; run tools/images/build.sh $name"
    return
  fi

  boot_isolated "$image" "$a" "$BASE.201"
  boot_isolated "$image" "$b" "$BASE.202"

  if wait_for_ssh "$a" 150; then
    ok "answers on port 22 with no route to any package repository"
  else
    bad "nothing listens on port 22 within 150s"
    incus exec "$a" -- sh -c 'cloud-init status --long 2>/dev/null | head -5' || true
  fi

  # It really had no way out: if this reaches, the test proved nothing.
  if incus exec "$a" -- ping -c 1 -W 3 1.1.1.1 >/dev/null 2>&1; then
    bad "the machine reached the internet, so this run does not prove the image carries the daemon"
  else
    ok "no outbound: the daemon cannot have been downloaded"
  fi

  local count
  count=$(incus exec "$a" -- ip -4 -o addr show 2>/dev/null | awk '$2 != "lo"' | wc -l)
  if [ "$count" = "1" ]; then
    ok "carries exactly one address, like a cloud VM"
  else
    bad "carries $count addresses; a cloud VM carries one (#202)"
  fi

  wait_for_ssh "$b" 150 || true
  local ka kb
  ka=$(host_key "$a"); kb=$(host_key "$b")
  if [ -z "$ka" ] || [ -z "$kb" ]; then
    bad "could not read a host key from both machines"
  elif [ "$ka" = "$kb" ]; then
    bad "two machines share one host key: the image baked it in"
  else
    ok "two machines, two host keys"
  fi

  incus delete --force "$a" >/dev/null 2>&1 || true
  incus delete --force "$b" >/dev/null 2>&1 || true
  echo
}

sweep() {
  for leftover in $(incus list -f csv -c n 2>/dev/null | grep '^verify-' || true); do
    incus delete --force "$leftover" >/dev/null 2>&1 || true
  done
}
trap sweep EXIT

if [ $# -gt 0 ]; then
  verify_one "$1"
else
  for image in $(incus image list "$ALIAS_PREFIX/" -f csv -c l 2>/dev/null | cut -d, -f1); do
    verify_one "${image#"$ALIAS_PREFIX"/}"
  done
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "$failures check(s) failed"
  exit 1
fi
echo "every built image answers on port 22 with no network"
