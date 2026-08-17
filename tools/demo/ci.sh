#!/usr/bin/env bash
# The CI demo, as a script that can fail.
#
# It exists because the recording could not. `tools/demo/ci.tape` drives a
# terminal, and vhs types the next line whatever the previous one printed — so
# the first cut of docs/assets/ci.gif recorded, frame by frame, a `docker run`
# against `feint:v` (an unset variable), a `curl` to a port nothing listened on,
# and a `terraform apply` failing on a catalogue that was never served. It looked
# like a demonstration and it was a screenshot of four errors.
#
# The failure underneath was mine and it is worth naming: a demo was published
# without being watched. This script is the fix that is not a promise — the tape
# runs the same sequence, and this runs it where a non-zero exit is visible.
#
#   mise run demo:ci        records the GIF
#   tools/demo/ci.sh        proves the sequence works first
#
# Usage: tools/demo/ci.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NAME="feint-demo-check"
PORT=4599

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }

command -v docker >/dev/null 2>&1 || fail "docker is not installed: this demo shows the container path"
command -v terraform >/dev/null 2>&1 || fail "terraform is not installed"

# The tag the tape reads, computed the same way. It was the first thing to break:
# in a tape, `\\[` reaches the shell as two characters and the grep matches
# nothing, so the image reference became `feint:v` and every later line failed
# against a container that never started.
TAG="$(grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' "$ROOT/CHANGELOG.md" | tr -d '#[] ')"
[ -n "$TAG" ] || fail "no released version in CHANGELOG.md: the demo cannot name an image"

# The tape shows the version rather than a variable, because vhs types its line
# literally and `$TAG` on screen is a line nobody can copy. A written-out figure
# is exactly what this repository refuses to leave unchecked, so it is checked
# here: the recording cannot name a version that has moved.
shown="$(grep -oE 'ghcr\.io/stephrobert/feint:v[0-9]+\.[0-9]+\.[0-9]+' "$ROOT/tools/demo/ci.tape" | head -1)"
[ "$shown" = "ghcr.io/stephrobert/feint:v$TAG" ] \
  || fail "the tape shows '$shown' and the CHANGELOG says v$TAG: update tools/demo/ci.tape and re-record"
ok "released version $TAG, and the tape shows it"

WORK="$(mktemp -d)"
cleanup() {
  docker stop "$NAME" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

cp "$ROOT/tools/demo/main.tf" "$WORK/"
cd "$WORK"

echo "- the image runs"
docker run -d --rm --name "$NAME" -p "127.0.0.1:$PORT:4599" \
  "ghcr.io/stephrobert/feint:v$TAG" >/dev/null \
  || fail "docker run failed: the demo would record the error rather than the emulator"
for _ in $(seq 1 60); do
  curl -sf "http://127.0.0.1:$PORT/_feint/health" >/dev/null 2>&1 && break
  sleep 0.5
done
health="$(curl -sf "http://127.0.0.1:$PORT/_feint/health")" || fail "the emulator never answered"
printf '%s' "$health" | jq -e '.providers | length == 3' >/dev/null \
  || fail "the health answer does not carry three providers: $health"
ok "three packs, machines $(printf '%s' "$health" | jq -r .machines)"

echo "- the real Terraform provider applies against it"
export TF_IN_AUTOMATION=1 TF_INPUT=0
export SCW_ACCESS_KEY=SCWXXXXXXXXXXXXXXXXX
export SCW_SECRET_KEY=11111111-1111-1111-1111-111111111111
export SCW_DEFAULT_PROJECT_ID=11111111-1111-1111-1111-111111111111
export SCW_DEFAULT_ORGANIZATION_ID=11111111-1111-1111-1111-111111111111
export SCW_DEFAULT_ZONE=fr-par-1 SCW_DEFAULT_REGION=fr-par
export SCW_API_URL="http://127.0.0.1:$PORT" SCW_INSECURE=true

terraform init -input=false >/dev/null 2>&1 || fail "terraform init failed"
terraform apply -auto-approve -input=false -no-color >"$WORK/apply.log" 2>&1 \
  || { grep -E "Error" -A 4 "$WORK/apply.log" | head -20; fail "terraform apply failed: this is what the GIF would show"; }
grep -q "Apply complete" "$WORK/apply.log" || fail "no 'Apply complete' in the output the demo is built around"
ok "$(grep -m1 'Apply complete' "$WORK/apply.log")"

echo "- the emulator counts what it drove"
driven="$(curl -sf "http://127.0.0.1:$PORT/_feint/conformance" | jq -r '.calls | length')"
[ "${driven:-0}" -gt 0 ] || fail "the emulator reports $driven driven operations after an apply"
ok "$driven operations driven"

echo "- and nothing survives it"
terraform destroy -auto-approve -input=false -no-color >/dev/null 2>&1 || true
docker stop "$NAME" >/dev/null || fail "the container would not stop"
sleep 1
if curl -sf --max-time 2 "http://127.0.0.1:$PORT/_feint/health" >/dev/null 2>&1; then
  fail "something still answers on $PORT after the container stopped"
fi
ok "nothing answers on $PORT"

echo "demo: every command the tape records works"
