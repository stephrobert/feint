#!/usr/bin/env bash
# What a real Outscale course asks of this emulator, played call by call.
#
# NOT a gate, and the difference matters. Every other script in this directory
# asserts a property and reddens when it fails; this one SURVEYS. It plays the
# calls a published training course tells a reader to type, records what the
# emulator answered, and compares that against a file of expectations somebody
# wrote down. It exits 2 only when a verdict MOVED — a call that used to be
# served and is not, or one that was refused and now passes without anybody
# saying so.
#
# Why a survey and not a gate: nineteen of the fifty-six calls do not pass
# today, and eight of those are refusals this repository argued for on purpose.
# A gate would be red by construction, and CLAUDE.md says what a permanently red
# gate teaches. What is useful is the DIFF against last time.
#
# The subject, measured 2026-08-30: the Outscale course at
# blog.stephane-robert.info — 54 pages, 48 distinct API operations, 94 pairs of
# (operation, parameter). The census that produced this list is in the issue
# this script was written for; the list below is the census applied.
#
# The harness is octl.sh's, deliberately: same fake credentials, same
# config.json, same guard_no_real_profile. A fourth harness in this directory
# would be a fourth place to get the endpoint wrong.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$SCRIPT_DIR/../../.." && pwd)"
ENDPOINT="${1:-http://127.0.0.1:4599}"
EXPECTED="$SCRIPT_DIR/training-expected.tsv"
ACTUAL="${FEINT_SURVEY_OUT:-$(mktemp -d)/actual.tsv}"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/../shared/verdicts.sh" 2>/dev/null || {
  fail() { echo "FAIL: $*" >&2; exit 1; }
  ok() { echo "  ok: $*"; }
}

cd "$REPO"
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# shellcheck disable=SC2034 # read by octl from the environment
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a

command -v octl >/dev/null 2>&1 || {
  echo "  SKIP: octl is not installed, so no call of the course can be played" >&2
  exit 0
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
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

osc() { octl --config "$WORK/config.json" --no-upgrade -o raw iaas api "$@" </dev/null; }

# field <key> — the first value of a key, out of a JSON body on stdin. Written
# in python rather than jq because a course reader is not required to have jq,
# and this script is read as often as it is run.
field() { python3 -c '
import json,sys
try: d = json.load(sys.stdin)
except Exception: print(""); raise SystemExit
def walk(node, key):
    if isinstance(node, dict):
        if key in node and isinstance(node[key], str): return node[key]
        for v in node.values():
            got = walk(v, key)
            if got: return got
    if isinstance(node, list):
        for v in node:
            got = walk(v, key)
            if got: return got
    return ""
print(walk(d, sys.argv[1]))' "$1" 2>/dev/null || true; }

: > "$ACTUAL"
# record <label> <call...> — the verdict, and the API's own reason when refused.
#
# Three outcomes, never two: served, refused BY the emulator with a document,
# and refused by octl itself before a request left. The third reads as a product
# refusal to anybody skimming, and it is not one.
record() {
  local label="$1"; shift
  local body verdict
  if body="$(osc "$@" 2>&1)"; then
    verdict=served
  elif printf '%s' "$body" | grep -q '"Errors"'; then
    verdict=refused
  else
    verdict=client
  fi
  printf '%s\t%s\n' "$label" "$verdict" >> "$ACTUAL"
}

echo "survey: the Outscale course against $ENDPOINT"

# ---- the resources the course assumes exist ---------------------------------
NET="$(osc CreateNet --IpRange=10.20.0.0/16 2>/dev/null | field NetId)"
SUBNET="$(osc CreateSubnet --NetId="$NET" --IpRange=10.20.1.0/24 2>/dev/null | field SubnetId)"
IGW="$(osc CreateInternetService 2>/dev/null | field InternetServiceId)"
EIP="$(osc CreatePublicIp 2>/dev/null | field PublicIpId)"
SG="$(osc CreateSecurityGroup --SecurityGroupName=survey --Description=survey --NetId="$NET" 2>/dev/null | field SecurityGroupId)"
IMG="$(osc ReadImages 2>/dev/null | field ImageId)"

# The course writes `CreateKeypair --KeypairName x` alone and says the response
# carries the private key. This emulator refuses to generate one, deliberately —
# docs/limits.md carries the measurement. The call is surveyed as the course
# writes it, and a usable keypair is then made the way the emulator requires, so
# that this one refusal does not cascade into every call below it.
record "CreateKeypair, as the course writes it" CreateKeypair --KeypairName=survey-nokey
osc CreateKeypair --KeypairName=survey-key \
  --PublicKey="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJk8f5rP0ZzQ2cVn0m5N4tYb7xQeR1sJ2wKpL9dHcMvA survey" \
  >/dev/null 2>&1 || true

VM="$(osc CreateVms --ImageId="$IMG" --VmType=tinav5.c1r1p2 --SubnetId="$SUBNET" \
  --KeypairName=survey-key --MinVmsCount=1 --MaxVmsCount=1 2>/dev/null | field VmId)"
VOL="$(osc CreateVolume --Size=10 --SubregionName="${OSC_REGION}a" --VolumeType=standard 2>/dev/null | field VolumeId)"
SNAP="$(osc CreateSnapshot --VolumeId="$VOL" --Description=survey 2>/dev/null | field SnapshotId)"
RT="$(osc ReadRouteTables 2>/dev/null | field RouteTableId)"

for name in NET SUBNET IGW EIP SG IMG VM VOL SNAP RT; do
  [ -n "${!name}" ] || fail "the survey could not create its $name, so every call below would measure the harness"
done
ok "the ten resources the course assumes are in place"

# ---- reads with no filter ---------------------------------------------------
for op in ReadVms ReadNets ReadSubnets ReadPublicIps ReadSecurityGroups ReadVolumes \
          ReadSnapshots ReadImages ReadRouteTables ReadNatServices ReadNetPeerings \
          ReadLoadBalancers ReadRegions ReadSubregions ReadTags; do
  record "$op" "$op"
done

# ---- reads WITH the filters the course passes -------------------------------
record "ReadVms Filters.VmStateNames"        ReadVms --Filters.VmStateNames=running
record "ReadVms Filters.NetIds"              ReadVms --Filters.NetIds="$NET"
record "ReadVms Filters.ImageIds"            ReadVms --Filters.ImageIds="$IMG"
record "ReadNets Filters.NetIds"             ReadNets --Filters.NetIds="$NET"
record "ReadNets Filters.Tags"               ReadNets --Filters.Tags=env=survey
record "ReadSubnets Filters.NetIds"          ReadSubnets --Filters.NetIds="$NET"
record "ReadSubnets Filters.Tags"            ReadSubnets --Filters.Tags=env=survey
record "ReadPublicIps Filters.Tags"          ReadPublicIps --Filters.Tags=env=survey
record "ReadVolumes Filters.Tags"            ReadVolumes --Filters.Tags=env=survey
record "ReadRouteTables Filters.Tags"        ReadRouteTables --Filters.Tags=env=survey
record "ReadNatServices Filters.Tags"        ReadNatServices --Filters.Tags=env=survey
record "ReadNetPeerings Filters.Tags"        ReadNetPeerings --Filters.Tags=env=survey
record "ReadSnapshots Filters.SnapshotIds"   ReadSnapshots --Filters.SnapshotIds="$SNAP"
record "ReadSnapshots Filters.Descriptions"  ReadSnapshots --Filters.Descriptions=survey
record "ReadSubregions Filters.SubregionNames" ReadSubregions --Filters.SubregionNames="${OSC_REGION}a"
record "ReadImages Filters.ImageIds"         ReadImages --Filters.ImageIds="$IMG"
record "ReadImages Filters.ImageNames"       ReadImages --Filters.ImageNames=none
record "ReadImages Filters.AccountIds"       ReadImages --Filters.AccountIds=123456789012
record "ReadImages Filters.TagKeys"          ReadImages --Filters.TagKeys=env
record "ReadImages Filters.TagValues"        ReadImages --Filters.TagValues=survey

# ---- the operations this pack declines, which the course still teaches ------
for op in ReadAccessKeys ReadUsers ReadFlexibleGpus ReadFlexibleGpuCatalog \
          ReadConsumptionAccount ReadCO2EmissionAccount; do
  record "$op" "$op"
done
record "CreateFlexibleGpu" CreateFlexibleGpu --ModelName=nvidia-p6 --Generation=v5 --SubregionName="${OSC_REGION}a"
record "CreatePolicy"      CreatePolicy --PolicyName=p --Document='{}'

# ---- the writes and links the course chains --------------------------------
record "CreateTags"              CreateTags --ResourceIds="$NET" --Tags.0.Key=env --Tags.0.Value=survey
record "LinkInternetService"     LinkInternetService --InternetServiceId="$IGW" --NetId="$NET"
record "LinkPublicIp"            LinkPublicIp --PublicIpId="$EIP" --VmId="$VM"
record "CreateNatService"        CreateNatService --PublicIpId="$(osc CreatePublicIp 2>/dev/null | field PublicIpId)" --SubnetId="$SUBNET"
record "CreateRoute"             CreateRoute --RouteTableId="$RT" --DestinationIpRange=0.0.0.0/0 --GatewayId="$IGW"
record "LinkVolume"              LinkVolume --VolumeId="$VOL" --VmId="$VM" --DeviceName=/dev/xvdb
record "CreateSecurityGroupRule" CreateSecurityGroupRule --SecurityGroupId="$SG" --Flow=Inbound --IpProtocol=tcp --FromPortRange=22 --ToPortRange=22 --IpRange=0.0.0.0/0
record "CreateImage"             CreateImage --ImageName=survey-img --Description=d --VmId="$VM"
record "CreateNetPeering"        CreateNetPeering --SourceNetId="$NET" --AccepterNetId="$NET"
record "StopVms"                 StopVms --VmIds="$VM"
record "DeleteVms"               DeleteVms --VmIds="$VM"
record "DeleteSnapshot"          DeleteSnapshot --SnapshotId="$SNAP"

# ---- the comparison ---------------------------------------------------------
sort -o "$ACTUAL" "$ACTUAL"
[ -f "$EXPECTED" ] || fail "no $EXPECTED to compare against; write it from a run you have read"

if diff -u <(sort "$EXPECTED") "$ACTUAL" > "$WORK/diff.txt"; then
  served="$(grep -c 'served$' "$ACTUAL" || true)"
  refused="$(grep -c 'refused$' "$ACTUAL" || true)"
  client="$(grep -c 'client$' "$ACTUAL" || true)"
  ok "every one of the $(wc -l < "$ACTUAL") calls answered what was written down: $served served, $refused refused, $client refused by octl itself"
  echo "survey: the course's verdicts are unchanged"
  exit 0
fi

echo
echo "A verdict moved. The left side is what was written down, the right side is this run:"
sed -n '3,40p' "$WORK/diff.txt"
echo
echo "If the move is an improvement, record it: FEINT_SURVEY_OUT=$EXPECTED $0 $ENDPOINT"
echo "A survey that rewrites its own expectations without a reader is a survey that measures nothing."
exit 2
