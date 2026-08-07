#!/usr/bin/env bash
# Check the page in a real browser, and photograph it.
#
# Same shape as tools/conformance/: start an emulator, drive it with real calls,
# assert against it, stop. The difference is the client — a headless browser
# instead of scw or terraform — and what it proves: that the page's nodes carry
# the values the endpoints answered. Four Go tests hold the asset as text and
# none of them runs a line of its JavaScript, which docs/limits.md records.
#
# The assertions are the point and the images are a by-product. `--check` runs
# the assertions and writes nothing, which is what a pull request needs; the
# default writes docs/assets/ui/ and the manifest that `feint docs --check`
# reads to know whether they are stale.
#
# Usage: tools/ui/screenshots.sh [--check]
# Exit:  0 ok, 1 an assertion failed, 3 no browser (nothing was measured).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

CHECK=""
[ "${1:-}" = "--check" ] && CHECK="--check"

ADDR="${FEINT_UI_ADDR:-127.0.0.1:4622}"
ENDPOINT="http://$ADDR"
OUT="docs/assets/ui"
BIN="$(mktemp -d)/feint"
trap 'rm -rf "$(dirname "$BIN")"' EXIT

echo "page: building"
go build -o "$BIN" ./cmd/feint

# --contracts is on, because one of the two verdict lines the log carries only
# exists when a response is checked against its provider's own description. A run
# without it would photograph half the feature.
"$BIN" start --addr "$ADDR" --contracts contracts --timeout 60s >/dev/null
trap '"$BIN" stop --addr "$ADDR" >/dev/null 2>&1 || true; rm -rf "$(dirname "$BIN")"' EXIT

post() { curl -sf -X POST -H 'Content-Type: application/json' -d "$2" "$ENDPOINT$1" >/dev/null; }

# Real calls through the packs' real routes, because the page must be shown with
# what a client actually produces. Two servers rather than one, so a group has
# more than a single row; one call carrying a field no handler reads, which is
# the log line this project cares most about; and one call to a route nobody
# mounts, which is how a plan dies.
echo "page: driving the emulator"
Z="/instance/v1/zones/fr-par-1"
post "$Z/servers" '{"name":"web-1","commercial_type":"DEV1-S","tags":["demo","web"]}'
post "$Z/servers" '{"name":"db-1","commercial_type":"DEV1-M","placement_group":"nope"}'
post "$Z/ips" '{"project":"11111111-1111-1111-1111-111111111111"}'
post "/vpc/v2/regions/fr-par/vpcs" '{"name":"demo-vpc"}'
post "/vpc/v2/regions/fr-par/private-networks" '{"name":"demo-net","subnets":["10.10.0.0/24"]}'
post "/api/v1/CreateKeypair" '{"KeypairName":"demo","PublicKey":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD demo@feint"}'
post "/api/v1/CreateVms" '{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","KeypairName":"demo"}'
post "/v2/ssh-key" '{"name":"exo-key","public-key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD demo@feint"}'
post "/v2/instance" '{"name":"exo-1","instance-type":{"id":"21624abb-764e-4def-81d7-9fc54b5957fb"},"template":{"id":"11111111-1111-1111-1111-111111111111"},"disk-size":10,"ssh-keys":[{"name":"exo-key"}]}'
curl -sf "$Z/servers" -o /dev/null || true
curl -sf "$ENDPOINT$Z/servers" -o /dev/null
# The last call is the one that finds nothing, so it is the last line of the log.
curl -s -o /dev/null -X POST -H 'Content-Type: application/json' \
  -d '{"SubnetId":"subnet-12345678"}' "$ENDPOINT/api/v1/CreateNatService" || true

echo "page: checking it in a browser"
status=0
python3 tools/ui/check-page.py --endpoint "$ENDPOINT" --out "$OUT" ${CHECK:+--check} || status=$?

if [ "$status" = 3 ]; then
  # Said loudly, never silently. A check that skips is a check that measures
  # nothing, and this repository has already shipped one of those: the network
  # suite returned SKIP in CI from the day it was written.
  echo "page: SKIPPED - no browser on this machine, so the page's DOM was not checked" >&2
  exit 3
fi
[ "$status" = 0 ] || exit "$status"

if [ -z "$CHECK" ]; then
  # The manifest is what puts the images on the repository's existing freshness
  # rail: `feint docs --check` compares the digest below with the page it serves,
  # and the pre-commit hook and the release preflight already run that command.
  #
  # It carries no date, on purpose. The drift workflow decides that the upstream
  # surface moved with `git diff --quiet -- coverage/`, and a field that changed
  # on every run would open a pull request every Monday. The same trap applies
  # here: a manifest that moved on every run would make this gate noise.
  "$BIN" docs --ui-manifest "$OUT/manifest.json"
  echo "page: manifest written to $OUT/manifest.json"
fi
