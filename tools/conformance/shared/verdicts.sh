#!/usr/bin/env bash
# Shared conformance verdicts: the four ways these suites measured less than they
# claimed (#219).
#
# The suites are what this project's claim rests on, so a weak control here is
# the most expensive defect in the repository — worse than a bug, because it
# reports green. Each function below replaces a form that passed where it should
# have failed, and tools/conformance/shared/demo-219.sh shows the old and the new
# side by side on a synthetic case.
#
# Sourced after shared/addresses.sh, whose addresses_carried it uses. Every
# function expects `ok`, `fail` and `skip` to be defined by the caller.

# machine_carries answers whether *this* machine carries *exactly* this address.
#
# It replaces `incus list -f csv -c n4 | grep -q "$ip"`, which was wrong twice
# over: it searched every instance on the host, so a leftover machine from
# another run satisfied it, and it matched a substring, so 10.1.1.4 was found
# inside 10.1.1.44. A verdict that another run can satisfy is not a verdict.
machine_carries() { # machine address
  local address
  for address in $(addresses_carried "$1"); do
    [ "$address" = "$2" ] && return 0
  done
  return 1
}

# assert_listening is the positive control every negative verdict needs.
#
# The isolation checks conclude "unreachable" from a connection that did not
# open. A machine that never booted, or whose listener never started, produces
# exactly that — so a broken run and a correctly isolated one were the same
# observation, and the suite read the first as a pass. That verdict is the
# product's strongest claim, which makes it the worst one to leave unguarded.
#
# Asked on the target itself, because the point is precisely that nothing else
# can reach it: if the listener answers on its own loopback, then a refusal seen
# from elsewhere is isolation rather than absence.
#
# Split in two since #459: `is_listening` is the question, `assert_listening` is
# the verdict. The suites wait on the question — `wait_until 30 is_listening …`,
# see shared/waiting.sh — where they used to sleep a fixed two or three seconds
# hoping the listener had come up, and then draw the same verdict. The verdict
# is unchanged: a listener that never binds still fails, having been asked
# rather than assumed.
is_listening() { # machine port
  incus exec "$1" -- timeout 3 nc -z -w 2 127.0.0.1 "$2" >/dev/null 2>&1
}

assert_listening() { # machine port what
  local machine=$1 port=$2 what=$3
  if ! is_listening "$machine" "$port"; then
    fail "$what is not listening on port $port, so 'unreachable' would measure a dead machine rather than isolation"
  fi
}

# assert_listening_within is the form the suites call, and it exists so that
# they probe ONCE.
#
# The responder these suites start is `nc -l` inside a `while true` loop, so a
# probe CONSUMES it and the next one races the loop's re-bind. A fixed sleep
# followed by one assertion probed once; a poll followed by the same assertion
# would probe twice, which would trade the slowness for a race — the exact
# swap this whole change exists to avoid. One call, one probe sequence, one
# verdict, and the verdict is the same one assert_listening draws.
#
# Requires shared/waiting.sh, which every caller of this file sources beside it.
assert_listening_within() { # seconds machine port what
  local budget=$1 machine=$2 port=$3 what=$4
  if ! wait_until "$budget" is_listening "$machine" "$port"; then
    fail "$what is not listening on port $port after ${budget}s, so 'unreachable' would measure a dead machine rather than isolation"
  fi
}

# assert_answers_itself is assert_listening for a suite that measures reach with
# ping rather than with a port.
#
# Same argument: a machine whose stack is not up refuses a ping exactly as
# isolation does. Asked of the machine about its own address, which proves the
# address is live on it without needing anything else to reach it.
#
# Split into question and verdict for the reason is_listening is (#459).
answers_itself() { # machine address
  incus exec "$1" -- ping -c 1 -W 2 "$2" >/dev/null 2>&1
}

assert_answers_itself() { # machine address what
  if ! answers_itself "$1" "$2"; then
    fail "$3 does not answer on $2 from itself, so 'unreachable' would measure a dead stack rather than isolation"
  fi
}

# The same, with a budget, and the same reason: one call, one probe, one
# verdict. Requires shared/waiting.sh.
assert_answers_itself_within() { # seconds machine address what
  local budget=$1 machine=$2 address=$3 what=$4
  if ! wait_until "$budget" answers_itself "$machine" "$address"; then
    fail "$what does not answer on $address from itself after ${budget}s, so 'unreachable' would measure a dead stack rather than isolation"
  fi
}

# stop_here ends a suite that cannot continue, and says so as a result rather
# than as silence.
#
# `skip … ; exit 0` mid-suite left everything after it neither passed nor
# skipped, and a reader saw a clean exit. The count is the difference: a run that
# stops after two of eleven checks reports that it did.
# Called as `stop_here "$LINENO" "why"`. The count is read from the script rather
# than written into the call, because a number typed here goes stale the first
# time somebody adds a check below it — which would make this fix the very thing
# it is fixing.
stop_here() { # LINENO why
  skip "$2"
  local total remaining
  total="$(grep -c '^echo "- ' "$0" 2>/dev/null || echo 0)"
  remaining="$(awk -v start="$1" 'NR > start && /^echo "- /' "$0" 2>/dev/null | wc -l)"
  echo "  STOPPED: $remaining of this suite's $total check(s) were not run" >&2
  exit 0
}

# The cleanup list, kept in a file because the suites fill it from inside command
# substitutions.
#
# `boot()` ends with `echo "$id"` and every caller writes `id="$(boot …)"`, which
# is a subshell: `cleanup_ids+=("$id")` inside it updated a copy that died with
# the subshell, so the trap swept an empty list and every machine the suite
# created stayed on the operator's host. A file survives the subshell; an array
# does not.
cleanup_init() {
  CLEANUP_FILE="$(mktemp)"
  export CLEANUP_FILE
}

cleanup_add() { # id
  [ -n "${CLEANUP_FILE:-}" ] && printf '%s\n' "$1" >>"$CLEANUP_FILE"
}

cleanup_list() {
  [ -n "${CLEANUP_FILE:-}" ] && [ -f "$CLEANUP_FILE" ] && cat "$CLEANUP_FILE"
}

cleanup_done() {
  [ -n "${CLEANUP_FILE:-}" ] && rm -f "$CLEANUP_FILE"
}
