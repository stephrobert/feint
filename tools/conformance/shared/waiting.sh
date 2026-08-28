#!/usr/bin/env bash
# Waiting for a condition, instead of waiting for a duration (#459).
#
# WHY THIS IS NOT A TIDY-UP
#
# `FEINT_VM=incus-ovn mise run conformance` was measured at 1331 s on
# 2026-08-27, and the four suites that only run with a machine runtime — the
# three `network.sh` and `outscale/balancer.sh` — are 675 s of it, half the run.
# Traced command by command (`PS4` carrying `EPOCHREALTIME`, which turns
# `bash -x` into a profiler whose resolution is one shell command), those four
# spent **160 s inside an explicit `sleep`**:
#
#     scaleway/network.sh   163 s,  30 s asleep over 10 calls   18%
#     outscale/network.sh   214 s,  28 s asleep over 13 calls   13%
#     exoscale/network.sh   126 s,  28 s asleep over  8 calls   22%
#     outscale/balancer.sh  168 s,  74 s asleep over  7 calls   44%
#
# Sixty of the balancer's seventy-four ARE the measurement of #315 and stay. The
# other 100 s were spent after the thing being waited for had already happened.
#
# Same four suites, same station, same emulator lifecycle, after this file:
#
#     scaleway/network.sh   139 s,   5.5 s asleep    (was 163 s,  30 s)
#     outscale/network.sh   192 s,   6.0 s asleep    (was 214 s,  28 s)
#     exoscale/network.sh    98 s,   0.0 s asleep    (was 126 s,  28 s)
#     outscale/balancer.sh  158 s,  64.0 s asleep    (was 168 s,  74 s — 60 of
#                                                     which are #315's window)
#
# 44 fixed `sleep` lines became 8. Of the 8: five stand before a verdict drawn
# from an absence, two are trap handlers with no verdict after them, and one is
# #315's minute. Every one of them says so on the line above it, and
# TestEveryFixedSleepInARuntimeSuiteSaysWhatItWaitsFor fails if one stops.
#
# A fixed `sleep N` before an assertion states two things at once and is wrong
# about both: that N seconds are always enough (so the suite goes red on a
# slower host, which teaches "re-run it") and that N seconds are always needed
# (so every run pays the worst case). Polling the condition itself is right on
# both counts, and it is STRICTLY STRONGER than the sleep it replaces: the
# assertion that follows is untouched, so a condition that never becomes true
# still fails the suite — later than the sleep would have, and having actually
# looked.
#
# THE RULE THAT DECIDES WHERE THESE MAY BE USED
#
# `wait_until` goes before a verdict drawn from a POSITIVE observation: the
# machine answers, the port opens, the pool holds three members. It must NEVER
# go before a verdict drawn from a NEGATIVE one — "unreachable", "refused", "no
# longer answers". There the fixed wait is the only thing separating "the rule
# was applied" from "we looked too early", and a poll that stops at the first
# negative observation converts a race into a pass. That is exactly the shape
# of #559, in the one suite whose whole subject is the dataplane, and it is
# worse than the slowness this file exists to remove.
#
# The single exception is stated rather than assumed, and it has its own name:
# an object the runtime has DELETED does not come back, so polling until it is
# gone reaches the same verdict as sleeping, only sooner. `wait_gone` is that
# case; a caller reaching for `wait_until` to prove an absence has picked the
# wrong one.
#
# tools/conformance/waiting_test.go holds both halves — that a condition which
# never comes true fails, and that one which does is not waited out — and
# tools/falsify/specs/waiting-is-not-sleeping.json replays them with the
# timeout's refusal neutralised.

# WAIT_POLL is how often the condition is asked. A quarter second is far below
# anything these suites wait for and far above the cost of asking: the dearest
# condition here is one `incus exec`, tens of milliseconds.
WAIT_POLL="${WAIT_POLL:-0.25}"

# --- THE INSTRUMENT (#587) ---------------------------------------------------
#
# A budget nobody measured is a number, and every budget in these suites is one
# until something records what the wait actually cost. #587 is what that costs:
# `wait_until 24` was 12 iterations of `sleep 2` converted by hand, it fails on
# the maintainer's station and passes in CI, and no artefact anywhere said how
# long the wait it replaced had ever taken.
#
# WAIT_TRACE names a file; every wait appends one tab-separated row to it —
# suite, kind, verdict, the budget the CALLER asked for, the seconds it really
# took, and the condition. WAIT_SCALE multiplies every budget without editing a
# call site, so "is it slow or is it broken" is asked in one run.
#
# Both are off unless set, so a suite run without them behaves exactly as
# before. TestTheWaitsRecordWhatTheyCostWhenAskedTo holds that, and holds that
# the row carries the budget as asked rather than as scaled — a trace that
# reported the scaled number would make every scaled run look like a suite whose
# budgets had already been raised.
WAIT_TRACE="${WAIT_TRACE:-}"
WAIT_SCALE="${WAIT_SCALE:-1}"

_wait_now() { date +%s%N; }
# The suite's name is its directory and its file: three of the four runtime
# suites are called `network.sh`, so `basename` alone collapsed scaleway,
# outscale and exoscale into one label and a distribution read off it was the
# sum of three different populations.
_wait_suite() {
  local file=${0##*/} path=${0%/*}
  if [ "$path" = "$0" ]; then
    printf '%s' "$file"
  else
    printf '%s/%s' "${path##*/}" "$file"
  fi
}
_wait_record() { # kind verdict budget started command...
  # `${WAIT_TRACE:-}`, not `$WAIT_TRACE`: the suites run under `set -u`, and a
  # caller that unsets the variable after sourcing this file would abort the
  # whole suite on an instrument nobody asked for. Absent and empty both mean
  # "record nothing".
  [ -n "${WAIT_TRACE:-}" ] || return 0
  local kind=$1 verdict=$2 budget=$3 started=$4
  shift 4
  local now elapsed
  now="$(_wait_now)"
  elapsed=$(((now - started) / 1000000))
  printf '%s\t%s\t%s\t%s\t%s.%03d\t%s\n' \
    "$(_wait_suite)" "$kind" "$verdict" "$budget" \
    "$((elapsed / 1000))" "$((elapsed % 1000))" "$*" >>"$WAIT_TRACE"
}
# -----------------------------------------------------------------------------

# wait_until <seconds> <command...> — poll until the command succeeds.
#
# Answers 0 as soon as it does, and 1 if the budget runs out. The caller keeps
# its own assertion: this function decides how long to look, never what the
# verdict is.
#
# The budget is rounded UP by one second, and that is not a rounding choice but
# the same rule as everything above: `SECONDS` ticks on the shell's integer
# boundary, so a shell started at X.99 sees it reach 1 a hundredth of a second
# later. Rounding down would let a wait give up early, which is the flake this
# file exists to remove. Measured: without the `+ 1`, `wait_until 1 false`
# returned in 0.25 s.
wait_until() { # seconds command...
  local budget="$1"
  shift
  local asked="$budget"
  budget=$((budget * WAIT_SCALE))
  local started
  started="$(_wait_now)"
  local deadline=$((SECONDS + budget + 1))
  while :; do
    if "$@"; then
      _wait_record until held "$asked" "$started" "$@"
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      _wait_record until EXPIRED "$asked" "$started" "$@"
      return 1 # the budget ran out and the condition never held
    fi
    sleep "$WAIT_POLL"
  done
}

# wait_gone <seconds> <command...> — poll until the command FAILS.
#
# The disappearance half, and the only negative this file offers: see the rule
# above for why there is no general `wait_while`. The command must be a
# question about an object's existence — "does the host still list this
# network" — so that its first failure is the object being gone and not the
# observation being early.
wait_gone() { # seconds command...
  local budget="$1"
  shift
  local asked="$budget"
  budget=$((budget * WAIT_SCALE))
  local started
  started="$(_wait_now)"
  local deadline=$((SECONDS + budget + 1))
  while :; do
    if ! "$@"; then
      _wait_record gone held "$asked" "$started" "$@"
      return 0
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      _wait_record gone EXPIRED "$asked" "$started" "$@"
      return 1 # the budget ran out and the object is still there
    fi
    sleep "$WAIT_POLL"
  done
}
