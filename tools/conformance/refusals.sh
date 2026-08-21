#!/usr/bin/env bash
# Conformance check: the refusals a real cloud actually answered, reissued here
# and graded on the `negative` evidence axis (#390).
#
# WHY THIS EXISTS, AND WHY IT IS NOT A SECOND MECHANISM. `negative` stood at 35
# of 370 because nothing in this repository had ever watched a real cloud say
# no. #26 built fault injection and deliberately refused to let it count: an
# injected answer leaves the observed path before the observer records it, stays
# `driven: false`, and a `negative` span whose only 4xx were injected is refused
# outright. That decision is what makes the axis worth reading, and it means the
# axis can only be raised by *observing* refusals.
#
# So this drives the ones a cloud really answered. `feint proxy` recorded them,
# `feint transcript --sanitise` made them committable, `feint replay` reissues
# them, `tools/conformance/prove.sh` brackets them. Nothing new: the corpus
# already existed as a comparison (`feint corpus --check`), and here it is a
# client.
#
# WHY IT CAN SHARE THIS RUN'S EMULATOR, WHICH `corpus --check` DOES NOT. A file
# whose every exchange is a 4xx mutates nothing, so replaying it beside the
# other suites cannot disturb them. That is a property of the file and not a
# promise about it, so it is READ off the file — and read twice, on purpose:
#
#   - the jq filter below only SELECTS which corpora to offer;
#   - `feint replay --refusals-only` is the guard, and it reads the whole file
#     before a single request goes out, so a mistake in the selection is a
#     refusal rather than a lifecycle corpus creating servers in the middle of
#     somebody else's suite. TestRefusalsOnlyRefusesARecordingThatWouldMutate
#     (internal/cli) plants the witness: one 200 among refusals, refused, and
#     the emulator unchanged.
#
# The selection has its own witness here: the run fails unless it both accepted
# at least one corpus and REJECTED at least one, because a filter that answers
# "refusals only" to everything looks exactly like this one working.
#
# What a green run means, precisely: for each operation marked, some real client
# sent that request to that provider's real cloud, the cloud refused it, and
# this emulator refused the reissued request too. An operation the emulator
# answers 2xx is not marked — the divergence is `corpus --check`'s to report,
# and the axis simply does not count it, which is the honest arithmetic.
#
# Usage: tools/conformance/refusals.sh [endpoint]   (default http://127.0.0.1:4599)
#        FEINT_BIN=/tmp/feint tools/conformance/refusals.sh <endpoint>
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
export ENDPOINT
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=/dev/null
. "$ROOT/tools/conformance/prove.sh"

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }
# FEINT_BIN for the same reason faults.sh reads it: CI builds once, to /tmp,
# and every suite is handed that binary rather than rebuilding per leg.
FEINT="${FEINT_BIN:-$ROOT/feint}"
[ -x "$FEINT" ] || { echo "FAIL: $FEINT is not built (set FEINT_BIN to point elsewhere)" >&2; exit 1; }

echo "conformance: recorded refusals against $ENDPOINT"

# refusals_only reports whether every exchange of a corpus is a 4xx. A file with
# no exchange at all is not "refusals only": it is empty, and answering yes
# would replay nothing and prove nothing.
refusals_only() {
  jq -sr 'if length == 0 then "no"
          elif all(.[]; .status >= 400 and .status < 500) then "yes"
          else "no" end' "$1"
}

accepted=0
rejected=0
marked=0
for corpus in "$ROOT"/corpus/*/*.jsonl; do
  [ -f "$corpus" ] || continue
  name="${corpus#"$ROOT"/corpus/}"
  if [ "$(refusals_only "$corpus")" != "yes" ]; then
    rejected=$((rejected + 1))
    continue
  fi
  accepted=$((accepted + 1))
  span="$(prove_begin negative)"
  # The replay's own verdict is not this suite's business: a divergence is
  # `feint corpus --check`'s to judge against corpus/accepted.json, and judging
  # it twice with two different lists is how the two answers drift apart. What
  # this suite needs is that the requests went out.
  #
  # So the exit CODE decides, not a substring of the output: rule 9 fixes 2 for
  # a divergence and 1 for "this tool failed", and reading the text instead
  # would swallow an unreadable file whose message happens to carry the word.
  out=""
  code=0
  out="$("$FEINT" replay "$corpus" --refusals-only --endpoint "$ENDPOINT" 2>&1)" || code=$?
  if [ "$code" != "0" ] && [ "$code" != "2" ]; then
    echo "FAIL: replaying $name exited $code: $out" >&2
    exit 1
  fi
  printf '  %-42s %s\n' "$name" "$(printf '%s' "$out" | grep -E '^[0-9]+ matched' || true)"
  # A span the emulator refuses to close fails the run, and that is the point:
  # it means this corpus of refusals provoked none here.
  prove_end "$span"
  marked=$((marked + 1))
done

if [ "$accepted" -eq 0 ]; then
  echo "FAIL: no committed corpus is refusals-only, so this suite measured nothing" >&2
  exit 1
fi
# The planted witness. Without it the filter above could be answering "yes" to
# every file — including the lifecycle corpora, which create machines — and this
# suite would read as passing while replaying creates into a shared emulator.
if [ "$rejected" -eq 0 ]; then
  echo "FAIL: every committed corpus was taken for refusals-only, which cannot be true" >&2
  echo "      while corpus/ holds lifecycle recordings: the filter is broken, not the corpus" >&2
  exit 1
fi

echo "conformance: $marked refusal corpus(es) reissued, $rejected corpus(es) left to corpus --check"
