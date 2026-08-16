#!/usr/bin/env bash
# The demonstration #219 asks for: each of the four old forms passes where its
# replacement fails.
#
# A fix to a control is worth nothing until the control is shown to have been
# blind, and these four are shell rather than Go, so tools/falsify cannot reach
# them. This does the same job by hand: it builds the situation the old form
# could not see, runs both forms against it, and requires the old to pass and the
# new to refuse.
#
# It needs no runtime. Where a check reads Incus, the reading is stubbed with the
# output Incus would have produced, which is the point — what is under test is
# the judgement, not the runtime.
#
#   bash --noprofile --norc tools/conformance/shared/demo-219.sh
#
# Exit 0 when all four are demonstrated, 1 otherwise.
set -uo pipefail

failures=0
demo() { printf '\n== %s\n' "$1"; }
verdict() { # old new label
  if [ "$1" = "pass" ] && [ "$2" = "fail" ]; then
    printf '   ok   the old form passed and the new one refuses: %s\n' "$3"
  else
    printf '   FAIL the demonstration did not hold (old=%s new=%s): %s\n' "$1" "$2" "$3"
    failures=$((failures + 1))
  fi
}

# ---------------------------------------------------------------------------
demo "1. an address carried by somebody else's machine"

# What `incus list -f csv -c n4` would print with a leftover machine from another
# run, and none of our own carrying the address.
host_listing() {
  printf 'feint-osc-i-other,10.196.1.4 (eth0)\n'
  printf 'feint-osc-i-ours,10.196.1.44 (eth0)\n'
}
# The address our API published, for our machine.
want_ip=10.196.1.4

old_form() { host_listing | grep -q "$want_ip" && echo pass || echo fail; }

# The new form asks the named machine what it carries, and compares whole values.
addresses_carried() { # machine
  case "$1" in
  feint-osc-i-ours) echo "10.196.1.44" ;;
  feint-osc-i-other) echo "10.196.1.4" ;;
  esac
}
machine_carries() {
  local address
  for address in $(addresses_carried "$1"); do
    [ "$address" = "$2" ] && return 0
  done
  return 1
}
new_form() { machine_carries feint-osc-i-ours "$want_ip" && echo pass || echo fail; }

verdict "$(old_form)" "$(new_form)" \
  "another run's machine, and a substring match, both satisfied the old grep"

# ---------------------------------------------------------------------------
demo "2. isolation concluded from a machine that never booted"

# The far machine is dead: every connection to it is refused, whatever the
# network says.
connect_to_far() { return 1; }
listener_on_far() { return 1; } # nothing is listening, because nothing booted

old_isolation() { if connect_to_far; then echo fail; else echo pass; fi; }
new_isolation() {
  # The positive control first: if the target answers nothing at all, the
  # negative verdict measures a dead machine.
  if ! listener_on_far; then echo fail; return; fi
  if connect_to_far; then echo fail; else echo pass; fi
}

verdict "$(old_isolation)" "$(new_isolation)" \
  "a dead machine and a correctly isolated one were the same observation"

# ---------------------------------------------------------------------------
demo "3. a suite that stops and reports a clean exit"

suite_body() {
  printf 'echo "- one"\necho "- two"\necho "- three"\necho "- four"\n'
}
# The old form: skip, exit 0. A reader sees two results and a zero exit.
old_stop() {
  local out
  out="$(printf '  SKIP: cannot continue\n')"
  case "$out" in
  *STOPPED*) echo fail ;;
  *) echo pass ;;
  esac
}
# The new form names what was not run, so the run cannot be read as complete.
new_stop() {
  local total remaining out
  total="$(suite_body | grep -c '^echo "- ')"
  remaining="$(suite_body | awk 'NR > 2 && /^echo "- /' | wc -l)"
  out="$(printf '  SKIP: cannot continue\n  STOPPED: %s of this suite'"'"'s %s check(s) were not run\n' \
         "$remaining" "$total")"
  case "$out" in
  *STOPPED*) echo fail ;;
  *) echo pass ;;
  esac
}

verdict "$(old_stop)" "$(new_stop)" \
  "everything after the skip was neither passed nor skipped, and the exit was zero"

# ---------------------------------------------------------------------------
demo "4. a cleanup list that dies with its subshell"

old_swept() (
  cleanup_ids=()
  boot_old() { cleanup_ids+=("machine-$1"); echo "machine-$1"; }
  # The call every suite makes: command substitution, hence a subshell.
  id="$(boot_old 1)"
  [ -n "$id" ] || true
  # The trap would sweep this list.
  if [ "${#cleanup_ids[@]}" -eq 0 ]; then echo pass; else echo fail; fi
)

new_swept() (
  CLEANUP_FILE="$(mktemp)"
  cleanup_add() { printf '%s\n' "$1" >>"$CLEANUP_FILE"; }
  cleanup_list() { cat "$CLEANUP_FILE"; }
  boot_new() { cleanup_add "machine-$1"; echo "machine-$1"; }
  id="$(boot_new 1)"
  [ -n "$id" ] || true
  swept="$(cleanup_list | wc -l)"
  rm -f "$CLEANUP_FILE"
  if [ "$swept" -eq 0 ]; then echo pass; else echo fail; fi
)

verdict "$(old_swept)" "$(new_swept)" \
  "the trap ran on an empty list and every machine stayed on the host"

# ---------------------------------------------------------------------------
printf '\n'
if [ "$failures" -eq 0 ]; then
  echo "all four weaknesses demonstrated: the old form passes, the new one refuses"
  exit 0
fi
echo "$failures demonstration(s) did not hold" >&2
exit 1
