#!/usr/bin/env bash
# Conformance check: an emulator that can refuse, and real clients meeting it
# (#26, #356).
#
# The measurement that justifies this file is in coverage/evidence.json: six of
# the seven evidence axes stand above 85% and `negative` stands at 34 of 357.
# This emulator proved what its routes answer when everything goes well and
# almost nothing about what they answer when it does not — so a client's
# degradation paths could only be simulated in the client's own tests, never
# observed against something answering on a real socket.
#
# What each leg proves, and none of it is a unit test:
#
#   1. `scw` decodes an injected 403 as PermissionsDeniedError, and a real 404
#      as ResourceNotFoundError. The client can tell "I am not allowed" from
#      "it does not exist" — the whole of #356's case, since a CSPM whose
#      doctrine is that an incomplete collection must never produce a PASS has
#      to degrade one and not the other.
#   2. `scw` survives 429, 429, 200. Its retry and its backoff are exercised
#      here for the first time, against something answering the way its cloud
#      would.
#   3. The real Outscale Terraform provider survives 503, 503, 200 inside an
#      apply, and says so in its own log.
#   4. `exo` and `octl` meet the same 403 in their own dialects, so what a
#      client decodes is its provider's error and never this tool's.
#   5. The evidence separation, observed rather than asserted: an operation that
#      only ever answered injected faults stays un-driven and un-proven, and a
#      `negative` assertion span cannot be closed on an injected refusal. If
#      that failed, this feature would be raising the very number it was built
#      to expose.
#
# It owns its emulator, on its own port, and that is not tidiness: `mise run
# conformance` drives every other suite against one shared process, and an armed
# rule there would put staged answers into the run whose numbers describe served
# behaviour. tools/conformance/score.sh refuses a run carrying any injected
# answer, and this suite is what would trip it.
#
# Usage: tools/conformance/faults.sh   (port: FEINT_FAULTS_PORT, default 4663)
set -euo pipefail

PORT="${FEINT_FAULTS_PORT:-4663}"
ADDR="127.0.0.1:$PORT"
ENDPOINT="http://$ADDR"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Resolved here rather than where it is used, and that is the whole point:
# the expression is relative to the script's own file, so it has to be made
# absolute while the working directory is still the one the caller started
# in. Placed next to the first use, it ran after a `cd` and answered "No such
# file or directory" on tools/conformance/<provider>/../../ci (#895).
TF_RETRY="$(cd "$(dirname "${BASH_SOURCE[0]}")/../ci" && pwd)/tf-retry.sh"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
# The rule files and the Terraform fixture, located the way every suite here
# locates its own. The idiom is what the generated fixture table reads to know
# CI applies this directory (internal/cli/docs_proved.go, fixturesAppliedInCI),
# so it is written exactly like the others rather than as a bare path.
RULES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/faults" && pwd)"

# Never let a client reach anything but the local emulator. See guard.sh for the
# incident that wrote this rule.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "$ENDPOINT"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

command -v jq >/dev/null 2>&1 || fail "jq is not installed"
command -v scw >/dev/null 2>&1 || fail "scw is not installed"
command -v octl >/dev/null 2>&1 || fail "octl is not installed"
command -v exo >/dev/null 2>&1 || fail "exo is not installed"

TF="${FEINT_TF:-terraform}"
command -v "$TF" >/dev/null 2>&1 || fail "$TF is not installed"

FEINT_BIN="${FEINT_BIN:-$REPO_DIR/feint}"
[ -x "$FEINT_BIN" ] || fail "no feint binary at $FEINT_BIN (build it: mise run build)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; "$FEINT_BIN" stop --addr "$ADDR" >/dev/null 2>&1 || true' EXIT

echo "conformance: fault injection against $ENDPOINT"

echo "- an emulator of its own, so no armed rule ever reaches the shared run"
# --objectstore is here for the `feint run` leg below and for nothing else:
# that command asks the emulator which S3 doors it serves and refuses a lab
# with none, because intercepting a client's object storage is half of what it
# promises. It changes no other leg: no suite here writes a byte.
"$FEINT_BIN" start --addr "$ADDR" --contracts "$REPO_DIR/contracts" \
  --objectstore "$WORK/objects" --timeout 60s

# arm replaces the whole rule set from a committed file, which is what makes a
# scenario replayable: whatever ran before, the emulator is in the state the
# file describes.
arm() {
  local file="$1" answer
  answer="$(curl -s -w '\n%{http_code}' -X PUT "$ENDPOINT/_feint/faults" --data-binary "@$file")"
  [ "${answer##*$'\n'}" = "200" ] || fail "arming $file was refused: ${answer%$'\n'*}"
}
disarm() { curl -sf -X DELETE "$ENDPOINT/_feint/faults" >/dev/null || fail "clearing the rules failed"; }
hits() { curl -sf "$ENDPOINT/_feint/faults" | jq -r --arg op "$1" '.faults[] | select(.operation == $op) | .hits'; }

echo "- off by default: a fresh emulator arms nothing"
armed="$(curl -sf "$ENDPOINT/_feint/faults" | jq -r '.faults | length')"
[ "$armed" = "0" ] || fail "a fresh emulator already arms $armed rules; off by default is non-negotiable"
ok "no rule is armed until one is asked for"

# ---------------------------------------------------------------- Scaleway ---
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/scaleway/fake-credentials.env"
# shellcheck disable=SC2034 # read by scw from the environment, not here
SCW_API_URL="$ENDPOINT"
set +a
guard_no_real_profile SCW_API_URL scw

echo "- scw tells a refusal from an absence: the 403 is decoded, not transported"
arm "$RULES_DIR/refusals.json"
refused="$(scw instance server list zone=fr-par-1 </dev/null 2>&1 || true)"
# PermissionsDeniedError.Error() in scaleway-sdk-go: "scaleway-sdk-go:
# insufficient permissions: <action> <resource>". Anything else means the SDK
# fell back to an untyped error, and errors.As stopped matching — which is the
# exact failure a consumer branching on the classification would meet.
printf '%s' "$refused" | grep -qi "insufficient permissions" \
  || fail "scw did not decode the injected 403 as a permissions error: $refused"
disarm
# The other half, and the one that makes the first mean something: a real 404,
# with nothing armed, has to read differently. A client that cannot separate
# them would degrade the wrong control.
absent="$(scw instance server get 11111111-1111-1111-1111-111111111111 zone=fr-par-1 </dev/null 2>&1 || true)"
printf '%s' "$absent" | grep -qi "cannot find resource" \
  || fail "a real 404 did not read as a missing resource: $absent"
if printf '%s' "$absent" | grep -qi "insufficient permissions"; then
  fail "a missing resource reads as a refusal: the two classifications are the same to this client"
fi
ok "scw: injected 403 is 'insufficient permissions', real 404 is 'cannot find resource'"

echo "- scw survives 429, 429, 200: its retry and its backoff, exercised"
arm "$RULES_DIR/transients.json"
scw instance server list zone=fr-par-1 </dev/null >/dev/null 2>&1 \
  || fail "scw gave up on two 429s; the rule fired $(hits instance/v1/API.ListServers) times"
[ "$(hits instance/v1/API.ListServers)" = "2" ] \
  || fail "the 429 rule fired $(hits instance/v1/API.ListServers) times, want exactly 2"
ok "scw retried through two 429s and completed"
disarm

# A schedule, and the receipt that says which call it hit (#851).
#
# The rule names the third call and no other. Driven by the real client, so the
# claim is not "the unit test agrees with itself": scw makes three list calls,
# the first two answer and the third does not, and the receipt names the rank.
#
# This is the only place a real client exercises `nth` and `fired`, and without
# it the whole schedule rests on unit tests and a frozen fixture.
echo "- a rule can name the third call, and the receipt says which one it hit"
arm "$RULES_DIR/third-call.json"
for n in 1 2; do
  scw instance server list zone=fr-par-1 </dev/null >/dev/null 2>&1 \
    || fail "call $n of the campaign failed, and only the third should"
done
if scw instance server list zone=fr-par-1 </dev/null >/dev/null 2>&1; then
  fail "the third call succeeded; the rule named it and did not fire"
fi
[ "$(hits instance/v1/API.ListServers)" = "1" ] \
  || fail "the nth rule fired $(hits instance/v1/API.ListServers) times, want exactly 1"
rank="$(curl -sf "$ENDPOINT/_feint/faults" | jq -r '.fired[0].rank')"
[ "$rank" = "3" ] \
  || fail "the receipt says the firing hit call $rank, want 3"
calls="$(curl -sf "$ENDPOINT/_feint/faults" | jq -r '.faults[0].calls')"
[ "$calls" = "3" ] \
  || fail "the rule saw $calls call(s), want 3: a declined call must still be counted"
ok "scw: calls 1 and 2 answered, call 3 refused, and the receipt names rank 3"


# --------------------------------------------------------------- Outscale ---
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/outscale/fake-credentials.env"
# The endpoint carries /api/v1: octl reads it from osc-sdk-go, whose default is
# https://api.<region>.outscale.com/api/v1, so the path is part of the value —
# the opposite of the archived oapi-cli this replaced (#460).
# shellcheck disable=SC2034 # read by octl from the environment
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a
guard_no_real_profile OSC_ENDPOINT_API octl

cat > "$WORK/osc.json" <<EOF
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

echo "- octl decodes the injected 403 in Outscale's own envelope"
arm "$RULES_DIR/refusals.json"
# -o raw, because the default -o json RESHAPES the answer and would hand this
# assertion the CLI's own arrangement instead of the API's bytes; `iaas api
# ReadNets` rather than the `iaas net list` alias, because the API is what is
# measured. Both rules are stated at length in outscale/octl.sh.
#
# 2>&1 folds the streams on purpose here: octl writes the error document to
# stderr under its own "The server returned an error" line, and jq reads a
# document, not a line — so the fold is followed by the same brace-onwards
# extraction the suite uses.
osc_refused="$(octl --config "$WORK/osc.json" --no-upgrade -o raw iaas api ReadNets </dev/null 2>&1 || true)"
osc_refused="$(printf '%s\n' "$osc_refused" | awk 'BEGIN { found = 0 }
  { if (!found) { i = index($0, "{"); if (i > 0) { print substr($0, i); found = 1 } } else print }')"
# 4120 is in osc.IsAuthError's explicit list (pkg/osc/errors.go), so a client
# asking "was I refused for lack of a grant" gets a true answer — and
# osc.IsNotFound, which reads 5000-5999, gets a false one.
printf '%s' "$osc_refused" | jq -e '.Errors[0].Code == "4120"' >/dev/null 2>&1 \
  || fail "octl did not decode the injected 403 as an Outscale auth error: $osc_refused"
printf '%s' "$osc_refused" | jq -e '.ResponseContext.RequestId | length > 0' >/dev/null 2>&1 \
  || fail "the injected refusal carries no ResponseContext: it is not Outscale's envelope"
ok "octl: Code 4120 inside a ResponseContext, which osc.IsAuthError reads"

echo "- terraform apply survives 503, 503, 200 on ReadNets"
cp "$RULES_DIR"/*.tf "$WORK/"
(cd "$WORK" && "$TF_RETRY" "$TF" faults init -no-color -upgrade) \
  || fail "terraform init failed"
arm "$RULES_DIR/transients.json"
if ! (cd "$WORK" && TF_IN_AUTOMATION=1 TF_INPUT=0 timeout 300 "$TF" apply -no-color -auto-approve \
      -var "endpoint=$ENDPOINT" >apply.out 2>&1); then
  cat "$WORK/apply.out" >&2
  fail "terraform gave up on two 503s; the rule fired $(hits osc/Client.ReadNets) times"
fi
grep -q "Apply complete" "$WORK/apply.out" || { cat "$WORK/apply.out" >&2; fail "no apply completed"; }
[ "$(hits osc/Client.ReadNets)" = "2" ] \
  || fail "the 503 rule fired $(hits osc/Client.ReadNets) times, want exactly 2: if it fired 0, the apply never reached the operation and this leg proves nothing"
ok "503, 503, 200 -> Apply complete, and the rule reports exactly two hits"
(cd "$WORK" && TF_IN_AUTOMATION=1 TF_INPUT=0 "$TF" destroy -no-color -auto-approve \
  -var "endpoint=$ENDPOINT" >destroy.out 2>&1) || { cat "$WORK/destroy.out" >&2; fail "destroy failed"; }

# --------------------------------------------------------------- Exoscale ---
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/exoscale/fake-credentials.env"
set +a
export EXOSCALE_API_ENDPOINT="$ENDPOINT/v2"
guard_no_real_profile EXOSCALE_API_ENDPOINT exo

echo "- exo tells a refusal from an absence"
arm "$RULES_DIR/refusals.json"
exo_refused="$(exo -O json compute instance list </dev/null 2>&1 || true)"
# Measured on exo 1.95.1: the CLI surfaces the HTTP status and discards the
# body's message for a 4xx ("ListInstances: http response: Forbidden"). So what
# a client of this provider branches on is the status, and the assertion is
# about what the client actually shows rather than about the text we wrote.
printf '%s' "$exo_refused" | grep -qi "forbidden" \
  || fail "exo did not classify the injected 403 as a refusal: $exo_refused"
disarm
exo_absent="$(exo -O json compute instance show 11111111-1111-1111-1111-111111111111 </dev/null 2>&1 || true)"
printf '%s' "$exo_absent" | grep -qi "not found" \
  || fail "a real absence did not read as missing to exo: $exo_absent"
if printf '%s' "$exo_absent" | grep -qi "forbidden"; then
  fail "a missing instance reads as a refusal: the two classifications are the same to this client"
fi
ok "exo: injected 403 is 'Forbidden', a real absence is 'not found'"

echo "- exo survives 503, 503, 200"
arm "$RULES_DIR/transients.json"
exo -O json compute instance list </dev/null >/dev/null 2>&1 \
  || fail "exo gave up on two 503s; the rule fired $(hits exoscale/v2.list-instances) times"
[ "$(hits exoscale/v2.list-instances)" = "2" ] \
  || fail "the 503 rule fired $(hits exoscale/v2.list-instances) times, want exactly 2"
ok "exo retried through two 503s and completed"

# Three dialects, one status, and no two bodies alike. A 503 that reached all
# three clients identically would be a failure of this tool rather than of their
# clouds, which is the one thing a client never sees from its cloud.
echo "- one status, three dialects"
arm "$RULES_DIR/dialects.json"
scw_body="$(curl -s "$ENDPOINT/instance/v1/zones/fr-par-1/servers")"
osc_body="$(curl -s -X POST "$ENDPOINT/api/v1/ReadNets" -d '{}')"
exo_body="$(curl -s "$ENDPOINT/v2/instance")"
printf '%s' "$scw_body" | jq -e 'has("type")' >/dev/null \
  || fail "the Scaleway 503 carries no type: scw.ResponseError requires one. $scw_body"
printf '%s' "$osc_body" | jq -e 'has("Errors") and has("ResponseContext")' >/dev/null \
  || fail "the Outscale 503 is not ErrorResponse: $osc_body"
printf '%s' "$exo_body" | jq -e 'has("message") and (has("type") | not)' >/dev/null \
  || fail "the Exoscale 503 is not its bare envelope: $exo_body"
if [ "$scw_body" = "$osc_body" ] || [ "$osc_body" = "$exo_body" ] || [ "$scw_body" = "$exo_body" ]; then
  fail "two packs render an identical 503: one shape wearing three names"
fi
ok "three packs, three bodies, each the one its own SDK decodes"

# ------------------------------------------------- the evidence separation ---
#
# The bound both issues state: an operation whose only negative evidence comes
# from a fault somebody injected has not been proven, it has been staged. These
# two checks are what make that a property of the running emulator rather than a
# claim in a design note.
echo "- an injected refusal earns no negative evidence"
arm "$RULES_DIR/refusals.json"
span="$(curl -sf -X POST "$ENDPOINT/_feint/assert" -d '{"proves":"negative"}' | jq -r '.id')"
[ -n "$span" ] || fail "the emulator opened no span"
curl -s -o /dev/null "$ENDPOINT/instance/v1/zones/fr-par-1/servers"
closing="$(curl -s -w '\n%{http_code}' -X POST "$ENDPOINT/_feint/assert/$span")"
[ "${closing##*$'\n'}" = "409" ] \
  || fail "a span closed on an injected 403 (HTTP ${closing##*$'\n'}): a fault would be able to raise the very axis it exists to expose"
printf '%s' "${closing%$'\n'*}" | grep -qi "injected" \
  || fail "the refusal does not say the 4xx was injected: ${closing%$'\n'*}"
ok "the emulator refuses to close a negative span on a staged refusal"

echo "- an operation answered only by faults reads as un-driven"
# A target no other leg of this suite touches, so "only ever answered injected
# faults" is true of it and of nothing else. Held on an operation the run drove
# for real, this check would pass while measuring the wrong thing.
staged="exoscale/v2.list-anti-affinity-groups"
arm "$RULES_DIR/staged.json"
for _ in 1 2 3; do curl -s -o /dev/null "$ENDPOINT/v2/anti-affinity-group"; done
report="$(curl -sf "$ENDPOINT/_feint/conformance")"
printf '%s' "$report" | jq -e --arg op "$staged" '.injected[$op] == 3' >/dev/null \
  || fail "the injected answers were not counted: nothing would tell an operator a fault fired"
printf '%s' "$report" | jq -e --arg op "$staged" '.calls[$op] // 0 == 0' >/dev/null \
  || fail "an operation answered only by injected faults counts client calls"
printf '%s' "$report" | jq -e --arg op "$staged" '.evidence[$op].driven == false' >/dev/null \
  || fail "an operation answered only by injected faults reads as driven by a real client"
printf '%s' "$report" | jq -e --arg op "$staged" '.evidence[$op].negative == false' >/dev/null \
  || fail "an operation answered only by injected faults earned the negative axis"
printf '%s' "$report" | jq -e --arg op "$staged" 'any(.untouched[]; . == $op)' >/dev/null \
  || fail "an operation answered only by injected faults left the backlog: it looks exercised and is not"
ok "injected answers are counted, and count for nothing else"

# ------------------------------------------------------- below HTTP (#851) ---
# A status injection is a response: the client parses it and branches on it.
# These two are the absence of a response, which is the half of a client's
# resilience a status can never exercise. Held against `scw` rather than curl
# because the question is what an OFFICIAL client does when the socket dies
# under it, and a hand-written request would only measure curl.
echo "- scw meets a dropped connection, and does not read it as a provider error"
arm "$RULES_DIR/transport-close.json"
dropped="$(scw instance server list zone=fr-par-1 </dev/null 2>&1 || true)"
# Whatever the wording, it must not be a decoded provider error: a dropped
# connection carries no body, so a client reporting one would be inventing it.
if printf '%s' "$dropped" | grep -qiE "insufficient permissions|too many requests|cannot find resource"; then
  fail "scw decoded a provider error out of a dropped connection: $dropped"
fi
printf '%s' "$dropped" | grep -qiE "EOF|reset|connection|refused" \
  || fail "scw did not report a transport failure: $dropped"
# And the emulator says which failure it staged, rather than leaving a reader
# with `status 0` to interpret.
receipt="$(curl -sf "$ENDPOINT/_feint/faults")"
printf '%s' "$receipt" | jq -e '.fired | map(select(.transport == "close")) | length >= 1' >/dev/null \
  || fail "the receipt does not name the dropped connection: $receipt"
disarm
ok "a dropped connection reaches the client as one, and the receipt names it"

echo "- scw meets a connection that never answers, and times out on its own"
arm "$RULES_DIR/transport-hang.json"
started="$(date +%s)"
hung="$(SCW_DEFAULT_TIMEOUT=5 timeout 40s scw instance server list zone=fr-par-1 </dev/null 2>&1 || true)"
elapsed="$(( $(date +%s) - started ))"
[ "$elapsed" -ge 1 ] || fail "the call returned in ${elapsed}s, so nothing hung: $hung"
if printf '%s' "$hung" | grep -qiE "insufficient permissions|too many requests"; then
  fail "a hang was decoded as a provider error: $hung"
fi
receipt="$(curl -sf "$ENDPOINT/_feint/faults")"
printf '%s' "$receipt" | jq -e '.fired | map(select(.transport == "hang")) | length >= 1' >/dev/null \
  || fail "the receipt does not name the held connection: $receipt"
disarm
ok "a connection held open reaches the client as a timeout, and the receipt names it"

# -------------------------------------------- the receipt a client earns (#851) ---
# #851 asks that a client which does NOT retry produce a clear failure receipt.
# curl is that client, and it is the right one: every official client here
# retries something, so a suite built only on them could never show the case
# where the first failure is the last word.
echo "- a client that does not retry leaves a receipt naming what hit it"
arm "$RULES_DIR/refusals.json"
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/instance/v1/zones/fr-par-1/servers")"
[ "$code" = "403" ] || fail "the non-retrying client got $code, not the injected 403"
line="$("$FEINT_BIN" fault list --addr "$ADDR")"
printf '%s' "$line" | grep -q "instance/v1/API.ListServers call 1 answered 403" \
  || fail "the receipt does not say which call was answered with what: $line"
disarm
ok "the receipt names the operation, the rank and the status"

# ------------------------------------------- the campaign feint run arms (#851) ---
# The product integration #851 names: a campaign armed for the child, and the
# receipt printed with the run's own. Driven with a real client as the child,
# because a campaign that only works when the child is curl proves nothing
# about the thing users will do with it.
echo "- feint run arms a committed campaign for its child and reports what fired"
RECEIPT="$WORK/run-receipt.json"
run_out="$("$FEINT_BIN" run --endpoint "$ENDPOINT" --fault "$RULES_DIR/campaign.json" \
  --json "$RECEIPT" -- scw instance server list zone=fr-par-1 2>&1 </dev/null || true)"
[ -s "$RECEIPT" ] || fail "feint run wrote no receipt: $run_out"
jq -e '.faults_armed == true' "$RECEIPT" >/dev/null \
  || fail "the run receipt does not say a campaign was armed: $(cat "$RECEIPT")"
jq -e '.faults_read == true' "$RECEIPT" >/dev/null \
  || fail "the run receipt could not read the campaign back: $(cat "$RECEIPT")"
jq -e '.faults | map(select(.operation == "instance/v1/API.ListServers" and .status == 503)) | length == 1' \
  "$RECEIPT" >/dev/null \
  || fail "the run receipt does not name the 503 the campaign armed: $(cat "$RECEIPT")"
# The prose half, which is what a person reads. A receipt only a jq filter can
# find is a receipt nobody reads at the moment the run surprised them.
printf '%s' "$run_out" | grep -q "faults" \
  || fail "the run printed no fault line in its prose receipt: $run_out"
# NO disarm here, deliberately. This line used to call it, and that was the
# suite covering for the product: a security review found that a campaign armed
# on an attached lab outlived the run, and the next run against that lab met
# injected answers while publishing `faults_armed: false`. The run clears what
# it armed now, and this asserts it rather than doing it.
left="$(curl -sf "$ENDPOINT/_feint/faults" | jq -r '.faults | length')"
[ "$left" = "0" ] || fail "the run left $left rule(s) armed on a lab it does not own: the next client \
to call those operations meets a campaign nobody armed for it"
ok "the campaign was armed for the child, reported, and cleared from the lab"

echo "- a second run on the same lab meets nothing, and its receipt says so"
RECEIPT2="$WORK/run-second.json"
second_out="$("$FEINT_BIN" run --endpoint "$ENDPOINT" --json "$RECEIPT2" \
  -- scw instance server list zone=fr-par-1 2>&1 </dev/null || true)"
[ -s "$RECEIPT2" ] || fail "the second run wrote no receipt: $second_out"
jq -e '.faults_armed == false and .faults_campaign == false' "$RECEIPT2" >/dev/null \
  || fail "the second run reports a campaign: $(cat "$RECEIPT2")"
jq -e '.faults_read == true' "$RECEIPT2" >/dev/null \
  || fail "the second run could not read the lab's fault state, so a false faults_armed claims \
something it did not measure: $(cat "$RECEIPT2")"
jq -e '(.faults // []) | length == 0' "$RECEIPT2" >/dev/null \
  || fail "the second run met injected answers: $(cat "$RECEIPT2")"
ok "no campaign survives the run that armed it"

echo "- a receipt never denies a campaign the lab is carrying"
# The other direction, and the one the defect lived in: rules armed on the lab
# by somebody else, and a run that never asked for any. The receipt must report
# what the lab HAS, not what this invocation asked for.
arm "$RULES_DIR/refusals.json"
RECEIPT3="$WORK/run-foreign.json"
"$FEINT_BIN" run --endpoint "$ENDPOINT" --json "$RECEIPT3" \
  -- scw instance server list zone=fr-par-1 >/dev/null 2>&1 </dev/null || true
[ -s "$RECEIPT3" ] || fail "the run wrote no receipt"
jq -e '.faults_campaign == false' "$RECEIPT3" >/dev/null \
  || fail "a run with no --fault claims it armed one: $(cat "$RECEIPT3")"
jq -e '.faults_armed == true' "$RECEIPT3" >/dev/null \
  || fail "the run denies the campaign its own child met: $(cat "$RECEIPT3")"
# And it did not clear what it never armed.
[ "$(curl -sf "$ENDPOINT/_feint/faults" | jq -r '.faults | length')" != "0" ] \
  || fail "the run cleared rules it did not arm"
disarm
ok "the receipt reports the lab's campaign, and leaves it alone"

echo "- a run with no campaign reports no faults, and says so by saying nothing"
RECEIPT_CLEAN="$WORK/run-clean.json"
"$FEINT_BIN" run --endpoint "$ENDPOINT" --json "$RECEIPT_CLEAN" \
  -- scw instance server list zone=fr-par-1 >/dev/null 2>&1 </dev/null || true
[ -s "$RECEIPT_CLEAN" ] || fail "the clean run wrote no receipt"
jq -e '.faults_armed == false' "$RECEIPT_CLEAN" >/dev/null \
  || fail "a run with no --fault reports a campaign: $(cat "$RECEIPT_CLEAN")"
ok "no campaign, no claim"

echo "- clearing puts the emulator back where it started"
disarm
[ "$(curl -sf "$ENDPOINT/_feint/faults" | jq -r '.faults | length')" = "0" ] || fail "rules survived the clear"
scw instance server list zone=fr-par-1 </dev/null >/dev/null 2>&1 || fail "the emulator still refuses after the clear"
ok "no rule armed, every operation answering again"

echo "conformance: fault injection passed"
