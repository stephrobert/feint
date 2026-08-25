#!/usr/bin/env bash
# Report what the run actually exercised, not just what is mounted.
#
# The coverage report answers "how much of the upstream API is implemented".
# It cannot answer "how much of that has ever been driven by a real client", and
# until this existed nobody could: 74 routes were mounted and the suites touched
# three of them. Microcks names the same pair — a conformance index for what the
# contract could cover, a conformance score for what the last run did.
#
# Printed, not enforced. A threshold here would be a number invented to be met;
# the useful output is the list of routes no client has ever proven, which is a
# backlog somebody can act on.
#
# Usage: tools/conformance/score.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

report="$(curl -sf "$ENDPOINT/_feint/conformance")" \
  || { echo "FAIL: the emulator did not answer /_feint/conformance" >&2; exit 1; }

# Before any number is printed: a run carrying a staged answer describes nothing.
#
# `mise run conformance` must stay green by default and must never count
# injected errors as served behaviour (#26). The rules are off unless somebody
# arms them, so the honest way to hold that is not a promise in a comment but
# this: the run's own report says how many answers the fault injector produced,
# per operation, and a score computed over a run that carries one would be
# describing a mixture of what the emulator serves and what a rule was told to
# say. tools/conformance/faults.sh drives the injector deliberately, on an
# emulator of its own, on its own port, precisely so this stays at zero here.
#
# TestTheScoreRefusesARunCarryingAnInjectedAnswer (tools/conformance) fails
# without it, and tools/falsify/specs/injected-is-not-evidence.json replays that.
injected="$(printf '%s' "$report" | jq -r '
  .injected // {} | to_entries[] | "  \(.key): \(.value) injected answer(s)"')"
if [ -n "$injected" ]; then
  echo "FAIL: this run answered with injected faults, so its numbers describe" >&2
  echo "      a mixture of what the emulator serves and what a rule was told to say:" >&2
  echo "$injected" >&2
  echo "      Clear them (curl -X DELETE \$ENDPOINT/_feint/faults) and run again." >&2
  echo "      Fault injection has its own suite and its own emulator:" >&2
  echo "      tools/conformance/faults.sh." >&2
  exit 1
fi

served="$(printf '%s' "$report" | jq -r '.served')"
exercised="$(printf '%s' "$report" | jq -r '.exercised')"
probed="$(printf '%s' "$report" | jq -r '.probed // 0')"
echo "conformance score: $exercised of $served routes proven by a real client"
# Never added to the line above. A probe proves the protocol — the route answers
# and its answer matches the provider's schema — and nothing about behaviour, so
# a probed route stays on the backlog until a client drives it. Merging the two
# is how a number goes up without anything being proven.
[ "$probed" = "0" ] || echo "protocol probe:    $probed more probed only (schema-valid, behaviour unproven)"

# A violation here means a response did not match the provider's own API
# description. It is a failure: the whole point of loading the contract is that
# nobody has to notice.
violations="$(printf '%s' "$report" | jq -r '.violations | to_entries[] | "  \(.key): \(.value | join("; "))"')"
if [ -n "$violations" ]; then
  echo "FAIL: responses that do not match the API description:" >&2
  echo "$violations" >&2
  exit 1
fi

# And the same treatment for the other half of the exchange: fields a real client
# sent that no handler read.
#
# The emulator had been recording them all along, and nothing looked. Terraform
# changed a server's commercial_type, the emulator answered 200, ignored the
# field, and the next plan asked for the same change forever — while
# /_feint/conformance listed `commercial_type` and `volumes` under
# unread_request_fields, run after run. An introspection nobody gates on is a
# confession nobody hears.
#
# A field here is not always a defect, so exemptions exist — and each one costs a
# line and a reason, the way Declined() does for operations.
#
# The only one so far: a client that echoes back a whole object it just read,
# where the API decides on the identifier alone. `exo` sends the entire
# instance-type and template objects it got from the catalogue; the emulator
# reads their id and ignores the rest, exactly as the real API does. Listing the
# sub-fields as unread would be reporting the client's verbosity as our defect.
ignored_fields='^(instance-type|template)\.'

unread="$(printf '%s' "$report" | jq -r --arg ignore "$ignored_fields" '
  .unread_request_fields
  | to_entries[]
  | {key: .key, value: [.value[] | select(test($ignore) | not)]}
  | select(.value | length > 0)
  | "  \(.key): \(.value | join(", "))"')"
if [ -n "$unread" ]; then
  echo "FAIL: fields a real client sent that no handler read:" >&2
  echo "$unread" >&2
  echo "       Either honour them, or say why they are ignored." >&2
  exit 1
fi

# The omission half of the same check (#88). A violation above is a field the
# emulator invented; an entry here is a field both upstream sources vouch for —
# declared by the provider's document, observed in a real cloud's recorded
# answer — that no answer of this run ever carried, though its container was
# served. The contract gate cannot see these (an absent field only violates a
# schema when it is required, and the providers declare almost none), and the
# offline shapes gate cannot either (its store is empty, so it never sees an
# element of a list). This run's populated objects are where the recording
# finally bites.
# It judges a whole run, and only a whole run says so.
#
# The verdict asks whether *no answer of this run* carried a declared field, so
# it is a statement about the run's entire population. CI splits the clients
# across a matrix, one emulator per leg, and a leg that never exercises a
# feature legitimately never serves the fields that feature produces: the
# terraform leg drives no octl, so ReadVms carries no UserData, ReadVolumes
# no SnapshotId, and ReadSecurityGroups no rule whose member is another group.
# Failing there blamed the emulator for the shape of the leg.
#
# So the gate is declared rather than assumed, the way a driver capability is:
# FEINT_FIELD_GATE=1 is set by `mise run conformance`, which drives every suite
# against one emulator, and by the workflow job that does the same. Everywhere
# else the findings still print, marked for what they are, and fail nothing.
# An undeclared whole run counts as partial.
omissions="$(printf '%s' "$report" | jq -r '
  .fields.missing | to_entries[] | "  \(.key): \(.value | join(", "))"')"
if [ -n "$omissions" ]; then
  if [ "${FEINT_FIELD_GATE:-0}" = "1" ]; then
    echo "FAIL: fields the real cloud returns and no answer of this run carried:" >&2
    echo "$omissions" >&2
    echo "       Either serve them, or decline them in the pack's DeclinedFields() with a reason." >&2
    exit 1
  fi
  echo "declared fields no answer of this partial run carried (not judged: this run"
  echo "drove some clients, not all; the gate runs where every suite does):"
  echo "$omissions"
fi

# What the gate subtracts stays visible: each excused field is a pack's
# decision, printed with its reason, never failed on.
excused="$(printf '%s' "$report" | jq -r '
  .fields.excused | to_entries[] | "  \(.key): \(.value | join("; "))"')"
if [ -n "$excused" ]; then
  echo "declared fields knowingly not served, each with its reason:"
  echo "$excused"
fi

# And a decision that outlived its subject fails: a decline for a field this
# very run served is arguing about an omission the emulator does not have.
stale="$(printf '%s' "$report" | jq -r '.fields.stale_declines[]? | "  \(.)"')"
if [ -n "$stale" ]; then
  echo "FAIL: field declines whose field the emulator now serves:" >&2
  echo "$stale" >&2
  echo "       Remove them, or the excused list rots into fiction." >&2
  exit 1
fi

checked="$(printf '%s' "$report" | jq -r '.contracts | join(", ")')"
[ -z "$checked" ] || echo "  responses checked against the API description of: $checked"

# Coverage, then the blind spot, both counted out loud. Unconfirmed fields are
# the ones only the document declares: no recording proves the real cloud
# serves them — and where a recording could arbitrate, four of five such
# fields turned out to be absent from the real answer too, which is why they
# are a list to record, not a failure. Each entry is one recording away from
# becoming either a finding or nothing.
compared="$(printf '%s' "$report" | jq -r '.fields.compared | length')"
[ "$compared" = "0" ] || echo "  field completeness: $compared operation(s) compared against what their document declares"
unconfirmed_fields="$(printf '%s' "$report" | jq -r '[.fields.unconfirmed[] | length] | add // 0')"
unconfirmed_ops="$(printf '%s' "$report" | jq -r '.fields.unconfirmed | length')"
[ "$unconfirmed_fields" = "0" ] || echo "  blind spot: $unconfirmed_fields declared field(s) on $unconfirmed_ops operation(s) that no recording arbitrates (fields.unconfirmed on /_feint/conformance)"

printf '%s' "$report" | jq -r '
  .untouched
  | group_by(split("/")[0])
  | map("  \(length) route(s) no client has driven: \(.[0] | split("/")[0])")
  | .[]'
