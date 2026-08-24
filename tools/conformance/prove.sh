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
#
# It also answers how many store touches it could not attribute to a request,
# and that number is printed rather than dropped. #398 is why: the axis used to
# lose attribution under a parallel client without saying so, and two identical
# runs then marked the same *count* of operations while disagreeing on six of
# them. A span that under-claims must say by how much on the suite's own output,
# where somebody reads it, rather than by moving a figure in an artefact.
prove_end() {
  local id="$1" out code body lost
  out="$(curl -s -w '\n%{http_code}' -X POST "$ENDPOINT/_feint/assert/$id")"
  code="${out##*$'\n'}"
  body="${out%$'\n'*}"
  if [ "$code" != "200" ]; then
    echo "FAIL: the emulator did not observe what this span claims (HTTP $code): $body" >&2
    exit 1
  fi
  lost="$(printf '%s' "$body" | jq -r '.unattributed // 0')"
  if [ "$lost" != "0" ]; then
    echo "  note: this span made $lost store touch(es) it could not attribute to a request," >&2
    echo "        so the behaviour axis is short by that much for this block (#398)" >&2
  fi
}

# refuse_client drives one command that must be refused BY THE EMULATOR, and
# reads the emulator's own verdict rather than the client's output (#428).
#
# It is here rather than in each suite for the reason the shared layer exists at
# all: three clients, one rule. `scw`, `exo` and `oapi-cli` all answer a refusal
# they made THEMSELVES and a refusal the API made with the same non-zero exit
# code and a similar JSON envelope, so a suite parsing that text cannot tell the
# two apart — and the difference is the whole measurement. `scw` validates enums
# against its own SDK and `exo` resolves a NAME|ID by listing first, so a case
# aimed at something that does not exist never reaches the emulator at all.
#
# Three outcomes, never two:
#
#   - the client succeeded          -> the API accepted what must be refused;
#   - the client failed, span closes -> the emulator answered a 4xx: it holds;
#   - the client failed, span 409s   -> nothing reached the emulator, so this
#                                       case proves nothing and says so by name.
#
# The third is the one that matters, and it is a guard rather than a comment:
# a case that quietly stops reaching the API reads exactly like this suite
# working, and it would keep passing for as long as nobody read the axis.
# TestARefusalTheClientMadeItselfFailsTheCase fails without it, and
# TestARefusalTheEmulatorMadePassesTheCase is the other half, without which a
# helper that refused everything would satisfy the first.
#
# The caller passes its own client word — `scw`, `exoc`, `osc` — because that is
# the only thing that varies.
refuse_client() { # label client args...
  local label="$1"; shift
  local span out rc=0 close code body
  span="$(prove_begin negative)"
  out="$("$@" 2>&1)" || rc=$?
  close="$(curl -s -w '\n%{http_code}' -X POST "$ENDPOINT/_feint/assert/$span")"
  code="${close##*$'\n'}"
  body="${close%$'\n'*}"
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: $label: the client was answered success where a refusal was demanded: $out" >&2
    exit 1
  fi
  case "$code" in
    200) ;;
    409)
      echo "FAIL: $label: the client refused this on its own and the emulator never saw it," >&2
      echo "      so the case measures nothing: $out" >&2
      exit 1
      ;;
    *)
      echo "FAIL: $label: the emulator answered HTTP $code closing the span: $body" >&2
      exit 1
      ;;
  esac
}
