#!/usr/bin/env bash
# Two observers of one run, over the real client.
#
# `feint proxy` watches an exchange from outside, with nothing but the route
# table; /_feint/conformance counts it from inside the process that answered it.
# The set of operations they report must be identical. A divergence means one of
# them is lying, and either being wrong matters: the transcript is what X-4 (#74)
# will rank a backlog from, and the counter is what the conformance score is
# computed from.
#
# It starts its own emulator rather than using a running one, and that is the
# correction of a real failure of this script: /_feint/conformance counts for the
# life of the process, so against a reused emulator the second run compares a
# transcript with an empty set of new operations and passes by accident. Taking a
# before/after difference does not fix it either — the operations of run two are
# not new. A fresh process is the only thing that makes "the same run" mean
# anything.
#
# It is not part of `mise run conformance`, on purpose and twice over. #72 asks
# for that suite to stay unchanged and green with the proxy out of its path, and
# recording is a human's job: this script proves the mechanism against the
# emulator, and the thing worth recording — a real cloud — needs an account that
# will never exist in a runner.
#
# Usage: tools/conformance/proxy.sh [emulator addr] [proxy addr]
set -euo pipefail

EMU_ADDR="${1:-127.0.0.1:4699}"
PROXY_ADDR="${2:-127.0.0.1:4700}"
ENDPOINT="http://$EMU_ADDR"

# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/guard.sh"
guard_local "$ENDPOINT"

command -v jq >/dev/null 2>&1 || { echo "jq is not installed" >&2; exit 1; }
command -v scw >/dev/null 2>&1 || { echo "scw is not installed" >&2; exit 1; }
[ -x ./feint ] || { echo "./feint is not built: run mise run build" >&2; exit 1; }

work="$(mktemp -d)"
transcript="$work/run.jsonl"
proxy_pid=""
cleanup() {
  [ -n "$proxy_pid" ] && kill "$proxy_pid" 2>/dev/null
  ./feint stop --addr "$EMU_ADDR" >/dev/null 2>&1
  rm -rf "$work"
  return 0
}
trap cleanup EXIT

echo "conformance: two observers of one scw run"
echo "- a fresh emulator on $EMU_ADDR"
./feint start --addr "$EMU_ADDR" --vm off --timeout 60s >"$work/emulator.log" 2>&1 \
  || { echo "FAIL: the emulator did not start"; cat "$work/emulator.log"; exit 1; }

echo "- proxy $PROXY_ADDR -> $ENDPOINT"
./feint proxy --upstream "$ENDPOINT" --addr "$PROXY_ADDR" --record "$transcript" \
  >"$work/proxy.log" 2>&1 &
proxy_pid=$!

# Wait for the proxy rather than sleeping: a fixed sleep is how a suite becomes
# flaky on a loaded machine, and it is the pattern the lifecycle verbs replaced
# everywhere else in this repository.
#
# The listener is opened, not requested. A proxy is transparent by design — it has
# no health endpoint of its own and forwards /_feint/health to the emulator like
# anything else — so an HTTP readiness probe would put an exchange in the
# transcript and report it as a route no pack claims. Measured: the first version
# of this script did exactly that.
ready=""
for _ in $(seq 1 100); do
  if (exec 3<>"/dev/tcp/${PROXY_ADDR%%:*}/${PROXY_ADDR##*:}") 2>/dev/null; then ready=yes; break; fi
  sleep 0.1
done
[ -n "$ready" ] || { echo "FAIL: the proxy never listened"; cat "$work/proxy.log"; exit 1; }

echo "- driving the real scw CLI through the proxy"
tools/conformance/scaleway/scw-cli.sh "http://$PROXY_ADDR" >"$work/scw.log" 2>&1 \
  || { echo "FAIL: the CLI suite did not pass through the proxy"; cat "$work/scw.log"; exit 1; }

# Read before the proxy is stopped, because stopping it is what drains the
# transcript, and after that the emulator's counters no longer move.
from_emulator="$(curl -fsS "$ENDPOINT/_feint/conformance" | jq -r '.calls | keys[]' | sort)"

# Stopped before the transcript is read: the writer drains on shutdown, and
# reading the file while it is still being written is how a comparison becomes a
# race.
kill "$proxy_pid"
wait "$proxy_pid" 2>/dev/null || true
proxy_pid=""

from_proxy="$(jq -r 'select(.operation != null and .operation != "") | .operation' "$transcript" | sort -u)"
lines="$(wc -l <"$transcript")"
unnamed="$(jq -r 'select(.mounted == false) | .method + " " + .path' "$transcript" | sort -u || true)"
echo "  transcript: $lines exchange(s), $(printf '%s' "$from_proxy" | grep -c . || true) distinct operation(s)"

if [ -z "$from_proxy" ]; then
  echo "FAIL: the proxy named no operation, so nothing was compared" >&2
  exit 1
fi
if ! diff <(echo "$from_emulator") <(echo "$from_proxy") >"$work/diff" 2>&1; then
  echo "FAIL: the two observers disagree (< emulator only, > proxy only)" >&2
  cat "$work/diff" >&2
  exit 1
fi

# The credentials the suite uses are fake, public and well-formed, which is what
# makes them usable as markers: the token the CLI sends must not survive into the
# transcript, and the header carrying it must.
# has() first, deliberately: without it a record that carries no such header
# compares null against the placeholder, reports a leak, and the check fails on
# every transcript for a reason that has nothing to do with a credential.
if jq -e 'select(.req.headers | has("X-Auth-Token")) | select(.req.headers["X-Auth-Token"] != "REDACTED")' \
     "$transcript" >/dev/null 2>&1; then
  echo "FAIL: an X-Auth-Token reached the transcript unredacted" >&2
  exit 1
fi
if ! jq -e 'select(.req.headers["X-Auth-Token"] == "REDACTED")' "$transcript" >/dev/null 2>&1; then
  echo "FAIL: no redacted X-Auth-Token anywhere, so the check above measured nothing" >&2
  exit 1
fi

if [ -n "$unnamed" ]; then
  echo "  routes the real client walked and no pack claims:"
  # Indented with a parameter expansion rather than sed: shellcheck's SC2001
  # refuses the sed form at --severity=style, and the pre-commit hook runs it.
  printf '    %s\n' "${unnamed//$'\n'/$'\n'    }"
fi
echo "conformance: two observers agree on $(printf '%s' "$from_proxy" | grep -c . || true) operation(s)"
