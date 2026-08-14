#!/usr/bin/env bash
# The assertion-level signal behind the behaviour and negative evidence axes.
#
# A suite brackets one assertion block between prove_begin and prove_end, and
# that is the whole channel: the suite never names an operation. The emulator
# resolves which operations the block proved from what it observed on its own —
# the store's create/read/delete events for a `behaviour` span, the 4xx a real
# client received for a `negative` span — and refuses to close a span whose
# claim its observation does not support. A refused close fails the suite, so
# the signal cannot say more than the traffic did.
#
# Why not a header on the requests themselves: none of the official clients
# (scw, terraform, oapi-cli, exo) lets a suite attach one, and this project only
# measures with real clients.
#
# Requires $ENDPOINT (the suites all set it before sourcing anything), curl and
# jq, which every suite here already depends on.
#
#   span="$(prove_begin behaviour)"
#   ... the real client walks create, read, act, destroy ...
#   prove_end "$span"
#
#   span="$(prove_begin negative)"
#   if scw <something the API must refuse>; then fail "was accepted"; fi
#   prove_end "$span"

# prove_begin opens a span claiming to prove one axis, and prints its id.
prove_begin() {
  local axis="$1" id
  id="$(curl -sf -X POST "$ENDPOINT/_feint/assert" -d "{\"proves\":\"$axis\"}" | jq -r '.id // empty')" || {
    echo "FAIL: the emulator refused to open a $axis span" >&2
    exit 1
  }
  if [ -z "$id" ]; then
    echo "FAIL: the emulator answered no span id for $axis" >&2
    exit 1
  fi
  printf '%s' "$id"
}

# prove_end closes a span. The emulator answers 409 when it observed nothing
# that supports the span's claim, and that verdict fails the suite: an
# assertion that says "I proved a lifecycle" or "I demanded a refusal" without
# the traffic to show for it is a suite defect, not a detail.
prove_end() {
  local id="$1" out code body
  out="$(curl -s -w '\n%{http_code}' -X POST "$ENDPOINT/_feint/assert/$id")"
  code="${out##*$'\n'}"
  body="${out%$'\n'*}"
  if [ "$code" != "200" ]; then
    echo "FAIL: the emulator did not observe what this span claims (HTTP $code): $body" >&2
    exit 1
  fi
}
