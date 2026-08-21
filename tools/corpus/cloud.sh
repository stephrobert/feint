#!/usr/bin/env bash
# Replay one committed corpus at the real Scaleway account, under the account
# rules that are not negotiable (#352, #354, #359).
#
# usage: tools/corpus/cloud.sh <feint-binary> <corpus-file> [--dry-run]
#   FEINT_SCW_PROFILE   the scw profile to use. REQUIRED, and named on every
#                       single command below: several profiles on a maintainer's
#                       station belong to third-party organisations and some sit
#                       in a sovereign region, so "whichever one is default"
#                       is not an answer anybody may give on their behalf.
#
# One file, two callers — this script and .github/workflows/corpus-cloud.yml —
# for the reason tools/drift/gate.sh states: the same procedure written twice
# drifts, and the copy that drifts is the one that touches the account.
#
# WHAT THIS DOES TO THE ACCOUNT, IN ORDER
#
#   1. an inventory, before anything is created. You cannot say "the account is
#      as I found it" without the two lists to compare.
#   2. a trap, armed before the first call, that sweeps anything named
#      feint-corpus-* whatever happens next — including a Ctrl-C.
#   3. the replay itself, which creates only what internal/cli/corpus_cloud.go's
#      free-to-create list allows, refuses to touch an object it did not create,
#      and destroys its own ledger with each destruction proved by a read.
#   4. a second inventory, compared with the first. A difference fails the run.
#
# The secret key is read by `scw` from its own config and handed to the child
# through the environment. It never reaches argv (world-readable in `ps`), never
# a log line, never a file.
set -euo pipefail

FEINT="${1:?usage: cloud.sh <feint-binary> <corpus-file> [--dry-run]}"
CORPUS="${2:?usage: cloud.sh <feint-binary> <corpus-file> [--dry-run]}"
DRY="${3:-}"
PROFILE="${FEINT_SCW_PROFILE:?name the scw profile explicitly: FEINT_SCW_PROFILE=<name>; there is no default anybody may choose for the account holder}"
ENDPOINT="${FEINT_SCW_ENDPOINT:-https://api.scaleway.com}"

command -v scw >/dev/null || { echo "cloud.sh: scw is not installed" >&2; exit 1; }
[ -x "$FEINT" ] || { echo "cloud.sh: $FEINT is not an executable feint binary" >&2; exit 1; }
[ -f "$CORPUS" ] || { echo "cloud.sh: $CORPUS is not a committed corpus" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'sweep; rm -rf "$WORK"' EXIT

# The families this replay can possibly touch, plus the billable ones it must
# never have touched. Both halves: an inventory that only counted what the run
# creates could not notice a server appearing.
FAMILIES=(
  "vpc vpc"
  "vpc private-network"
  "iam ssh-key"
  "instance server"
  "instance ip"
  "instance volume"
  "instance snapshot"
  "lb lb"
)

inventory() {
  local label="$1" kind
  for kind in "${FAMILIES[@]}"; do
    # shellcheck disable=SC2086 # the family is two words on purpose
    scw --profile "$PROFILE" $kind list -o json \
      | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d) if isinstance(d,list) else d)' \
      > "$WORK/$label-${kind// /-}.count"
  done
}

# sweep is the second belt. The replay destroys its own ledger; this catches the
# run that died before it could — and it deletes by name, never by "everything
# that looks recent", because a sweep that guesses is a sweep that deletes
# somebody's work.
# shellcheck disable=SC2329 # invoked by the EXIT trap armed above
sweep() {
  local kind id name left=0
  for kind in "vpc private-network" "vpc vpc" "iam ssh-key"; do
    # shellcheck disable=SC2086
    while read -r id name; do
      [ -n "$id" ] || continue
      case "$name" in
        feint-corpus-*) ;;
        *) continue ;;
      esac
      echo "cloud.sh: sweeping $kind $name" >&2
      # shellcheck disable=SC2086
      scw --profile "$PROFILE" $kind delete "$id" >/dev/null 2>&1 || true
      # shellcheck disable=SC2086
      if scw --profile "$PROFILE" $kind get "$id" >/dev/null 2>&1; then
        echo "cloud.sh: NOT DESTROYED: $kind $name ($id)" >&2
        left=1
      fi
    done < <(scw --profile "$PROFILE" $kind list -o json 2>/dev/null \
      | python3 -c 'import json,sys
try:
    rows = json.load(sys.stdin)
except Exception:
    rows = []
for r in rows if isinstance(rows, list) else []:
    print(r.get("id", ""), r.get("name", ""))')
  done
  [ "$left" = 0 ] || echo "cloud.sh: the sweep left something behind; delete it by hand" >&2
}

echo "== inventory before"
inventory before
for f in "$WORK"/before-*.count; do printf '  %-28s %s\n' "$(basename "$f" .count | sed 's/^before-//')" "$(cat "$f")"; done

PROJECT="$(scw --profile "$PROFILE" config get default-project-id)"
ORGANISATION="$(scw --profile "$PROFILE" config get default-organization-id)"
SCW_SECRET_KEY="$(scw --profile "$PROFILE" config get secret-key)"
export SCW_SECRET_KEY

echo
echo "== replaying $CORPUS at $ENDPOINT"
status=0
"$FEINT" corpus --against-cloud \
  --file "$CORPUS" \
  --endpoint "$ENDPOINT" \
  --credential X-Auth-Token=SCW_SECRET_KEY \
  --bind "project_id=$PROJECT" \
  --bind "organization_id=$ORGANISATION" \
  ${DRY:+"$DRY"} || status=$?

echo
echo "== inventory after"
inventory after
drifted=0
for kind in "${FAMILIES[@]}"; do
  key="${kind// /-}"
  before="$(cat "$WORK/before-$key.count")"
  after="$(cat "$WORK/after-$key.count")"
  printf '  %-28s %s -> %s\n' "$key" "$before" "$after"
  [ "$before" = "$after" ] || drifted=1
done

if [ "$drifted" != 0 ]; then
  echo "cloud.sh: the account is NOT as it was found; the two inventories above disagree" >&2
  exit 1
fi
echo "the account is as it was found"
exit "$status"
