#!/usr/bin/env bash
# Record what the three real clouds answer, and fold it into the versioned
# shape catalogues.
#
# This is the half of drift detection the SDK scan cannot do. `mise run
# drift:check` reads the upstream SDK and reports what a provider *offers*;
# nothing reported what it *returns*, and that is where this emulator was
# measurably wrong — a real Outscale account showed ReadVms omitting twenty
# fields, every one of which had passed the contract gate.
#
# It runs on an operator's own station, against their own accounts, on purpose.
# There are no cloud credentials in this repository's CI and there will not be:
# what is committed is the field tree (paths and types, no values), never the
# recording.
#
# READ-ONLY. Every call below lists or describes. Nothing is created, so nothing
# has to be cleaned up, and a run interrupted halfway leaves no trace on any
# account. The shapes of *creation* responses need writing and are captured
# deliberately, elsewhere.
#
# Usage:
#   tools/shapes/record.sh                 all three providers
#   tools/shapes/record.sh outscale        one of them
#
# Environment:
#   OSC_PROFILE   Outscale profile to use (default: the first that answers)
#   FEINT_SHAPES  where the catalogues live (default: shapes)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHAPES="${FEINT_SHAPES:-$ROOT/shapes}"
WORK="$(mktemp -d)"; chmod 700 "$WORK"
BIN="$WORK/feint"
PORT=4710

cd "$ROOT" || exit 1

proxy_pid=""
cleanup() {
  local status=$?
  echo
  echo "-- cleanup"
  [ -n "$proxy_pid" ] && kill "$proxy_pid" 2>/dev/null && echo "   proxy stopped"
  # The work directory holds transcripts of real accounts and, for Outscale, a
  # config carrying real credentials. It goes whatever happened.
  rm -rf "$WORK"
  echo "   recordings and temporary credentials removed"
  exit "$status"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$1"; }
note() { printf '   %s\n' "$1"; }

# run_client drives one client through the proxy and folds the result in.
#
# Each call is announced before it is made and capped: a batch that goes quiet
# for a minute is indistinguishable from a batch that has hung, and the one
# thing worse than a slow recording is one nobody can tell is still working.
record_provider() {
  local provider="$1" upstream="$2"; shift 2
  local transcript="$WORK/$provider.jsonl"

  step "$provider — proxy 127.0.0.1:$PORT -> $upstream"
  "$BIN" proxy --provider "$provider" --upstream "$upstream" \
    --addr "127.0.0.1:$PORT" --record "$transcript" >"$WORK/$provider-proxy.log" 2>&1 &
  proxy_pid=$!
  sleep 2
  if ! kill -0 "$proxy_pid" 2>/dev/null; then
    note "FAILED to start the proxy:"
    sed 's/^/     /' "$WORK/$provider-proxy.log"
    return 1
  fi
  note "listening (pid $proxy_pid)"

  step "$provider — driving the official client, read-only"
  "record_$provider" "$transcript"

  kill "$proxy_pid" 2>/dev/null; proxy_pid=""
  sleep 1

  if [ ! -s "$transcript" ]; then
    note "nothing was recorded; the client reached nothing through the proxy"
    return 1
  fi
  note "$(wc -l < "$transcript") exchange(s) recorded"

  step "$provider — folding into the catalogue"
  "$BIN" shapes "$transcript" --provider "$provider" --dir "$SHAPES"
  PORT=$((PORT + 1))
}

# --- Scaleway -----------------------------------------------------------------
# scw takes its endpoint from SCW_API_URL, which the conformance suite uses too.
record_scaleway() {
  # The zone and region come from the environment, never from a flag: `scw
  # instance server list --zone=...` answers "unknown flag: --zone". That is how
  # tools/conformance/scaleway/scw-cli.sh does it, and inventing the flag cost a
  # run where thirteen of fifteen calls were refused before they left the
  # process — so the proxy recorded nothing and the catalogue looked empty for a
  # reason that had nothing to do with the cloud.
  export SCW_API_URL="http://127.0.0.1:$PORT"
  export SCW_DEFAULT_ZONE=fr-par-1
  export SCW_DEFAULT_REGION=fr-par
  local calls=(
    "instance server list"
    "instance ip list"
    "instance volume list"
    "instance security-group list"
    "instance image list"
    "instance snapshot list"
    "instance placement-group list"
    "instance server-type list"
    "vpc vpc list"
    "vpc private-network list"
    "ipam ip list"
    "iam ssh-key list"
    "iam application list"
    "marketplace image list"
    "block volume list"
    "block snapshot list"
  )
  for c in "${calls[@]}"; do
    printf '   scw %-46s ' "$c"
    # shellcheck disable=SC2086
    if timeout 45 scw $c -o json >/dev/null 2>&1; then echo "ok"; else echo "refused or absent"; fi
  done
  unset SCW_API_URL SCW_DEFAULT_ZONE SCW_DEFAULT_REGION
}

# --- Outscale -----------------------------------------------------------------
# Outscale is recorded WITHOUT the proxy, and that is a boundary rather than a
# shortcut: SigV4 signs the Host header, so a client pointed at 127.0.0.1 signs
# 127.0.0.1 and the real cloud recomputes the signature from what it received —
# every authenticated call answers InvalidParameterValue 4120 while the public
# ones still work and hide the cause. A reverse proxy cannot make a client sign a
# host it was never given; lifting that needs DNS interception and TLS
# termination, which is #76.
#
# tools/shapes/osc_read.py signs the real host, talks to it directly, and writes
# the same trace.Exchange shape the proxy emits, so `feint shapes` folds it
# without knowing the difference. It refuses any call that is not a Read.
record_outscale_direct() {
  local transcript="$1"
  step "outscale — reading directly (the proxy cannot carry SigV4, see the note above)"
  timeout 900 python3 tools/shapes/osc_read.py "$transcript"
}

# --- Exoscale -----------------------------------------------------------------
# exo has no endpoint flag: EXOSCALE_API_ENDPOINT carries the /v2 suffix, which
# the CLI concatenates with the route rather than replacing it.
record_exoscale() {
  export EXOSCALE_API_ENDPOINT="http://127.0.0.1:$PORT/v2"
  local calls=(
    "compute instance list" "compute instance-type list" "compute template list"
    "compute security-group list" "compute elastic-ip list" "compute ssh-key list"
    "compute anti-affinity-group list" "compute private-network list"
    "compute block-storage volume list" "compute block-storage snapshot list"
    "compute instance-pool list" "compute load-balancer list"
    "compute deploy-target list" "compute instance-template list"
    "dns list" "zone list" "limits"
  )
  for c in "${calls[@]}"; do
    printf '   exo %-42s ' "$c"
    # shellcheck disable=SC2086
    if timeout 45 exo $c -O json >/dev/null 2>&1; then echo "ok"; else echo "refused or absent"; fi
  done
  unset EXOSCALE_API_ENDPOINT
}

# --- main ---------------------------------------------------------------------
step "building"
if ! timeout 300 go build -o "$BIN" ./cmd/feint; then
  note "build failed"; exit 1
fi
note "ok"

wanted=("$@")
[ ${#wanted[@]} -eq 0 ] && wanted=(scaleway outscale exoscale)

failed=()
for p in "${wanted[@]}"; do
  case "$p" in
    scaleway) record_provider scaleway https://api.scaleway.com || failed+=("$p") ;;
    outscale)
      t="$WORK/outscale.jsonl"
      if record_outscale_direct "$t" && [ -s "$t" ]; then
        step "outscale — folding into the catalogue"
        "$BIN" shapes "$t" --provider outscale --dir "$SHAPES"
      else
        failed+=("$p")
      fi ;;
    exoscale) record_provider exoscale https://api-ch-gva-2.exoscale.com || failed+=("$p") ;;
    *) note "unknown provider: $p"; failed+=("$p") ;;
  esac
done

step "done"
for f in "$SHAPES"/*.json; do
  [ -e "$f" ] || continue
  n=$(python3 -c "import json,sys; print(len(json.load(open(sys.argv[1]))['operations']))" "$f" 2>/dev/null || echo '?')
  note "$(basename "$f"): $n operation(s) described"
done
if [ ${#failed[@]} -gt 0 ]; then
  note "provider(s) that recorded nothing: ${failed[*]}"
  exit 1
fi
note "read \`git diff -- $SHAPES\` : that diff is the behavioural drift report"
