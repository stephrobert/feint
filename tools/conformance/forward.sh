#!/usr/bin/env bash
# The real client, through a CONNECT, landing on the emulator (#357).
#
# `proxy.sh` proves the reverse half: a client pointed at the proxy, forwarded to
# one upstream. This proves the other one — the half that could not be expressed
# before #357: a client that reaches its endpoint by *name*, terminated by the
# forward proxy, recorded, and re-originated to the emulator instead of to the
# real cloud. No namespace, no /etc/hosts edit, no privileged port: two
# environment variables and one `=`.
#
# **Why the endpoint is a name that cannot resolve.** api.scaleway.test is under
# a reserved TLD (RFC 6761): no resolver anywhere answers for it. So if the proxy
# is not what carries this traffic — it died, the mapping is missing, the client
# ignored HTTPS_PROXY — the run fails with a DNS error and **nothing leaves the
# machine**. Pointing this at the real api.scaleway.com would prove the same
# mechanism and would make the proxy the only thing standing between a broken
# suite and the internet, which is the shape guard.sh exists to refuse.
#
# It is not part of `mise run conformance`, for proxy.sh's reasons: recording is
# a human's job, and this suite's subject is the recorder rather than the pack.
#
# Usage: tools/conformance/forward.sh [emulator addr] [proxy addr]
set -euo pipefail

EMU_ADDR="${1:-127.0.0.1:4701}"
PROXY_ADDR="${2:-127.0.0.1:4702}"
ENDPOINT="http://$EMU_ADDR"

# The name the client believes it is talking to. Reserved, unresolvable, and
# asserted below rather than trusted.
FAKE_CLOUD="api.scaleway.test"

# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/guard.sh"
guard_local "$ENDPOINT"

# The endpoint the client is given is the one thing this suite's safety rests on,
# so it is asserted rather than assumed. A name outside .test would resolve, and
# the run would then depend on the proxy to stay offline.
case "$FAKE_CLOUD" in
  *.test) ;;
  *) echo "FAIL: $FAKE_CLOUD is not under the reserved .test TLD, so it may resolve" >&2; exit 1 ;;
esac

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

echo "conformance: a real client reaches $FAKE_CLOUD and lands on the emulator"
echo "- a fresh emulator on $EMU_ADDR"
./feint start --addr "$EMU_ADDR" --vm off --timeout 60s >"$work/emulator.log" 2>&1 \
  || { echo "FAIL: the emulator did not start"; cat "$work/emulator.log"; exit 1; }

echo "- forward proxy $PROXY_ADDR, $FAKE_CLOUD -> $ENDPOINT"
./feint proxy --forward "$FAKE_CLOUD=$ENDPOINT" --addr "$PROXY_ADDR" --record "$transcript" \
  >"$work/proxy.log" 2>&1 &
proxy_pid=$!

# The listener is opened, not requested: proxy.sh's reason, and one more here —
# an HTTP probe of a forward proxy is answered with a 400 telling the caller to
# set HTTPS_PROXY, which is correct and would still put nothing useful anywhere.
ready=""
for _ in $(seq 1 100); do
  if (exec 3<>"/dev/tcp/${PROXY_ADDR%%:*}/${PROXY_ADDR##*:}") 2>/dev/null; then ready=yes; break; fi
  sleep 0.1
done
[ -n "$ready" ] || { echo "FAIL: the proxy never listened"; cat "$work/proxy.log"; exit 1; }

ca="$(grep -o '/tmp/feint-intercept-ca-[^ ]*\.pem' "$work/proxy.log" | head -1)"
[ -n "$ca" ] || { echo "FAIL: the proxy named no CA file"; cat "$work/proxy.log"; exit 1; }
[ -s "$ca" ] || { echo "FAIL: the CA at $ca is empty"; exit 1; }

# Fake but well-formed, the same public values every suite here uses: the SDK
# validates the shape of a credential even though the emulator ignores its value.
export SCW_ACCESS_KEY="SCWXXXXXXXXXXXXXXXXX"
export SCW_SECRET_KEY="11111111-1111-1111-1111-111111111111"
export SCW_DEFAULT_PROJECT_ID="11111111-1111-1111-1111-111111111111"
export SCW_DEFAULT_ORGANIZATION_ID="11111111-1111-1111-1111-111111111111"
export SCW_DEFAULT_ZONE="fr-par-1"
# The whole point: the client is given a *name*, not this emulator's address, and
# two environment variables carry it here.
export SCW_API_URL="https://$FAKE_CLOUD"
export SSL_CERT_FILE="$ca"
export HTTPS_PROXY="http://$PROXY_ADDR"
export https_proxy="http://$PROXY_ADDR"
export NO_PROXY=""
export no_proxy=""

echo "- driving scw at https://$FAKE_CLOUD through HTTPS_PROXY"
created="$(scw instance server create name=forward-1 type=DEV1-S zone=fr-par-1 -o json 2>&1)" \
  || { echo "FAIL: the CLI did not reach the emulator through the tunnel: $created"; cat "$work/proxy.log"; exit 1; }
id="$(printf '%s' "$created" | jq -r '.id // empty')"
[ -n "$id" ] || { echo "FAIL: no id in the create response: $created"; exit 1; }
scw instance server list zone=fr-par-1 -o json >/dev/null || { echo "FAIL: list rejected"; exit 1; }

# What the emulator itself counted, read before anything is stopped and from
# outside the tunnel: a transcript alone could be produced by a proxy talking to
# itself, and this is the second observer that says it was not.
emu_seen="$(curl -fsS "$ENDPOINT/_feint/conformance" | jq -r '.calls | keys[]' | sort)"

# Stopped before the transcript is read: the writer drains on shutdown, and
# reading the file while it is still being written is how a comparison becomes a
# race.
kill "$proxy_pid"; wait "$proxy_pid" 2>/dev/null || true
proxy_pid=""

lines="$(wc -l <"$transcript")"
[ "$lines" -gt 0 ] || { echo "FAIL: nothing was recorded, so nothing below measured anything"; exit 1; }
echo "  transcript: $lines exchange(s)"

# 1. The transcript names the host the client asked for, never the socket it was
#    sent to. This is what separates a mapped --forward from --upstream, which
#    would have recorded 127.0.0.1 and lost the one fact a recording is for.
hosts="$(jq -r '.host' "$transcript" | sort -u)"
[ "$hosts" = "$FAKE_CLOUD" ] \
  || { echo "FAIL: the transcript names host(s) [$hosts], want $FAKE_CLOUD alone" >&2; exit 1; }

# 2. The route table still names operations through the mapped door.
named="$(jq -r 'select(.operation != null and .operation != "") | .operation' "$transcript" | sort -u)"
[ -n "$named" ] || { echo "FAIL: no exchange was named, so the table did not reach this door" >&2; exit 1; }
while IFS= read -r op; do
  printf '%s\n' "$emu_seen" | grep -qx "$op" \
    || { echo "FAIL: the proxy recorded $op and the emulator never counted it" >&2; exit 1; }
done <<<"$named"

# 3. The redaction survives the mapped tunnel. has() first, then the assertion,
#    so a transcript carrying no such header fails as "measured nothing" rather
#    than passing by comparing null against the placeholder (proxy.sh's lesson).
jq -e 'select(.req.headers["X-Auth-Token"] == "REDACTED")' "$transcript" >/dev/null 2>&1 \
  || { echo "FAIL: no redacted X-Auth-Token anywhere, so the check below measured nothing" >&2; exit 1; }
if jq -e 'select(.req.headers | has("X-Auth-Token")) | select(.req.headers["X-Auth-Token"] != "REDACTED")' \
     "$transcript" >/dev/null 2>&1; then
  echo "FAIL: an X-Auth-Token reached the transcript unredacted" >&2
  exit 1
fi

# 4. Nothing in this run went anywhere the operator did not name. A refusal here
#    would mean the client reached for a host the mapping does not cover, and
#    that call is absent from the transcript above — the silent gap the whole
#    allowlist exists to make loud.
if grep -q "does not name" "$work/proxy.log"; then
  echo "FAIL: a host was refused in this run, so the transcript is missing calls:" >&2
  grep "does not name" "$work/proxy.log" >&2
  exit 1
fi

echo "conformance: $(printf '%s' "$named" | grep -c .) operation(s) recorded through a CONNECT to $FAKE_CLOUD, served by $ENDPOINT"
