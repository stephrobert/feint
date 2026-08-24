#!/usr/bin/env bash
# Conformance check: the same request produces the same machine on every
# provider.
#
# The rule is the author's, stated during #201: "I must see the same number of
# IPs whatever the cloud provider." What triggered it was measured on one
# station: a Scaleway server on one private network carried three addresses on
# two interfaces where an Outscale Vm carried one on one — and the difference
# was the runtime's fallback interface surviving the attachment, an address no
# provider API publishes.
#
# So this suite compares, it does not recite: it drives an equivalent request
# through each provider's own surface, counts what the HOST carries (read from
# the runtime, never from the API that is under test), and requires the counts
# to agree line by line. No expected number is written anywhere: a hard-coded
# count gets updated without anyone noticing the providers diverged.
#
# Two configurations, made equivalent explicitly:
#   1. a machine on a private network, no public address asked for — the
#      baseline, because in a real cloud a public address costs money and
#      nothing must allocate one the client did not request;
#   2. the same machine with one public address, explicitly requested.
#
# And one property per row on top of the counts: every address the machine
# carries is published by its provider's API. Zero orphan addresses, on the
# three — this is what replaced the long-standing "carried but not published"
# skip of the Scaleway network suite.
#
# A verdict about "this run" must hold on the poorest run that triggers it: a
# provider whose client is not installed is skipped BY NAME, and with fewer
# than two participants the equality is skipped explicitly rather than
# affirmed on a population of one.
#
# Usage: tools/conformance/parity.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
ZONE="${ZONE:-fr-par-1}"
REGION="${REGION:-fr-par}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "$ENDPOINT"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# Two kinds of bad news, and they must not share an exit.
#
# `fail` is the harness giving up: a client refused a create, an address never
# appeared. Nothing has been compared, and stopping is right.
#
# `note` is a finding: the comparison ran and the providers disagreed. Findings
# accumulate and are printed together at the end, because the first version of
# this script exited on the first one — and the check that exited was the count
# equality, which is the weaker of the two. It aborted before the orphan check,
# which is the universal one, so the run that found a divergence reported the
# debatable half and never measured the half nobody disputes. A control that
# stops the control behind it is a control that hides it.
FINDINGS=0
note() { echo "  FINDING: $*" >&2; FINDINGS=$((FINDINGS + 1)); }
api()  { curl -sf -H 'Content-Type: application/json' "$@"; }

command -v jq >/dev/null 2>&1 || fail "jq is not installed"
command -v incus >/dev/null 2>&1 || fail "the incus client is not on PATH, so nothing can be counted"

echo "conformance: address parity against $ENDPOINT"

MACHINES="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.machines')"
if [ "$MACHINES" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); nothing to count"
  exit 0
fi

# Which providers can be driven from this station. Each absence is named: a
# comparison that silently shrank its population is a comparison that lies.
HAVE_OSC=""; HAVE_EXO=""
# if/else rather than `A && B || C`: with the short form C also runs when A
# succeeded and B returned non-zero, so a station that HAS the client could be
# told it does not — and this suite's whole verdict is a population count.
if command -v oapi-cli >/dev/null 2>&1; then HAVE_OSC=1; else skip "oapi-cli is not installed; Outscale sits out this comparison"; fi
if command -v exo      >/dev/null 2>&1; then HAVE_EXO=1; else skip "exo is not installed; Exoscale sits out this comparison"; fi

# Blocks, distinct from every other suite on this host.
SCW_BLOCK="10.171.0.0/24"
OSC_BLOCK="10.172.0.0/16"; OSC_SUBBLOCK="10.172.1.0/24"
EXO_START="10.173.0.20"; EXO_END="10.173.0.200"; EXO_MASK="255.255.255.0"

# ---- What the host carries, read from the runtime ----------------------------

iface_count() { # machine-name
  incus query "/1.0/instances/$1" 2>/dev/null \
    | jq '[.expanded_devices | to_entries[] | select(.value.type == "nic")] | length'
}
addr_list() { # machine-name -> one address per line, no mask
  incus exec "$1" -- ip -4 -o addr show scope global 2>/dev/null \
    | awk '{print $4}' | cut -d/ -f1
}
counts_of() { # machine-name -> "ifaces/addrs"
  echo "$(iface_count "$1")/$(addr_list "$1" | grep -c . || true)"
}
orphans_of() { # machine-name published... -> addresses carried but not published
  local m="$1"; shift
  local out=""
  for addr in $(addr_list "$m"); do
    case " $* " in
      *" $addr "*) ;;
      *) out="$out $addr" ;;
    esac
  done
  echo "$out"
}
wait_carried() { # address
  for _ in $(seq 1 15); do
    incus list -f csv -c n4 2>/dev/null | grep -q "$1" && return 0
    sleep 2
  done
  return 1
}

# ---- Cleanup ----------------------------------------------------------------

scw_srv=""; scw_pn=""; scw_ip_id=""
osc_vm=""; osc_sub=""; osc_net=""; osc_ip_id=""
exo_id=""; exo_eip=""
WORK="$(mktemp -d)"
cleanup() {
  [ -n "$scw_srv" ] && api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$scw_srv/action" \
    -d '{"action":"terminate"}' >/dev/null 2>&1
  [ -n "$osc_vm" ] && osc DeleteVms '--VmIds[]' "$osc_vm" >/dev/null 2>&1
  [ -n "$exo_id" ] && curl -sf -X DELETE "$ENDPOINT/v2/instance/$exo_id" >/dev/null 2>&1
  sleep 8
  [ -n "$scw_ip_id" ] && api -X DELETE "$ENDPOINT/instance/v1/zones/$ZONE/ips/$scw_ip_id" >/dev/null 2>&1
  [ -n "$osc_ip_id" ] && osc DeletePublicIp --PublicIpId "$osc_ip_id" >/dev/null 2>&1
  [ -n "$exo_eip" ] && exo -Q compute elastic-ip delete "$exo_eip" --force >/dev/null 2>&1
  [ -n "$scw_pn" ] && api -X DELETE "$ENDPOINT/vpc/v2/regions/$REGION/private-networks/$scw_pn" >/dev/null 2>&1
  [ -n "$osc_sub" ] && osc DeleteSubnet --SubnetId "$osc_sub" >/dev/null 2>&1
  [ -n "$osc_net" ] && osc DeleteNet --NetId "$osc_net" >/dev/null 2>&1
  [ -n "$HAVE_EXO" ] && exo -Q compute private-network delete parity-exo-net --force >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

# ---- Scaleway: server on a private network, nothing public asked for --------
# The raw API is the minimal request: dynamic_ip_required defaults to false and
# no flexible IP is attached, which is what "no public address" means here. The
# scw CLI's own default is ip=new — a real client asking for an address — and
# that path stays covered by scw-cli.sh; this suite is about what the emulator
# does when nobody asks.

echo "- scaleway: a server on a private network"
scw_pn="$(api -X POST "$ENDPOINT/vpc/v2/regions/$REGION/private-networks" \
          -d "{\"name\":\"parity\",\"subnets\":[\"$SCW_BLOCK\"]}" | jq -r '.id')"
[ -n "$scw_pn" ] && [ "$scw_pn" != null ] || fail "scaleway refused the block $SCW_BLOCK"
scw_srv="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers" \
           -d '{"name":"parity-scw","commercial_type":"DEV1-S","image":"alpine"}' | jq -r '.server.id')"
[ -n "$scw_srv" ] && [ "$scw_srv" != null ] || fail "the scaleway server was not created"
api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$scw_srv/action" -d '{"action":"poweron"}' >/dev/null
scw_nic="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$scw_srv/private_nics" \
           -d "{\"private_network_id\":\"$scw_pn\"}")"
scw_ipam="$(printf '%s' "$scw_nic" | jq -r '.private_nic.ipam_ip_ids[0] // empty')"
[ -n "$scw_ipam" ] || fail "the scaleway NIC names no IPAM address"
scw_priv="$(api "$ENDPOINT/ipam/v1/regions/$REGION/ips/$scw_ipam" | jq -r '.address' | cut -d/ -f1)"
wait_carried "$scw_priv" || fail "no machine carries $scw_priv"
scw_public_baseline="$(api "$ENDPOINT/instance/v1/zones/$ZONE/servers/$scw_srv" \
                       | jq -r '[.server.public_ips[]?.address] | length')"
[ "$scw_public_baseline" = "0" ] \
  || fail "scaleway allocated a public address nobody asked for"
scw_m="feint-scw-$scw_srv"

# ---- Outscale: a Vm born in a Subnet ----------------------------------------

osc() { oapi-cli --config "$WORK/osc-config.json" "$@"; }
if [ -n "$HAVE_OSC" ]; then
  echo "- outscale: a Vm in a Subnet"
  set -a
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/outscale/fake-credentials.env"
  # shellcheck disable=SC2034 # read by oapi-cli from the environment, not here
  OSC_ENDPOINT_API="$ENDPOINT"
  set +a
  # oapi-cli falls back to its stored credentials when the environment says
  # nothing, and this station carries real Outscale profiles. Every other
  # Outscale suite asks this question before its first call; this one drives
  # CreateNet and CreateVms, so it asks it too.
  guard_no_real_profile OSC_ENDPOINT_API oapi-cli
  cat > "$WORK/osc-config.json" <<EOF
{
  "default": {
    "access_key": "$OSC_ACCESS_KEY",
    "secret_key": "$OSC_SECRET_KEY",
    "region": "$OSC_REGION",
    "protocol": "http",
    "endpoints": { "api": "$ENDPOINT" }
  }
}
EOF
  osc_net="$(osc CreateNet --IpRange "$OSC_BLOCK" | jq -r '.Net.NetId')"
  osc_sub="$(osc CreateSubnet --NetId "$osc_net" --IpRange "$OSC_SUBBLOCK" | jq -r '.Subnet.SubnetId')"
  osc_doc="$(osc CreateVms --ImageId ami-00000003 --VmType tinav6.c1r1p2 --SubnetId "$osc_sub")"
  osc_vm="$(printf '%s' "$osc_doc" | jq -r '.Vms[0].VmId')"
  osc_priv="$(printf '%s' "$osc_doc" | jq -r '.Vms[0].PrivateIp // empty')"
  [ -n "$osc_vm" ] && [ -n "$osc_priv" ] || fail "the outscale Vm was not created with a PrivateIp"
  wait_carried "$osc_priv" || fail "no machine carries $osc_priv"
  osc_public_baseline="$(osc ReadVms '--Filters.VmIds[]' "$osc_vm" | jq -r '.Vms[0].PublicIp // empty')"
  [ -z "$osc_public_baseline" ] || fail "outscale allocated a public address nobody asked for"
  osc_m="feint-osc-$osc_vm"
fi

# ---- Exoscale: an instance attached to a private network --------------------

if [ -n "$HAVE_EXO" ]; then
  echo "- exoscale: an instance on a private network"
  set -a
  # shellcheck source=/dev/null
  . "$SCRIPT_DIR/exoscale/fake-credentials.env"
  set +a
  export EXOSCALE_API_ENDPOINT=${ENDPOINT}/v2
  guard_no_real_profile EXOSCALE_API_ENDPOINT exo
  exo compute private-network create parity-exo-net \
    --start-ip "$EXO_START" --end-ip "$EXO_END" --netmask "$EXO_MASK" >/dev/null \
    || fail "exoscale refused the range $EXO_START-$EXO_END"
  exo compute instance create parity-exo \
    --template "Linux Ubuntu 24.04 LTS 64-bit" --instance-type standard.tiny >/dev/null \
    || fail "the exoscale instance was not created"
  exo_id="$(exo -O json compute instance list | jq -r '.[] | select(.name == "parity-exo") | .id')"
  [ -n "$exo_id" ] || fail "the exoscale instance is not in the list"
  exo compute instance private-network attach parity-exo parity-exo-net >/dev/null \
    || fail "the exoscale attach was refused"
  exo_priv="$(exo -O json compute private-network show parity-exo-net \
              | jq -r '.leases[] | select(.instance == "parity-exo") | .ip_address')"
  [ -n "$exo_priv" ] || fail "the exoscale lease publishes no address"
  wait_carried "$exo_priv" || fail "no machine carries $exo_priv"
  exo_m="feint-exo-$exo_id"
fi
sleep 3

# ---- Row 1: private network, no public address ------------------------------

echo "- row 1: same request, no public address: the counts must agree"
rows=""
verdict=""
scw_c="$(counts_of "$scw_m")"; rows="scaleway=$scw_c"
[ -n "$HAVE_OSC" ] && { osc_c="$(counts_of "$osc_m")"; rows="$rows outscale=$osc_c"; }
[ -n "$HAVE_EXO" ] && { exo_c="$(counts_of "$exo_m")"; rows="$rows exoscale=$exo_c"; }
participants=$(echo "$rows" | wc -w)
if [ "$participants" -lt 2 ]; then
  skip "only $participants provider(s) drivable; equality not measurable on a population of one"
else
  first="${rows%% *}"; first="${first#*=}"
  for entry in $rows; do
    [ "${entry#*=}" = "$first" ] || verdict="diverged"
  done
  if [ -n "$verdict" ]; then
    note "the same request produced different machines: $rows (interfaces/addresses)"
  else
    ok "one request, one shape: $rows (interfaces/addresses)"
  fi
fi

echo "- row 1: every carried address is published"
orphans=0
o="$(orphans_of "$scw_m" "$scw_priv")"
[ -z "${o// /}" ] || { note "scaleway carries unpublished address(es):$o"; orphans=1; }
if [ -n "$HAVE_OSC" ]; then
  o="$(orphans_of "$osc_m" "$osc_priv")"
  [ -z "${o// /}" ] || { note "outscale carries unpublished address(es):$o"; orphans=1; }
fi
if [ -n "$HAVE_EXO" ]; then
  o="$(orphans_of "$exo_m" "$exo_priv")"
  [ -z "${o// /}" ] || { note "exoscale carries unpublished address(es):$o"; orphans=1; }
fi
[ "$orphans" = "1" ] || ok "zero orphan addresses"

# ---- Row 2: one public address, explicitly requested ------------------------

echo "- row 2: one public address each, explicitly requested"
scw_ip_doc="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/ips" -d '{}')"
scw_ip_id="$(printf '%s' "$scw_ip_doc" | jq -r '.ip.id')"
scw_pub="$(printf '%s' "$scw_ip_doc" | jq -r '.ip.address')"
api -X PATCH "$ENDPOINT/instance/v1/zones/$ZONE/ips/$scw_ip_id" -d "{\"server\":\"$scw_srv\"}" >/dev/null \
  || fail "attaching the scaleway flexible IP was refused"
if [ -n "$HAVE_OSC" ]; then
  osc_ip_doc="$(osc CreatePublicIp)"
  osc_ip_id="$(printf '%s' "$osc_ip_doc" | jq -r '.PublicIp.PublicIpId')"
  osc_pub="$(printf '%s' "$osc_ip_doc" | jq -r '.PublicIp.PublicIp')"
  osc LinkPublicIp --PublicIpId "$osc_ip_id" --VmId "$osc_vm" >/dev/null \
    || fail "linking the outscale public IP was refused"
fi
if [ -n "$HAVE_EXO" ]; then
  exo -O json compute elastic-ip create --description parity >/dev/null \
    || fail "the exoscale elastic IP was not created"
  exo_eip="$(exo -O json compute elastic-ip list | jq -r '.[0].ip_address // .[0].ip // empty')"
  [ -n "$exo_eip" ] || fail "no address on the exoscale elastic IP"
  exo compute instance elastic-ip attach parity-exo "$exo_eip" >/dev/null \
    || fail "attaching the exoscale elastic IP was refused"
fi
sleep 5

rows=""
verdict=""
scw_c="$(counts_of "$scw_m")"; rows="scaleway=$scw_c"
[ -n "$HAVE_OSC" ] && { osc_c="$(counts_of "$osc_m")"; rows="$rows outscale=$osc_c"; }
[ -n "$HAVE_EXO" ] && { exo_c="$(counts_of "$exo_m")"; rows="$rows exoscale=$exo_c"; }
if [ "$participants" -lt 2 ]; then
  skip "only $participants provider(s) drivable; equality not measurable on a population of one"
else
  first="${rows%% *}"; first="${first#*=}"
  for entry in $rows; do
    [ "${entry#*=}" = "$first" ] || verdict="diverged"
  done
  if [ -n "$verdict" ]; then
    note "with one public address each, the machines differ: $rows (interfaces/addresses)"
  else
    ok "one request, one shape: $rows (interfaces/addresses)"
  fi
fi

echo "- row 2: every carried address is published"
orphans=0
o="$(orphans_of "$scw_m" "$scw_priv" "$scw_pub")"
[ -z "${o// /}" ] || { note "scaleway carries unpublished address(es):$o"; orphans=1; }
if [ -n "$HAVE_OSC" ]; then
  o="$(orphans_of "$osc_m" "$osc_priv" "$osc_pub")"
  [ -z "${o// /}" ] || { note "outscale carries unpublished address(es):$o"; orphans=1; }
fi
if [ -n "$HAVE_EXO" ]; then
  o="$(orphans_of "$exo_m" "$exo_priv" "$exo_eip")"
  [ -z "${o// /}" ] || { note "exoscale carries unpublished address(es):$o"; orphans=1; }
fi
[ "$orphans" = "1" ] || ok "zero orphan addresses"

if [ "$FINDINGS" -gt 0 ]; then
  echo "conformance: address parity found $FINDINGS divergence(s)" >&2
  exit 1
fi
echo "conformance: address parity passed"
