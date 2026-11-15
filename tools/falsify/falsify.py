#!/usr/bin/env python3
"""Run a falsification: remove a guard in a copy outside the repository, and
require the test that names it to fail.

    mise run falsify -- tools/falsify/specs/<name>.json

Why this exists as a program rather than as a habit. `/falsify` was a procedure
followed by hand, and on 2026-08-15 the same mistake voided a verdict three
times in one day, across three unrelated issues:

    if ok && fe.EnforcesFirewall() {   ->  if ok {                 # fe orphaned
    if strings.TrimSpace(s) == "" {    ->  if false {              # s orphaned
    if err != nil || n == 0 {          ->  if n == 0 {             # err orphaned

Go refuses to compile an unused variable, so each mutation broke the build. A
mutation that does not build makes *every* test fail, which looks exactly like
the guard being proven — the failure mode `.claude/commands/falsify.md` already
warned about, met three times anyway. Warning was not enough; this refuses.

The rule it enforces, and it is the one that would have caught all three:

    A MUTATION MUST NOT DROP AN IDENTIFIER THE ORIGINAL EXPRESSION USED.

Neutralise a condition by making it never true while every name stays evaluated
(`x && false`, `x == "\\x00never"`, `(err != nil && false) || ...`), never by
deleting the term that mentions it.

The spec is JSON:

    {
      "package": "./internal/cli/",
      "mutations": [
        {
          "label": "the notice never fires",
          "file": "internal/cli/lifecycle.go",
          "find": "if err != nil || health.Resources == 0 {",
          "replace": "if err != nil || health.Resources >= 0 {",
          "test": "TestStopSaysWhatItIsAboutToDiscard",
          "package": "./internal/cli/"
        }
      ]
    }

Per-mutation `package` is optional and falls back to the top-level one.

`timeout` is optional too, on a mutation or on the spec, and the mutation's
wins. It is a Go duration ("20s", "1m30s") handed to `go test -timeout`, so it
bounds the whole test run of that mutation, every `repeat` included. Without
one, Go's own default applies (10 minutes). Either way, a run that exits
non-zero with `panic: test timed out after …`, or with go's own `*** Test
killed … ran too long` when the test binary's alarm never fired, is HUNG
(void), never "the test bit": a test that hangs until it is killed has not
shown that it catches the mutation (#965).
"""

import contextlib
import inspect
import io
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

EXCLUDE = {".git", ".upstream", ".terraform", "notes", ".claude"}

# Go keywords and predeclared names are not identifiers a mutation "uses": they
# appear on both sides of every rewrite and would make the check vacuous.
RESERVED = {
    "break",
    "case",
    "chan",
    "const",
    "continue",
    "default",
    "defer",
    "else",
    "fallthrough",
    "for",
    "func",
    "go",
    "goto",
    "if",
    "import",
    "interface",
    "map",
    "package",
    "range",
    "return",
    "select",
    "struct",
    "switch",
    "type",
    "var",
    "true",
    "false",
    "nil",
    "len",
    "cap",
    "make",
    "new",
    "append",
    "copy",
    "delete",
    "panic",
    "recover",
    "print",
    "println",
    "string",
    "int",
    "bool",
    "byte",
    "rune",
    "error",
    "any",
}

# Identifiers that are not preceded by a dot. A name after a selector is a field
# or a method — dropping `EnforcesFirewall` from `fe.EnforcesFirewall()` orphans
# nothing, while dropping `fe` orphans a variable and breaks the build. The
# selftest found this on its first run: without the lookbehind the rule refused
# `if ok && fe != nil {`, which is the safe form it exists to recommend.
#
# Package names stay in scope on purpose: an unused import is a compile error in
# Go exactly as an unused variable is, so `strings` dropped from a fragment is
# the same class of mistake.
IDENT = re.compile(r"(?<![.\w])[A-Za-z_][A-Za-z0-9_]*")

# Go's three literal forms, so their contents never look like code. Interpreted
# strings and runes admit backslash escapes; a raw string runs to its closing
# backquote and holds no escapes at all.
LITERAL = re.compile(r'"(?:[^"\\\n]|\\.)*"' r"|`[^`]*`" r"|'(?:[^'\\\n]|\\.)*'", re.S)


def identifiers(text):
    """The identifiers a fragment of Go uses, minus keywords and builtins.

    String contents are removed first, and that is not tidiness. Without it the
    rule read `no route matches this path` as code and refused the mutation for
    dropping `this` — a word in a sentence, in the one guard of #179 whose whole
    subject is what the answer says. A tool that refuses a valid falsification
    is worse than no tool: it teaches the author to stop declaring the awkward
    ones, which are the guards most worth replaying.
    """
    return {name for name in IDENT.findall(LITERAL.sub('""', text)) if name not in RESERVED}


# A short declaration inside the fragment: `x := ...` or `x, ok := ...`.
DECLARED = re.compile(r"^\s*([A-Za-z_][A-Za-z0-9_,\s]*?)\s*:=", re.MULTILINE)


def uses_of(name, text):
    return len(re.findall(r"(?<![.\w])" + re.escape(name) + r"\b", text))


def orphaned_by_declaration(find, replace):
    """Names the replacement declares and stops using, which the original used.

    Presence alone is not enough, and an end-to-end run proved it: a fragment
    carrying its own `fe, ok := p.(FirewallEnforcer)` line keeps the word `fe` on
    both sides of any rewrite, so a set difference sees nothing lost while Go
    sees an unused variable.

    Comparative rather than absolute, and a second end-to-end run proved that
    too: a fragment can declare a name it uses *after* the fragment ends, as
    `sum := sha256.New()` does, and judging the replacement alone refused a
    perfectly good mutation. What matters is whether the mutation made it worse —
    the count falling to its declaration alone, from more than that.
    """
    orphans = []
    for match in DECLARED.finditer(replace):
        for name in (n.strip() for n in match.group(1).split(",")):
            if not name or name == "_" or name in RESERVED:
                continue
            if uses_of(name, replace) <= 1 < uses_of(name, find):
                orphans.append(name)
    return sorted(set(orphans))


def dropped_identifiers(find, replace):
    """Names the mutation removes from the fragment, whether or not Go minds.

    Two ways a removal costs a verdict, and both have happened here: a term is
    deleted and the name goes with it, or the name survives only in a
    declaration the fragment carries. Either leaves an unused name, Go refuses
    to compile, every test in the package fails, and that reads exactly like the
    guard being proven.

    This is deliberately conservative and says so where it refuses: it cannot
    see the rest of the file, so it flags every name that leaves the fragment,
    including the many that would still compile. Replaying the 0.9 train against
    it found three such: `IncusMinimum` is a package-level var used at four
    other sites, `synthetic` and `health` are locals used further down their own
    functions. All three mutations would have built.

    Refusing them anyway is the trade this tool makes, and the reason is that
    the alternative is worse. The check that would let them through is the
    compiler, which the harness already runs — but by then the copy exists, the
    package is built, and a void verdict has cost what a real one costs. The
    rewrite it demands takes one operator (`&& false`, `|| true`) and produces a
    strictly better mutation: one that changes a truth value and nothing else,
    so the test that goes red is answering about the guard rather than about
    whatever else the deleted term was doing.
    """
    lost = identifiers(find) - identifiers(replace)
    return sorted(lost | set(orphaned_by_declaration(find, replace)))


class Verdict(int):
    """An exit code that is a verdict: every mutation of the spec was applied
    and its test run. main() returns one only from its last line. Every other
    code it returns, whatever its value, is the ABSENCE of a verdict.

    The number alone cannot say which (#954). A copy red before any mutation
    used to answer 2 so that the replay could tell it from a guard that stopped
    biting (1); 2 is drift in this repository (rule 9), and a red tree is not
    drift. It answers 1 now, like a guard that stopped biting, and what tells
    them apart is this type, which an int that merely equals 1 does not carry.
    """


def replay_outcome(code):
    """What one spec's exit code means for the replay's tally.

    Three answers, and the middle one is the whole point. A Verdict of 1 is a
    VERDICT: a mutation applied, compiled, and the test stayed green, so that
    guard is not guarded. Any other non-zero is the ABSENCE of a verdict: the
    copy was red before any mutation (1, a plain int), or the spec was refused
    (2), so nothing was measured. Read the value alone and the red copy becomes
    a broken guard, which is the arithmetic of 2026-09-14 below.

    They were added together until 2026-09-14, and a mise shim that would not
    start in the copy turned that arithmetic into "199 of 199 falsifications no
    longer hold" — every guard in the repository reported broken, by a harness
    that had just said 199 times that it was measuring nothing.

    The selftest drives THIS function rather than a copy of its logic, or the
    check would pass while the replay went on adding the two together.
    """
    if code == 0:
        return "bit"
    if isinstance(code, Verdict):
        return "stopped biting"
    return "not measured"


def copy_tree(src, dst):
    def ignore(directory, names):
        out = [n for n in names if n in EXCLUDE]
        if (
            os.path.abspath(directory) == os.path.abspath(src)
            and "feint" in names
            and os.path.isfile(os.path.join(directory, "feint"))
        ):
            out.append("feint")
        return out

    if os.path.exists(dst):
        shutil.rmtree(dst)
    shutil.copytree(src, dst, ignore=ignore, symlinks=True)


def run(dst, cmd, extra_env=None):
    """Every command is capped, so a runaway cannot take the station with it.

    `extra_env` exists for a subject whose test is itself behind an opt-in: the
    container tests skip unless FEINT_TESTCONTAINER is set, and a skip counts as
    a pass, so without this the harness would report the mutation undetected and
    blame the guard rather than its own environment.
    """
    # The copy carries the repository's mise.toml, and on a station where `go`
    # is a mise shim, mise refuses to start in a directory whose config it has
    # not been told to trust. It fails before the compiler, with an EMPTY
    # stdout, so the harness read it as "the copy is red" and reported 199
    # guards as no longer holding while nothing was wrong with any of them.
    # Measured on 2026-09-14. TestACopyIsTrustedSoTheToolchainStarts holds it.
    env = {**os.environ, "MISE_TRUSTED_CONFIG_PATHS": dst}
    if extra_env:
        env.update(extra_env)
    return subprocess.run(
        ["systemd-run", "--user", "--scope", "-q", "-p", "MemoryMax=8G", "-p", "MemorySwapMax=0"]
        + cmd,
        cwd=dst,
        capture_output=True,
        text=True,
        env=env,
    )


# The baseline verdicts already proved in this process, keyed by the packages and
# the environment they were proved under. Empty for a single-spec run, which
# therefore behaves exactly as before.
BASELINES = {}


def baseline_key(packages, spec_env):
    """What makes two baseline questions the same question.

    The packages, and the environment: `env` arms tests that otherwise skip, so
    two specs on one package with different env are two different questions and
    must not share an answer.
    """
    return (tuple(packages), tuple(sorted((spec_env or {}).items())))


def unrestored(dst, pristine):
    """Every mutated file whose bytes are not what they were before any mutation.

    This is the restore claim asked directly, and it replaces a second full test
    run of the package. If every mutated file is byte-identical to the copy the
    baseline was green on, the package IS that copy, and a test run can only
    confirm what the bytes already settle.

    Why it matters (#890): the baseline and the restoration runs were two full
    suites per spec. Measured on 2026-11-12, that was ~69 minutes of a
    ~123-minute replay on one station — 60 specs name ./internal/cli/ and each
    ran its 11-second suite twice — and the night had just been cancelled at its
    210-minute ceiling.

    What it does NOT cover, said rather than implied: a test that writes
    somewhere else in the copy. The old run would not have named that either —
    it would have gone red somewhere unrelated — and the copy is deleted
    immediately afterwards.
    """
    out = []
    for name, body in pristine.items():
        with open(os.path.join(dst, name), encoding="utf-8") as fh:
            if fh.read() != body:
                out.append(name)
    return sorted(out)


def refuse(message, code=2):
    """Print why, and hand back 2 by default: what the lint refuses is a spec
    that drifted from the code. A usage error passes code=1 (rule 9: 0 ok, 1
    error, 2 drift); it used to be 2, which a caller reads as drift.

    The line between the two, for every exit of this program (#954): 2 means
    the fault is in a committed spec and the spec is what to fix (a fragment
    gone or doubled, a test renamed, a path spelled two ways, a mutation that
    would not compile or changes nothing, a spec with no mutation). 1 means the
    fault is anywhere else: the invocation, the tree, the environment, the
    harness, a guard that stopped biting, or nothing to judge at all. A
    directory with no spec in it is the last: no spec drifted, there is none.
    _exit_code_cases() asserts the code and the reason of each outcome it can
    reach offline."""
    print(f"falsify: {message}", file=sys.stderr)
    return code


# The mutations that voided a verdict on 2026-08-15, and the safe form
# each one should have taken. This is the rule's own falsification: it is not
# invented cases, it is what actually happened, in unrelated issues, plus the
# fourth shape an end-to-end run of this harness found afterwards.
HISTORY = [
    # (find, broken replacement, safe replacement, the name that was orphaned)
    ("if ok && fe.EnforcesFirewall() {", "if ok {", "if ok && fe != nil {", "fe"),
    (
        'if strings.TrimSpace(string(out)) == "" {',
        "if false {",
        'if strings.TrimSpace(string(out)) == "\\x00never" {',
        "out",
    ),
    (
        "if err != nil || health.Resources == 0 {",
        "if health.Resources == 0 {",
        "if (err != nil && false) || health.Resources == 0 {",
        "err",
    ),
    # The fourth, found by running the harness end to end rather than by
    # reasoning about it: when the fragment carries its own declaration, the name
    # survives on both sides and a set difference sees nothing.
    (
        "fe, ok := p.(FirewallEnforcer)\n\t\tif ok && fe.EnforcesFirewall() {",
        "fe, ok := p.(FirewallEnforcer)\n\t\tif ok {",
        "fe, ok := p.(FirewallEnforcer)\n\t\tif ok && fe != nil {",
        "fe",
    ),
]


# Valid mutations the rule must not refuse. The first is the one that made this
# list necessary: the rule read the contents of a Go string as code, so changing
# a sentence looked like dropping a name. The rest are the neutralising rewrites
# the 0.9 specs now carry, kept here so the accepted style is asserted and not
# only demonstrated.
ACCEPTED = [
    (
        'return "no route matches this path, but it is served under " +',
        'return "no route matches " + path + ", but it is served under " +',
        "a word inside a string is not an identifier",
    ),
    (
        "if olderThan(got, IncusMinimum) {",
        "if olderThan(got, IncusMinimum) && false {",
        "a condition made never true, every name still evaluated",
    ),
    (
        "if rec.status < 400 && !synthetic {",
        "if rec.status < 400 && (!synthetic || true) {",
        "a term neutralised by disjunction rather than deleted",
    ),
    (
        "if err != nil || health.Resources == 0 {",
        "if err != nil || (health.Resources == 0 && false) {",
        "one branch of a disjunction disarmed, both still read",
    ),
]


def drop_inherited_git(environ):
    """Remove every GIT_* variable from `environ` and return their names, sorted.

    Inside a git hook, git exports GIT_DIR and GIT_INDEX_FILE, and a child git
    obeys them before it looks at its working directory. This program copies
    the repository WITHOUT .git and runs `go test` in the copy, so a test there
    that ran git with the hook's variables would write into the repository the
    hook named, not into the copy (#955; the Go tests measured it on
    2026-11-14). Dropped once, when the program starts, before any selftest
    case or any replay: what this protects is whatever runs afterwards, a git
    called by a script or a program name nobody can read statically.
    _git_scrub_cases() holds the function and the wiring both.
    """
    names = sorted(name for name in environ if name.startswith("GIT_"))
    for name in names:
        del environ[name]
    return names


# What drop_inherited_git() removed when this program started; None when the
# module was not started as a program, so the selftest can say it cannot judge.
DROPPED_GIT = None


def _git_scrub_cases():
    """One case per question drop_inherited_git() asks, then the wiring.

    The wiring is only measurable with a witness: a process started without any
    GIT_* passes "none is left" for nothing. TestTheFalsifyHarnessHoldsItsOwnSelftest
    exports GIT_DIR and GIT_INDEX_FILE (towards a decoy under its own temporary
    directory) and requires the line below that names GIT_DIR as dropped.
    """
    failures = 0
    for environ, want, why in (
        ({"GIT_DIR": "/d", "GIT_INDEX_FILE": "/d/i"}, {}, "a variable a hook exports is dropped"),
        ({"MY_GIT_DIR": "x"}, {"MY_GIT_DIR": "x"}, "GIT_ inside a name is kept"),
        ({"PATH": "/bin"}, {"PATH": "/bin"}, "an unrelated variable is kept"),
    ):
        got = dict(environ)
        drop_inherited_git(got)
        if got != want:
            print(f"!! drop_inherited_git leaves {got!r}, want {want!r}: {why}")
            failures += 1
        else:
            print(f"ok  {why}")
    left = sorted(name for name in os.environ if name.startswith("GIT_"))
    if DROPPED_GIT is None:
        print("!! not started as a program: whether GIT_* was dropped cannot be judged")
        failures += 1
    elif left:
        print(f"!! the process still holds {', '.join(left)}: a git started now obeys a hook")
        failures += 1
    else:
        print(
            "ok  the process holds no GIT_* variable; dropped at start: "
            + (", ".join(DROPPED_GIT) or "none was exported")
        )
    return failures


def selftest():
    """Prove the rule on the four mistakes of 2026-08-15 it was written for.

    Four, not three: the three unrelated issues of that day plus the one found
    the same day by running the harness end to end (#187). Both counts printed
    below come from len(HISTORY) and len(ACCEPTED), so this docstring is the only
    place the number could go stale, and CONTRIBUTING.md said three while this
    printed four for long enough to be found by an audit (#937).
    """
    failures = _git_scrub_cases()
    print()
    for find, replace, why in ACCEPTED:
        lost = dropped_identifiers(find, replace)
        if lost:
            print(f"!! the rule refuses a valid mutation, losing {lost}: {why}")
            failures += 1
        else:
            print(f"ok  accepted, {why}")
    print()
    for find, broken, safe, orphan in HISTORY:
        lost = dropped_identifiers(find, broken)
        if orphan not in lost:
            print(f"!! the rule accepts a mutation that orphans {orphan}: {broken}")
            failures += 1
        else:
            print(f"ok  refused, {orphan} would be orphaned: {broken}")
        # And the accepting half, which matters as much: a rule that refused
        # every mutation would pass the checks above and make the tool useless.
        still = dropped_identifiers(find, safe)
        if still:
            print(f"!! the rule refuses the safe form too, losing {still}: {safe}")
            failures += 1
        else:
            print(f"    accepted, every name kept: {safe}")
    print()
    failures += lint_selftest()

    print()
    if failures:
        print(f"{failures} failure(s): the rule does not hold its own history")
        return 1
    print(
        HISTORY_SENTENCE.format(n=len(HISTORY))
        + f", and accepts {len(ACCEPTED)} valid mutations it has refused at some point"
    )
    return 0


def _selftest_module(tmp, second_package=False, gated=False, panicking=False):
    """A Go module for the lint to be pointed at, returning its two subject
    files. With `second_package`, it holds a q/ whose test is NOT in p/, which
    is what _cross_package_case() needs and what one package cannot show.

    It is a real module, with real tests in it, because since #923 the lint asks
    `go test` whether a declared name selects anything — a question no fake tree
    can answer. Compiling it, and running the parents the subtest cases name, is
    the whole of what the selftest now costs. Measured on 2026-11-14 on
    Stéphane's station, and the invocation is part of the number rather than a
    detail: 1.37 s to 1.44 s through `mise run falsify:selftest`, 2.92 s to
    3.04 s through a bare `python3`, the same 32 assertions printed either way.
    The gap is two interpreter installs on that station — mise runs the pinned
    3.12.11, a bare `python3` resolves 3.12.13 — not the work this does. Either
    way it is against 0.03 s for the build-free version.

    Beside it rather than over it, the same day after #938, #949 and #950 added
    their cases: 1.43 s to 1.51 s over three runs through `mise run
    falsify:selftest` with GOMAXPROCS=4 GOFLAGS=-p=2, 49 `ok` lines.

    Beside both, on 2026-11-15 with the cases of #954 and #955 and the
    skipped, anchoring and field-type cases: 2.31 s to 2.39 s over three runs
    through `mise run falsify:selftest` with GOMAXPROCS=4 GOFLAGS=-p=2, 104
    `ok` lines. Wall time of the mise task, measured with `date +%s%N` around
    it, so it includes mise starting; the earlier figures may not.

    Beside those, on 2026-11-15 with the cases of #965, same invocation and
    same flags: 3.69 s to 4.30 s over three runs, 121 `ok` lines (one run
    before them, on the same tree without #965: 1.91 s, 104 lines). One of
    the new seconds is spent on purpose: _hang_cases() waits a real `go test`
    out to its 1 s timeout, because a hang is only shown by hanging.

    Beside those, the same day, after the lint bounds were added: 5.42 s to
    5.49 s over three runs, 127 `ok` lines, same invocation and flags. Two
    more seconds spent on purpose, one per lint call killed at its 1 s bound
    in _lint_bound_cases().

    Beside those, after the red-baseline cases of #967: 5.64 s to 5.72 s over
    three runs, 133 `ok` lines, same invocation and flags.

    Beside those, after cycle 1 of lot A (both kill forms, the exit-0 case,
    more red-baseline rows): 5.70 s to 5.75 s over three runs, 142 `ok`
    lines. Go's own kill is driven from captured output, not live: it comes
    at the timeout plus one minute (see _hang_cases()).

    Beside those, after cycle 2 (the duration read as a Go duration): 5.66 s
    to 5.68 s over three runs, 144 `ok` lines.

    The module name is a constant, and it makes no difference either way: the
    same measurement under a module name the build cache had never seen cost
    0.54 s, because the cache keys on the bytes compiled and not on the path
    they sat at. An earlier note here claimed the opposite, and it was a
    mechanism this function does not have.
    """
    os.mkdir(os.path.join(tmp, "p"))
    with open(os.path.join(tmp, "go.mod"), "w", encoding="utf-8") as fh:
        fh.write("module falsifyselftest\n\ngo 1.26\n")

    subject = os.path.join(tmp, "p", "subject.go")
    with open(subject, "w", encoding="utf-8") as fh:
        fh.write("package p\n\nfunc guard() bool { return true }\n")

    # A second subject carrying the same fragment twice, which is what a spec
    # meets when its subject is duplicated after the spec was written. The
    # witness for the ambiguity rule: without a file that really holds two
    # matches, the case would pass by finding nothing.
    twice = os.path.join(tmp, "p", "twice.go")
    with open(twice, "w", encoding="utf-8") as fh:
        fh.write("package p\n\nfunc a() bool { return true }\n\nfunc b() bool { return true }\n")

    with open(os.path.join(tmp, "p", "guard_test.go"), "w", encoding="utf-8") as fh:
        fh.write(
            'package p\n\nimport "testing"\n\n'
            "func TestTheGuardHolds(t *testing.T) {\n"
            '\tt.Run("named-sub", func(t *testing.T) {\n'
            '\t\tif !guard() || !a() || !b() {\n\t\t\tt.Fatal("the guard does not hold")\n\t\t}\n'
            "\t})\n}\n"
        )

    # Two names, one the prefix of the other, and only the longer one catches
    # a mutation of guard(): what _anchoring_cases() and the lint's
    # "prefix.json" case need, a name that exists and selects a neighbour.
    with open(os.path.join(tmp, "p", "loose_test.go"), "w", encoding="utf-8") as fh:
        fh.write(
            'package p\n\nimport "testing"\n\n'
            "func TestLoose(t *testing.T) {}\n\n"
            "func TestLooseButStrict(t *testing.T) {\n"
            '\tif !guard() {\n\t\tt.Fatal("the guard does not hold")\n\t}\n}\n'
        )

    if gated:
        # A parent that skips unless the spec's own `env` reaches the
        # measurement, with its subtest behind the gate: what the committed
        # specs declaring `env` would look like the day one of them names a
        # subtest. _parent_cannot_answer_case()'s neighbour.
        with open(os.path.join(tmp, "p", "gated_test.go"), "w", encoding="utf-8") as fh:
            fh.write(
                'package p\n\nimport (\n\t"os"\n\t"testing"\n)\n\n'
                "func TestGatedByEnv(t *testing.T) {\n"
                '\tif os.Getenv("FEINT_SELFTEST_ARMED") == "" {\n'
                '\t\tt.Skip("not armed")\n\t}\n'
                '\tt.Run("armed-sub", func(t *testing.T) {\n'
                '\t\tif !guard() {\n\t\t\tt.Fatal("the guard does not hold")\n\t\t}\n'
                "\t})\n}\n"
            )

    if panicking:
        # A parent that dies before reaching its t.Run: the run exits non-zero
        # having entered no subtest, which is a tree that could not answer and
        # not a stale name.
        with open(os.path.join(tmp, "p", "panicking_test.go"), "w", encoding="utf-8") as fh:
            fh.write(
                'package p\n\nimport "testing"\n\n'
                "func TestThePanickingParent(t *testing.T) {\n"
                '\tpanic("before any t.Run")\n}\n'
            )

    if second_package:
        os.mkdir(os.path.join(tmp, "q"))
        with open(os.path.join(tmp, "q", "elsewhere.go"), "w", encoding="utf-8") as fh:
            fh.write("package q\n\nfunc elsewhere() bool { return true }\n")
        with open(os.path.join(tmp, "q", "elsewhere_test.go"), "w", encoding="utf-8") as fh:
            fh.write(
                'package q\n\nimport "testing"\n\n'
                "func TestTheGuardLivesInQ(t *testing.T) {\n"
                '\tif !elsewhere() {\n\t\tt.Fatal("not here either")\n\t}\n}\n'
            )
    return subject, twice


def _one_spec(directory, name, *, package, file, find, test, replace=None, env=None):
    """Write one spec holding one mutation, and return nothing but the file."""
    spec = {
        "package": package,
        **({"env": env} if env else {}),
        "mutations": [
            {
                "label": f"selftest {name}",
                "file": file,
                "find": find,
                "replace": replace or find.replace("true", "true && false"),
                "test": test,
            }
        ],
    }
    path = os.path.join(directory, name)
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(spec, fh)
    return path


def _linted(directory, root):
    """lint() with what it printed on stderr, so a case can hold the reason too."""
    said = io.StringIO()
    with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
        got = lint(directory, root)
    return got, said.getvalue()


def _report(got, want, why, said="", reason=None):
    """One lint case: the exit code AND the reason (#938).

    The lint answers 2 for eight different reasons — a missing file, a path
    spelled two ways, a dead fragment, an ambiguous one, a name the package does
    not hold, a pattern Go would refuse, a subtest the parent does not carry, a
    listing a broken package made untrustworthy — so a case that compared the
    code alone could pass through a branch its author never meant. That happened
    twice while #923 was written: two cases named their subject relatively,
    fragment_problem() read it against another tree, and both answered 2
    through "no such file", green under the very mutants they existed to catch.

    So a refusing case declares the words its branch prints, and a case that
    declares none is itself refused: an unrecognised shape must refuse, not
    pass. An accepting case must print no refusal at all.
    """
    head = said.strip().splitlines()
    if want != 0 and not reason:
        print(
            f"!! the case {why!r} expects a refusal and names no reason: it cannot tell them apart"
        )
        return 1
    if got != want:
        print(f"!! the lint answers {got} on {why}, want {want}")
        for line in head[:4]:
            print(f"   it said: {line}")
        return 1
    if reason and reason not in said:
        print(f"!! the lint answers {got} on {why}, but not because {reason!r}. It said:")
        for line in head[:6]:
            print(f"   {line}")
        return 1
    if want == 0 and head:
        print(f"!! the lint answers 0 on {why}, and still printed: {head[0]}")
        return 1
    print(f"ok  the lint answers {got} on {why}" + (f": {reason}" if reason else ""))
    return 0


def _usage_case():
    """`--help` prints the usage and exits 0; it used to open a file named --help.

    And a usage error exits 1, never 2: 2 is drift (rule 9), and an unknown
    option or a missing argument is not a spec that drifted.
    """
    failures = 0
    out = io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(io.StringIO()):
        got = main([argv0, "--help"])
    if got != 0 or not out.getvalue().startswith("usage: "):
        print(f"!! falsify.py --help answers {got}, want 0 and the usage")
        failures += 1
    else:
        print("ok  falsify.py --help answers 0 with the usage")
    for args, why in (([argv0, "--bogus"], "an unknown option"), ([argv0], "no argument")):
        err = io.StringIO()
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(err):
            got = main(args)
        if got != 1 or "usage: " not in err.getvalue():
            print(f"!! falsify.py answers {got} on {why}, want 1 and the usage (2 is drift)")
            failures += 1
        else:
            print(f"ok  falsify.py answers 1 on {why}, with the usage")
    return failures


def _cross_package_case():
    """The package filter is load-bearing, and only two packages can prove it.

    This is #923 verbatim: a-transport-fault-is-bounded.json declared
    ./internal/core/emulator/ while the test it named lived in internal/cli, and
    the harness reported TEST STILL PASSED. So the question the check has to
    answer is not "does this name exist somewhere" but "does it exist in THIS
    package", and a module of one package cannot tell the two apart — the union
    over the packages a spec declares and the union over the whole listing are
    then the same set.

    Hence two packages AND two specs: the lint lists both in one call, exactly
    as the committed run lists every pattern in one call. Replace the body of
    covered_paths() with `return set(known)` and this case answers 0.

    Two accepting runs come first in the same module, and their order is the
    point (#938, two of the four behaviours no case held):

      - a spec on p alone, which leaves a listing of p in LISTINGS;
      - a spec whose default package is p and whose one mutation overrides it
        with q. Drop the per-mutation `package` in entries_of() and the name is
        looked for in p and refused. Drop `packages` from listed_tests()'s
        memoisation key and the listing of p is handed back for q, and the
        name is refused just the same.
    """
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp, second_package=True)

        only_p = os.path.join(tmp, "only-p")
        os.mkdir(only_p)
        _one_spec(
            only_p,
            "only-p.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        got, said = _linted(only_p, tmp)
        failures += _report(got, 0, "a spec on one package of two", said)

        override = os.path.join(tmp, "override")
        os.mkdir(override)
        path = _one_spec(
            override,
            "override.json",
            package="./p/",
            file="q/elsewhere.go",
            find="func elsewhere() bool { return true }",
            test="TestTheGuardLivesInQ",
        )
        with open(path, encoding="utf-8") as fh:
            spec = json.load(fh)
        spec["mutations"][0]["package"] = "./q/"
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(spec, fh)
        got, said = _linted(override, tmp)
        failures += _report(
            got, 0, "a mutation whose own package overrides the spec's, listed after another", said
        )

        directory = os.path.join(tmp, "two")
        os.mkdir(directory)
        # The name lives in q. One spec declares p, which is the mistake; the
        # other declares q, which is correct and is what makes the lint list
        # both packages in the same call.
        _one_spec(
            directory,
            "names-p.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardLivesInQ",
        )
        _one_spec(
            directory,
            "names-q.json",
            package="./q/",
            file="q/elsewhere.go",
            find="func elsewhere() bool { return true }",
            test="TestTheGuardLivesInQ",
        )
        got, said = _linted(directory, tmp)
    failures += _report(
        got,
        2,
        "a name that exists in another package than the one declared",
        said,
        "./p/ holds no test matching 'TestTheGuardLivesInQ'",
    )
    return failures


def _unbuildable_case():
    """A listing that could not be taken is not a listing that found everything.

    The check reads `go test -list`, and a tree that does not build gives it a
    non-zero exit and a partial listing. Reporting the declared name as missing
    would blame the spec for the compiler, and reporting nothing would be the
    empty population again — the exact shape of the defect this check closes.

    The first version of this case could not see that branch: it put the broken
    file in the package that held the test, so the name was missing anyway and
    the ordinary path answered 2. The branch is reachable in the shape that
    matters — `go test -list .* ./...` with one package broken prints the other
    packages' names, exits non-zero, and every declared name reads as found over
    a listing nobody can trust. So: the name really exists in p, bad/ does not
    compile, and the spec declares ./... . Delete the `if err and not missing:`
    block, or stop capturing the exit code, and this case answers 0.

    The reason is held, not only the 2 (#938), and that is what holds the `...`
    branch of covered_paths(): without it, ./... covers no listed package, the
    name reads as missing, and the lint still answers 2 — through "holds no test
    matching", which is not this case's reason.
    """
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        os.mkdir(os.path.join(tmp, "bad"))
        with open(os.path.join(tmp, "bad", "broken.go"), "w", encoding="utf-8") as fh:
            fh.write("package bad\n\nfunc oops( {\n")

        directory = os.path.join(tmp, "unbuildable")
        os.mkdir(directory)
        _one_spec(
            directory,
            "unbuildable.json",
            package="./...",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        got, said = _linted(directory, tmp)
    return _report(
        got,
        2,
        "a name that exists, over a listing one broken package made untrustworthy",
        said,
        "so no name was really confirmed",
    )


def _single_spec_case():
    """The wiring in main(), not the function it calls.

    The lint and the single-spec run are two entry points, and only one of them
    was measured. With `unknown = []` in main(), a `mise run falsify -- <spec>`
    naming a dead test copies the tree, runs nothing, reads the 0, and prints
    TEST STILL PASSED — #923 restored verbatim while every lint case stays
    green. Measured end to end on 2026-11-14.

    main() resolves its packages against the working directory, so this drives
    it from inside the temporary module. The pristine path returns before a
    single file is copied, which is why this case costs milliseconds.
    """
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        spec = _one_spec(
            tmp,
            "single.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHoldsUnderANameNobodyDeclared",
        )
        here = os.getcwd()
        try:
            os.chdir(tmp)
            got = main([argv0, spec])
        finally:
            os.chdir(here)
    why = "a single-spec run whose declared test does not exist (main(), not lint())"
    if got != 2:
        print(f"!! the single-spec run answers {got} on {why}, want 2")
        return 1
    print(f"ok  the single-spec run answers {got} on {why}")

    # And main() refuses a path spelled two ways before copying anything
    # (#950): the fragment is alive and the test exists, so without
    # path_problem() in main() this run copies the module, sees the test bite,
    # and answers 0.
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        spec = _one_spec(
            tmp,
            "dotted-single.json",
            package="./p/",
            file="./p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        here, said = os.getcwd(), io.StringIO()
        try:
            os.chdir(tmp)
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = main([argv0, spec])
        finally:
            os.chdir(here)
    why = "a single-spec run whose file is spelled ./p/subject.go"
    if got != 2 or "is not written in its normal form" not in said.getvalue():
        print(f"!! the single-spec run answers {got} on {why}, want 2 naming the spelling")
        return 1
    print(f"ok  the single-spec run answers {got} on {why}, naming the spelling")

    # A directory, in main() and not only in lint(): `p` reached the open()
    # and died on IsADirectoryError, and `.` was refused as a symbolic link.
    failures = 0
    for target in ("p", "."):
        with tempfile.TemporaryDirectory() as tmp:
            _selftest_module(tmp)
            spec = _one_spec(
                tmp,
                "directory-single.json",
                package="./p/",
                file=target,
                find="func guard() bool { return true }",
                test="TestTheGuardHolds",
            )
            here, said = os.getcwd(), io.StringIO()
            try:
                os.chdir(tmp)
                with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                    got = main([argv0, spec])
                    spec_dir = main([argv0, tmp])
            finally:
                os.chdir(here)
        why = f"a single-spec run whose file is the directory {target!r}"
        if got != 2 or f"{target}: is not a regular file" not in said.getvalue():
            print(f"!! the single-spec run answers {got} on {why}, want 2 and the reason")
            failures += 1
        else:
            print(f"ok  the single-spec run answers {got} on {why}, saying it is no regular file")
        if spec_dir != 1:
            print(f"!! a spec argument that is a directory answers {spec_dir}, want 1")
            failures += 1
    return failures


def _parent_cannot_answer_case():
    """A parent that could not answer is not a subtest that does not exist.

    The exit code of the subtest run is not the measurement, and it still has to
    be read: a parent that panics before reaching its `t.Run` enters no subtest
    and exits non-zero, so "no subtest of it matches" would blame the spec for a
    tree that could not answer. It is the line listed_tests() and
    _unbuildable_case() already draw one level up, drawn one level down.

    This case asserts the REASON and not only the exit code, because 2 is the
    answer for several different reasons and the distinction is the whole
    subject here. Stop reading `res.returncode` in ran_under() and the reported
    reason becomes "no subtest of it matches", which this requires it not to be.
    """
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp, panicking=True)
        found = tests_that_do_not_exist(
            [("panicking.json", "selftest", "TestThePanickingParent/unreachable-sub", "./p/", ())],
            tmp,
        )
    why = "a parent that panics before its t.Run, which confirms nothing either way"
    if len(found) != 1 or "no subtest was really confirmed" not in found[0][4]:
        print(f"!! the check reports {found!r} on {why}")
        print("   want one finding whose reason says no subtest was really confirmed")
        return 1
    print(f"ok  the check names the doubt rather than the spec on {why}")
    return 0


def _env_armed_case():
    """The spec's own `env` has to reach the subtest measurement.

    A spec declares `env` for a subject whose tests skip without it, and the
    replay already hands it to run(). A parent that skips enters no subtest, so
    a check that measured under the ambient environment would REFUSE a valid
    spec — the family where one check stops every falsification at once, and the
    reason this harness has an ACCEPTED list at all.

    Latent in the committed specs today, because the one spec declaring `env`
    names no subtest; the first spec combining the two would have been refused.
    Drop the env from the ran_under() call and this case answers 2.
    """
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp, gated=True)
        directory = os.path.join(tmp, "gated")
        os.mkdir(directory)
        _one_spec(
            directory,
            "env-armed.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestGatedByEnv/armed-sub",
            env={"FEINT_SELFTEST_ARMED": "1"},
        )
        got, said = _linted(directory, tmp)
    return _report(
        got, 0, "a subtest that exists only when the spec's own env arms its parent", said
    )


def lint_selftest():
    """The lint refuses each stale shape for its own reason, and accepts a live spec.

    A control that never fires reads exactly like a control that passes, which
    is the failure this whole program exists to name. So the lint is pointed at
    specs built here, each one wrong in exactly ONE way: a live fragment, a test
    that exists, a file named plainly — except for the one thing the case is
    about. A case wrong in several ways at once cannot tell which question
    refused it, and neutralising any one of them changes no verdict (#938).

    Every `file` is relative, the way a committed spec names it. The selftest
    used to name its subjects absolutely to get around fragment_problem()
    reading them against another tree than the Go half, which removed the
    symptom and kept the cause; #938 put both halves on `root` instead.

    Written against a temporary directory rather than the repository's own
    specs, because those are supposed to be green — a selftest that depended on
    them would go red the day somebody legitimately retargets a mutation, and
    that is the noise which teaches people to skip the check.
    """
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        live = "func guard() bool { return true }"
        subject = "p/subject.go"
        # An absolute link, the shape copy_tree() carries out of the copy.
        os.symlink(os.path.join(tmp, "p"), os.path.join(tmp, "via"))

        # (file, spec name, fragment, test, want, why, the words its branch prints)
        cases = [
            (subject, "live.json", live, "TestTheGuardHolds", 0, "a live spec", None),
            # Regex, not literal: twelve committed specs anchor their name as
            # `^Test…$`, and a lint comparing names literally would refuse all
            # twelve. #938 measured that no case noticed.
            (
                subject,
                "anchored.json",
                live,
                "^TestTheGuardHolds$",
                0,
                "a name anchored as a regular expression, the way `-run` reads it",
                None,
            ),
            (
                "p/gone.go",
                "gone.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file that does not exist",
                "p/gone.go: no such file",
            ),
            (
                subject,
                "dead.json",
                "func guard() bool { return gone }",
                "TestTheGuardHolds",
                2,
                "a fragment that is not in the file",
                "p/subject.go: fragment not found",
            ),
            (
                "p/twice.go",
                "ambiguous.json",
                "bool { return true }",
                "TestTheGuardHolds",
                2,
                "a fragment that matches two places, where only the first is rewritten",
                "fragment matches 2 places",
            ),
            # #950. The three spellings below name the subject the compiler
            # reads, with a live fragment and a test that exists, so nothing
            # but path_problem() can refuse them: `mise run testplan` compares
            # the path with a diff as written and would never claim the spec.
            (
                "./p/subject.go",
                "dotted.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file spelled with a leading ./",
                "./p/subject.go: is not written in its normal form 'p/subject.go'",
            ),
            (
                "q/../p/subject.go",
                "climbing.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file spelled through a ..",
                "is not written in its normal form 'p/subject.go'",
            ),
            (
                os.path.join(os.pardir, os.path.basename(tmp), "p", "subject.go"),
                "outside.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file reached by climbing out of the tree and back, normal as written",
                "leaves the repository",
            ),
            (
                os.path.join(tmp, "p", "subject.go"),
                "absolute.json",
                live,
                "TestTheGuardHolds",
                2,
                "an absolute file, which a single-spec run would rewrite in place",
                "is absolute",
            ),
            # #950, the fourth spelling. Relative and normal as written, a
            # live fragment, a test that exists: only the link is wrong, and
            # a replay would write the mutation into the working p/subject.go.
            (
                "via/subject.go",
                "symlinked.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file reached through a symbolic link that leaves the copy",
                "via/subject.go: goes through a symbolic link",
            ),
            # A directory is a path that exists and holds no fragment: it used
            # to raise IsADirectoryError without naming the spec.
            (
                "p",
                "directory.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file that is a directory",
                "p: is not a regular file",
            ),
            (
                ".",
                "root.json",
                live,
                "TestTheGuardHolds",
                2,
                "a file that is the root itself, which is no link",
                ".: is not a regular file",
            ),
            # #923. The fragment is alive and the package is right; only the
            # name is stale, which is what a renamed test leaves behind. Without
            # tests_that_do_not_exist() the lint answers 0 here, the replay runs
            # `go test -run TestTheGuardHoldsRenamed` over a package holding no
            # such test, `go test` exits 0, and the harness prints
            # TEST STILL PASSED about a guard it never measured.
            (
                subject,
                "renamed.json",
                live,
                "TestTheGuardHoldsRenamed",
                2,
                "a live fragment whose test has been renamed out from under it",
                "./p/ holds no test matching 'TestTheGuardHoldsRenamed'",
            ),
            # And the accepting half, on the same module: a name that is really
            # there must stay green, or the check would redden every spec and
            # teach people to skip it.
            (
                subject,
                "subtest.json",
                live,
                "TestTheGuardHolds/named-sub",
                0,
                "a subtest of a test that exists, which `-list` never prints on its own",
                None,
            ),
            # #923 one level down, and it was live in the first version of this
            # check: `go test -run TestX/no-such-sub` runs TestX, no subtest
            # matches, the parent passes, the run exits 0, and the replay prints
            # TEST STILL PASSED. Measured against this harness on 2026-11-14:
            # named-sub gave "the test bit", no-such-sub gave TEST STILL PASSED.
            (
                subject,
                "no-such-sub.json",
                live,
                "TestTheGuardHolds/no-such-sub",
                2,
                "a subtest name no subtest of that parent carries",
                "but no subtest of it matches 'TestTheGuardHolds/no-such-sub'",
            ),
            # A name that is only the prefix of a test: unanchored, `-run
            # TestTheGuardHold` selects TestTheGuardHolds and the lint answers
            # 0, then the replay measures the neighbour (see anchored()).
            (
                subject,
                "prefix.json",
                live,
                "TestTheGuardHold",
                2,
                "a name that is only the prefix of a test that exists",
                "./p/ holds no test matching 'TestTheGuardHold'",
            ),
            # A `test` field that is not a usable pattern. Not a void verdict but
            # a FALSE one: `go test -run 'TestX('` rejects the pattern and exits
            # non-zero, the replay reads non-zero as "the test bit", and the
            # harness prints `ok` for a mutation it never ran.
            (
                subject,
                "unparsable.json",
                live,
                "TestTheGuardHolds(",
                2,
                "a test field that is not a usable pattern, which would read as a proof",
                "'TestTheGuardHolds(' is not a usable pattern",
            ),
        ]
        for target, name, find, test, want, why, reason in cases:
            directory = os.path.join(tmp, name.removesuffix(".json"))
            os.mkdir(directory)
            _one_spec(
                directory,
                name,
                package="./p/",
                file=target,
                find=find,
                test=test,
                replace=find.replace("true", "false"),
            )
            got, said = _linted(directory, tmp)
            failures += _report(got, want, why, said, reason)

    failures += _usage_case()
    failures += _cross_package_case()
    failures += _parent_cannot_answer_case()
    failures += _env_armed_case()
    failures += _unbuildable_case()
    failures += _single_spec_case()
    print()
    failures += _published_history_cases()
    print()
    failures += _replay_reporting_cases()
    print()
    failures += _exit_code_cases()
    print()
    failures += _unreadable_cases()
    print()
    failures += _spec_outcome_cases()
    print()
    failures += _skipped_cases()
    print()
    failures += _anchoring_cases()
    print()
    failures += _field_type_cases()
    print()
    failures += _budget_cases()
    print()
    failures += _hang_cases()
    print()
    failures += _lint_bound_cases()
    print()
    failures += _red_baseline_cases()
    return failures


def _red_toolchain(dst, cmd, extra_env=None):
    """Stands in for run() in _exit_code_cases(): every command is red, the way
    a tree that does not pass its tests, or a toolchain that will not start in
    the copy, answers the baseline."""
    del dst, extra_env
    return subprocess.CompletedProcess(cmd, 1, "selftest: the copy is red\n", "")


def _exit_code_cases():
    """The code AND the reason of the outcomes #954 moved from 2 to 1.

    Neither is drift: a directory with no spec holds no spec that could have
    drifted, and a copy red before any mutation is the tree or the environment.
    Each case is one outcome, and each asserts the words its branch prints,
    because 1 answers several questions.

    The red copy is driven through main() and replay_all(), the real reader,
    with run() replaced by _red_toolchain() so that this stays offline: what is
    measured is what the harness does with a red baseline, not why it is red.
    Read replay_outcome() by the value again and the replay case reports the
    spec as no longer holding; that is the regression it holds.
    """
    failures = 0

    def check(got, said, want, reason, why):
        if got != want or reason not in said:
            print(f"!! answers {got} on {why}, want {want} saying {reason!r}. It said:")
            for line in said.strip().splitlines()[:4]:
                print(f"   {line}")
            return 1
        print(f"ok  answers {got} on {why}: {reason}")
        return 0

    with tempfile.TemporaryDirectory() as empty:
        for fn, label in ((lint, "--lint"), (replay_all, "--all")):
            said = io.StringIO()
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = fn(empty)
            failures += check(
                got, said.getvalue(), 1, "holds no spec", f"{label} over a directory with no spec"
            )

    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        specs = os.path.join(tmp, "specs")
        os.mkdir(specs)
        spec = _one_spec(
            specs,
            "red-copy.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            globals()["run"] = _red_toolchain
            said, out = io.StringIO(), io.StringIO()
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = main([argv0, spec])
            failures += check(
                got,
                said.getvalue(),
                1,
                "red before any mutation",
                "a single spec whose copy is red before any mutation",
            )
            if isinstance(got, Verdict):
                print("!! a red copy came back as a Verdict: the replay counts it a broken guard")
                failures += 1
            with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(out):
                got = replay_all(specs)
            tally = out.getvalue()
            if got != 1 or "1 of 1 were NOT MEASURED" not in tally or "no longer hold" in tally:
                print(f"!! the replay answers {got} over one red copy and tallies it as:")
                for line in tally.strip().splitlines()[-4:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  the replay answers 1 over a red copy and tallies it NOT MEASURED")

            # The other half, which keeps the first honest: a guard that stopped
            # biting is still a verdict. With every command green, the mutation
            # applies and its test stays green, which is TEST STILL PASSED.
            # Without this case, a last line that lost its Verdict would turn
            # every broken guard into "not measured" and no case would notice.
            globals()["run"] = _green_toolchain
            out = io.StringIO()
            with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(out):
                got = replay_all(specs)
            tally = out.getvalue()
            if got != 1 or "1 of 1 falsifications no longer hold" not in tally:
                print(f"!! the replay answers {got} over a test that stayed green, tallied as:")
                for line in tally.strip().splitlines()[-4:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  the replay answers 1 over a test that stayed green and calls it broken")
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
    return failures


def _unreadable_cases():
    """A malformed spec, and a directory that is not one (#954): code and reason.

    A malformed spec is a fault in the spec, so 2, naming the file, the line
    and the column; and `--all` goes on to the next spec and counts this one as
    not measured, where it used to die on a traceback. A missing directory is
    an error of the call, so 1, said in a sentence instead of a traceback.
    """
    failures = 0

    def check(got, said, want, reason, why):
        if got != want or reason not in said:
            print(f"!! answers {got} on {why}, want {want} saying {reason!r}. It said:")
            for line in said.strip().splitlines()[:4]:
                print(f"   {line}")
            return 1
        print(f"ok  answers {got} on {why}: {reason}")
        return 0

    with tempfile.TemporaryDirectory() as tmp:
        for fn, label in ((lint, "--lint"), (replay_all, "--all")):
            said = io.StringIO()
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = fn(os.path.join(tmp, "nowhere"))
            failures += check(
                got, said.getvalue(), 1, "is not a directory", f"{label} over a missing directory"
            )

        _selftest_module(tmp)
        specs = os.path.join(tmp, "specs")
        os.mkdir(specs)
        with open(os.path.join(specs, "a-broken.json"), "w", encoding="utf-8") as fh:
            fh.write('{\n  "mutations": [\n    {"label": "x",\n  ]\n}\n')
        broken_at = "a-broken.json:4:3: is not valid JSON"
        for name, body, reason, why in (
            ("b-list.json", "[]", "holds a JSON list", "a spec that is a JSON list"),
            (
                "c-shape.json",
                '{"mutations": ["x"]}',
                "is not a list of objects",
                "a mutation that is a string",
            ),
        ):
            with open(os.path.join(specs, name), "w", encoding="utf-8") as fh:
                fh.write(body)
            said = io.StringIO()
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = main([argv0, os.path.join(specs, name)])
            failures += check(got, said.getvalue(), 2, reason, f"a single spec: {why}")
            os.remove(os.path.join(specs, name))

        said = io.StringIO()
        with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
            got = main([argv0, os.path.join(specs, "a-broken.json")])
        failures += check(got, said.getvalue(), 2, broken_at, "a single malformed spec")

        # A live spec AFTER the broken one, in the same directory: the lint
        # must name the broken one and still judge the other, and the replay
        # must reach the other.
        _one_spec(
            specs,
            "z-live.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            said = io.StringIO()
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = lint(specs, tmp)
            failures += check(got, said.getvalue(), 2, broken_at, "--lint over a malformed spec")
            globals()["run"] = _red_toolchain
            out = io.StringIO()
            with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(out):
                got = replay_all(specs)
            tally = out.getvalue()
            want = "2 of 2 were NOT MEASURED: a-broken.json, z-live.json"
            if got != 1 or want not in tally:
                print(f"!! the replay answers {got} past a malformed spec, tallied as:")
                for line in tally.strip().splitlines()[-4:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  the replay goes past a malformed spec and counts it NOT MEASURED")
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)

        # A mutation that does not build, end to end: `go vet` red, everything
        # else green. Not a Verdict, and the reason says so.
        def vet_red(dst, cmd, extra_env=None):
            del dst, extra_env
            code = 1 if cmd[:2] == ["go", "vet"] else 0
            return subprocess.CompletedProcess(cmd, code, "selftest\n", "selftest: vet\n")

        try:
            os.chdir(tmp)
            globals()["run"] = vet_red
            said = io.StringIO()
            with contextlib.redirect_stderr(said), contextlib.redirect_stdout(io.StringIO()):
                got = main([argv0, os.path.join(specs, "z-live.json")])
            failures += check(
                got,
                said.getvalue(),
                1,
                "did not build",
                "a mutation that does not build",
            )
            if isinstance(got, Verdict):
                print("!! a mutation that did not build came back as a Verdict")
                failures += 1
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
    return failures


def _spec_outcome_cases():
    """One row per question spec_outcome() asks, the reason asserted where one is given.

    The broken restore is held here and not end to end: making a restore fail
    for real needs a file the harness cannot write, which takes the run down
    rather than through this branch.
    """
    bit = ("a", "the test bit", "yes")
    still = ("b", "TEST STILL PASSED", "yes")
    void = ("c", "DID NOT COMPILE (void)", "no")
    absent = ("d", "DID NOT APPLY", "-")
    skipped = ("e", SKIPPED, "yes")
    hung = ("f", HUNG, "yes")
    failures = 0
    for verdicts, broken, want, typed, reason, why in (
        ([bit], [], 0, True, None, "every mutation bit: a verdict, green"),
        ([bit, still], [], 1, True, None, "a test stayed green: a verdict, red"),
        ([bit, void], [], 1, False, "did not build", "a mutation did not build"),
        ([bit, absent], [], 1, False, "did not apply", "a mutation did not apply"),
        ([bit, skipped], [], 1, False, "its test was skipped", "a test skipped or never ran"),
        ([bit, hung], [], 1, False, "hung until the timeout", "a test that hung (#965)"),
        ([bit], ["p/x.go"], 1, False, "the restore broke (p/x.go)", "the restore broke"),
        ([still, void], [], 1, True, None, "a measured green test outranks a void sibling"),
        ([still], ["p/x.go"], 1, False, "the restore broke", "a broken restore outranks a verdict"),
    ):
        code, said = spec_outcome(verdicts, broken)
        ok = code == want and isinstance(code, Verdict) == typed
        ok = ok and (
            reason is None and said is None or reason is not None and reason in (said or "")
        )
        if not ok:
            print(f"!! spec_outcome answers ({code!r}, {said!r}) on {why}")
            failures += 1
        else:
            print(f"ok  spec_outcome: {why}")
    return failures


def _green_toolchain(dst, cmd, extra_env=None):
    """Stands in for run() in _exit_code_cases(): every command is green, so a
    mutation applies, compiles, and its test does not notice. The test run
    says the test passed, the way `go test -v` does, since a green run that
    names no test is not a verdict (passed_leaves)."""
    del dst, extra_env
    said = "--- PASS: TestTheGuardHolds (0.00s)\nPASS\n" if "-run" in cmd else ""
    return subprocess.CompletedProcess(cmd, 0, said + "selftest: green\n", "")


def _skipping_toolchain(dst, cmd, extra_env=None):
    """Like _green_toolchain(), but the test the mutation names calls t.Skip."""
    del dst, extra_env
    said = "--- SKIP: TestTheGuardHolds (0.00s)\nPASS\n" if "-run" in cmd else ""
    return subprocess.CompletedProcess(cmd, 0, said, "")


def _loose_toolchain(dst, cmd, extra_env=None):
    """Stands in for run() with Go's selection: `-run` is searched, unanchored,
    in TestLoose and TestLooseButStrict. TestLooseButStrict catches the
    mutation and TestLoose does not, so the run is red whenever the pattern
    reaches the neighbour."""
    del dst, extra_env
    if "-run" not in cmd:
        return subprocess.CompletedProcess(cmd, 0, "", "")
    pattern = cmd[cmd.index("-run") + 1]
    if re.search(pattern, "TestLooseButStrict"):
        return subprocess.CompletedProcess(cmd, 1, "--- FAIL: TestLooseButStrict (0.00s)\n", "")
    return subprocess.CompletedProcess(cmd, 0, "--- PASS: TestLoose (0.00s)\nPASS\n", "")


def _driven(fn, *args):
    """(answer, stdout, stderr) of fn(*args), with both streams captured."""
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        got = fn(*args)
    return got, out.getvalue(), err.getvalue()


def _skipped_cases():
    """A green run whose named test never ran to a PASS is not a verdict.

    passed_leaves() row by row, then end to end through main() and replay_all()
    with run() replaced: a test that calls t.Skip must come back a plain 1 with
    its reason, tallied NOT MEASURED, where it used to be TEST STILL PASSED and
    "no longer hold". _exit_code_cases() holds the accepting half: the same run
    reporting a PASS is still a Verdict.
    """
    failures = 0
    for out, pattern, want, why in (
        ("--- PASS: TestX (0.00s)\n", "^TestX$", ["TestX"], "a test that passed is measured"),
        ("--- SKIP: TestX (0.00s)\n", "^TestX$", [], "a test that skipped is not"),
        ("", "^TestX$", [], "a run that names no test is not"),
        (
            "--- PASS: TestX (0.00s)\n    --- SKIP: TestX/sub (0.00s)\n",
            "^TestX$",
            [],
            "a parent whose only subtest skipped measured nothing",
        ),
        (
            "--- PASS: TestXY (0.00s)\n",
            "^TestX$",
            [],
            "a neighbour the pattern does not select is not the test",
        ),
        (
            "--- SKIP: TestX (0.00s)\n--- PASS: TestX (0.00s)\n",
            "^TestX$",
            ["TestX"],
            "one PASS among -count runs is enough",
        ),
    ):
        got = passed_leaves(pattern, out)
        if got != want:
            print(f"!! passed_leaves answers {got} where {why}")
            failures += 1
        else:
            print(f"ok  passed_leaves: {why}")

    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        specs = os.path.join(tmp, "specs")
        os.mkdir(specs)
        spec = _one_spec(
            specs,
            "skipped.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            globals()["run"] = _skipping_toolchain
            got, _, said = _driven(main, [argv0, spec])
            if got != 1 or isinstance(got, Verdict) or "its test was skipped" not in said:
                print(f"!! a skipped test answers {got!r} ({type(got).__name__}), saying: {said!r}")
                failures += 1
            else:
                print("ok  a skipped test answers a plain 1: its test was skipped")
            got, tally, _ = _driven(replay_all, specs)
            if got != 1 or "1 of 1 were NOT MEASURED" not in tally or "no longer hold" in tally:
                print(f"!! the replay answers {got} over a skipped test and tallies it as:")
                for line in tally.strip().splitlines()[-4:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  the replay tallies a skipped test NOT MEASURED, not broken")
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
    return failures


def _anchoring_cases():
    """A declared name selects that test and no neighbour (anchored()).

    Row by row, then through the replay, where it decides a verdict: TestLoose
    does not catch the mutation and TestLooseButStrict does, so an unanchored
    `-run TestLoose` reports "the test bit" and the spec exits 0 having proved
    nothing about TestLoose. Anchored, the spec is a Verdict(1), TEST STILL
    PASSED, which is the truth about TestLoose. The lint half is the
    "prefix.json" case of lint_selftest().
    """
    failures = 0
    for pattern, want, why in (
        ("TestX", "^TestX$", "a plain name is anchored"),
        ("TestX/named-sub", "^TestX$/^named-sub$", "each level of a plain name is anchored"),
        ("^TestX$", "^TestX$", "an anchored name is left as written"),
        ("TestX/a|TestX/b", "TestX/a|TestX/b", "an alternation across levels is left as written"),
        ("TestA|TestB", "TestA|TestB", "an alternation is left as written"),
    ):
        got = anchored(pattern)
        if got != want:
            print(f"!! anchored({pattern!r}) is {got!r}: {why}")
            failures += 1
        else:
            print(f"ok  anchored: {why}")

    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        specs = os.path.join(tmp, "specs")
        os.mkdir(specs)
        spec = _one_spec(
            specs,
            "loose.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestLoose",
        )
        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            globals()["run"] = _loose_toolchain
            got, out, _ = _driven(main, [argv0, spec])
            if not isinstance(got, Verdict) or got != 1 or "TEST STILL PASSED" not in out:
                print(
                    f"!! a name its neighbour extends answers {got!r}: the neighbour's bite counted"
                )
                failures += 1
            else:
                print("ok  a name its neighbour extends is replayed alone: TEST STILL PASSED")
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
    return failures


def _field_type_cases():
    """One case per type question field_problem() asks, through main() and
    then through `--all`, which must go on to the next spec (#954)."""
    failures = 0
    live = {
        "label": "selftest typed",
        "file": "p/subject.go",
        "find": "func guard() bool { return true }",
        "replace": "func guard() bool { return true && false }",
        "test": "TestTheGuardHolds",
    }
    cases = (
        (
            "a-repeat.json",
            {"package": "./p/", "mutations": [{**live, "repeat": "deux"}]},
            'mutation 1: `repeat` is "deux"',
            "a repeat that is a word",
        ),
        (
            "b-env.json",
            {"package": "./p/", "env": {"X": 1}, "mutations": [live]},
            'the spec: `env` is {"X": 1}',
            "an env value that is a number",
        ),
        (
            "c-test.json",
            {"package": "./p/", "mutations": [{**live, "test": 5}]},
            "mutation 1: `test` is 5, not a string",
            "a test that is a number",
        ),
        (
            "d-flag.json",
            {"package": "./p/", "repeat": True, "mutations": [live]},
            "the spec: `repeat` is true",
            "a repeat that is a boolean",
        ),
    )
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        specs = os.path.join(tmp, "specs")
        os.mkdir(specs)
        for name, body, _reason, _why in cases:
            with open(os.path.join(specs, name), "w", encoding="utf-8") as fh:
                json.dump(body, fh)
        with open(os.path.join(specs, "z-live.json"), "w", encoding="utf-8") as fh:
            json.dump({"package": "./p/", "repeat": 2, "env": {"X": "1"}, "mutations": [live]}, fh)
        _, why = load_spec(os.path.join(specs, "z-live.json"))
        if why:
            print(f"!! a spec with a whole repeat and a text env is refused: {why}")
            failures += 1
        else:
            print("ok  a spec with a whole repeat and a text env is read")
        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            globals()["run"] = _green_toolchain
            for name, _, reason, why in cases:
                got, _, said = _driven(main, [argv0, os.path.join(specs, name)])
                if got != 2 or reason not in said:
                    print(f"!! answers {got} on {why}, want 2 saying {reason!r}. It said: {said!r}")
                    failures += 1
                else:
                    print(f"ok  answers 2 on {why}: {reason}")
            got, tally, _ = _driven(replay_all, specs)
            want = "4 of 5 were NOT MEASURED"
            if got != 1 or want not in tally or "no longer hold: z-live.json" not in tally:
                print(f"!! the replay answers {got} past four mistyped specs, tallied as:")
                for line in tally.strip().splitlines()[-4:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  the replay goes past four mistyped specs and still judges the fifth")
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
    return failures


# The sentence selftest() prints, built in one place so the pages can quote it.
HISTORY_SENTENCE = "the rule refuses all {n} historical mistakes, accepts all {n} fixes"

NUMBER_WORDS = {
    "en": ["zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"],
    "fr": ["zéro", "un", "deux", "trois", "quatre", "cinq", "six", "sept", "huit", "neuf"],
}

# Every place that publishes len(HISTORY), and the sentences it carries it in
# (#949). Each must be found exactly once in its page, with the live number.
HISTORY_SITES = [
    ("CONTRIBUTING.md", "en", "*" + HISTORY_SENTENCE + "*"),
    ("CONTRIBUTING.md", "en", "The {word} of this section is the number the task prints"),
    ("CONTRIBUTING.fr.md", "fr", "*" + HISTORY_SENTENCE + "*"),
    ("CONTRIBUTING.fr.md", "fr", "Le « {word} » de cette section est le chiffre que la tâche"),
]

# What HISTORY_SITES must hold, written a second time on purpose: the same
# guard as TestTheFigureCheckNamesEveryPlaceThatPublishesASpecCount for the Go
# list of #937. A site dropped from HISTORY_SITES stops being judged and nothing
# else would say so; _published_history_cases() refuses the difference.
EXPECTED_HISTORY_PAGES = (
    ("CONTRIBUTING.md", "en"),
    ("CONTRIBUTING.md", "en"),
    ("CONTRIBUTING.fr.md", "fr"),
    ("CONTRIBUTING.fr.md", "fr"),
)

REPOSITORY = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))


def published_history_problems(pages, n, sites=None):
    """Every site that does not publish `n` as the number of historical mistakes (#949).

    Both CONTRIBUTING pages say how many mistakes the selftest holds, and the
    number comes from len(HISTORY). It was hand-written, and it was wrong once:
    the pages said three while the task printed four, long enough for an audit
    to find it (#937). Recounting it from Go would mean reading a Python list
    from Go, which is one more instrument that can lie; so the owner of the
    list checks the pages instead, from the same constant it prints.

    `pages` maps a page name to its text, or to None when it could not be read.
    Three outcomes, never two: a site found once with the live number passes; a
    page that could not be read, a number with no word to spell it, and a
    sentence found zero or several times are each a problem, never a pass. The
    empty list therefore means every declared sentence was read.
    """
    sites = HISTORY_SITES if sites is None else sites
    if not sites:
        return ["no site declared: a check over no sentence is not a verdict"]
    problems = []
    for page, lang, template in sites:
        text = pages.get(page)
        if text is None:
            problems.append(f"{page}: cannot be read, so its count of {n} was not judged")
            continue
        words = NUMBER_WORDS.get(lang)
        if words is None or not 0 <= n < len(words):
            problems.append(f"{page}: no {lang!r} word for {n}, so the sentence was not judged")
            continue
        want = template.format(n=n, word=words[n])
        found = " ".join(text.split()).count(want)
        if found != 1:
            problems.append(
                f"{page}: carries {want!r} {found} time(s), want exactly 1 — HISTORY holds {n} "
                "mistakes and the page must say so, in this exact sentence"
            )
    return problems


def _published_history_cases():
    """The pages say how many mistakes HISTORY holds, and say it right (#949).

    One planted case per question the check asks, each wrong in that one way
    only, then the accepting half, then the real pages.
    """
    failures = 0
    en = "*" + HISTORY_SENTENCE.format(n=4) + "*. The four of this section is the number"
    en += " the task prints."
    sites = [
        ("A.md", "en", "*" + HISTORY_SENTENCE + "*"),
        ("A.md", "en", "The {word} of this section is the number the task prints"),
    ]
    for pages, n, want, why in (
        ({"A.md": en}, 4, None, "a page carrying the live number in digits and in words"),
        ({"A.md": en.replace("all 4", "all 3")}, 4, "carries", "a stale digit"),
        ({"A.md": en.replace("The four", "The three")}, 4, "carries", "a stale word"),
        ({"A.md": en + " " + en}, 4, "2 time(s)", "a sentence published twice"),
        ({"A.md": None}, 4, "cannot be read", "a page that could not be read"),
        ({"A.md": en}, 12, "no 'en' word", "a number the page has no word for"),
    ):
        got = published_history_problems(pages, n, sites)
        if want is None and got:
            print(f"!! the published count is refused on {why}: {got}")
            failures += 1
        elif want is not None and not any(want in g for g in got):
            print(f"!! the published count reads {got} on {why}, want a problem saying {want!r}")
            failures += 1
        else:
            verdict = "is accepted" if want is None else "is refused"
            print(f"ok  the published count {verdict} on {why}")
    if published_history_problems({}, 4, []) == []:
        print("!! a check over no declared site answers nothing, which reads as a pass")
        failures += 1
    else:
        print("ok  a check over no declared site is refused")

    declared = tuple((page, lang) for page, lang, _ in HISTORY_SITES)
    if declared != EXPECTED_HISTORY_PAGES:
        print(
            f"!! HISTORY_SITES declares {declared}, want {EXPECTED_HISTORY_PAGES}: "
            "a site removed from the list is a sentence nobody judges any more"
        )
        failures += 1
    else:
        print(f"ok  HISTORY_SITES still declares the {len(EXPECTED_HISTORY_PAGES)} expected sites")

    pages = {}
    for page in sorted({site[0] for site in HISTORY_SITES}):
        try:
            with open(os.path.join(REPOSITORY, page), encoding="utf-8") as fh:
                pages[page] = fh.read()
        except OSError:
            pages[page] = None
    real = published_history_problems(pages, len(HISTORY))
    for problem in real:
        print(f"!! {problem}")
    if not real:
        print(
            f"ok  both CONTRIBUTING pages publish the {len(HISTORY)} of HISTORY, digits and words"
        )
    return failures + len(real)


def _replay_reporting_cases():
    """The two mistakes of 2026-09-14, held here so they cannot come back.

    The replay used to add two different outcomes together. A spec whose copy was
    red before any mutation measured NOTHING, and was counted beside a spec whose
    guard genuinely stopped biting. On a station where `go` is a mise shim that
    refuses to start in an untrusted copy, that arithmetic printed
    "199 of 199 falsifications no longer hold": every guard in the repository,
    reported broken, by a harness that had just said, 199 times, that it was
    measuring nothing.
    """
    failures = 0

    # An absence of measurement must not be spelled as a verdict. The two lists
    # are built from the same exit codes the replay reads, so folding the
    # branches back together fails this. The second row is #954: the red copy
    # answers the same number as a guard that stopped biting, and only the
    # type tells them apart.
    for code, want, why in (
        (Verdict(1), "stopped biting", "a mutation applied, its test still green: a verdict"),
        (1, "not measured", "a copy red before any mutation measured nothing, at 1"),
        (2, "not measured", "a refused spec measured no guard either"),
        (Verdict(0), "bit", "a mutation that bit is neither failed nor unmeasured"),
    ):
        got = replay_outcome(code)
        if got != want:
            print(f"!! exit {code} reads as {got!r}, want {want!r}: {why}")
            failures += 1
        else:
            print(f"ok  {why}")

    # The copy has to be trusted or the toolchain never starts in it. Read off
    # run() itself, so the selftest stays offline and instant; `--all` is what
    # exercises it against a real copy.
    source = inspect.getsource(run)
    if '"MISE_TRUSTED_CONFIG_PATHS": dst' not in source:
        print("!! run() does not declare the copy trusted; a mise shim refuses to start in it")
        failures += 1
    else:
        print("ok  run() declares the copy trusted, so the toolchain starts in it")

    return failures


def _budget_cases():
    """The two measurements the replay stopped repeating, and what keeps them honest (#890).

    The night was cancelled at its 210-minute ceiling on 2026-11-12, and the
    cost was not where it looked. Instrumented on one spec: the copy took 0.04 s
    and the two full suites took 45 of the 53 seconds. Across a replay those are
    242 baselines and 242 restorations for some fifteen distinct questions —
    sixty specs name ./internal/cli/ and each ran its 11-second suite twice.

    Measured on eight ./internal/cli/ specs, 54 mutations, every one still
    biting: 441 s before, 181 s after.

    Both shortcuts are only sound under a condition, and these cases hold the
    conditions rather than the speed.
    """
    failures = 0

    # A baseline answer may be reused only for the same question. The packages
    # are the obvious half; the environment is the half that bites, because
    # `env` arms tests that otherwise skip and a skip counts as a pass. Sharing
    # one answer across two environments would report a suite as green that
    # never ran in that environment.
    for a, b, same, why in (
        (
            (["./internal/cli/"], {}),
            (["./internal/cli/"], {}),
            True,
            "one package, no env: the same question",
        ),
        (
            (["./internal/cli/"], {}),
            (["./internal/s3/"], {}),
            False,
            "two packages are two questions",
        ),
        (
            (["./internal/cli/"], {}),
            (["./internal/cli/"], {"FEINT_TESTCONTAINER": "1"}),
            False,
            "an env that arms a skipped suite is a different question",
        ),
        (
            (["./internal/cli/"], {"A": "1", "B": "2"}),
            (["./internal/cli/"], {"B": "2", "A": "1"}),
            True,
            "the same env in another order is the same question",
        ),
        (
            (["./a/", "./b/"], {}),
            (["./b/", "./a/"], {}),
            False,
            "the list is taken as given, so a caller that sorts makes two orders meet",
        ),
    ):
        got = baseline_key(*a) == baseline_key(*b)
        if got != same:
            print(f"!! baseline_key says same={got} for {why}, want {same}")
            failures += 1
        else:
            print(f"ok  {why}")

    # The restoration is now read off the bytes, so it has to NAME a file that
    # did not come back. A check that always answers "nothing" would make every
    # replay green on a broken restore, which is worse than the suite it
    # replaces.
    with tempfile.TemporaryDirectory() as tmp:
        kept, lost = "kept.go", "lost.go"
        with open(os.path.join(tmp, kept), "w", encoding="utf-8") as fh:
            fh.write("package p\n")
        with open(os.path.join(tmp, lost), "w", encoding="utf-8") as fh:
            fh.write("package p // mutated and never put back\n")
        pristine = {kept: "package p\n", lost: "package p\n"}

        got = unrestored(tmp, pristine)
        if got != [lost]:
            print(
                f"!! unrestored answers {got}, want [{lost!r}]: "
                "a restore that did not happen is not named"
            )
            failures += 1
        else:
            print(f"ok  a file that did not come back is named: {got}")

        # And the accepting half: a check that named everything would redden
        # every replay and be just as useless.
        got = unrestored(tmp, {kept: "package p\n"})
        if got != []:
            print(f"!! unrestored answers {got} for a file that did come back, want []")
            failures += 1
        else:
            print("ok  a file that came back byte for byte is not named")

    return failures


def _unwrapped_toolchain(dst, cmd, extra_env=None):
    """Stands in for run() in _hang_cases(): the same command, run by the real
    Go toolchain, without the systemd-run scope around it, which a CI runner
    may have no user session to give. What this measures is what Go prints and
    what main() makes of it, not the memory cap.

    Bounded on its own, at 60 s: a declared timeout that never reaches `go
    test` would leave the hang case waiting Go's ten minutes, which is the
    hang it exists to name. Killed, it answers 124 with no panic line, main()
    reads that as "the test bit", and the case fails saying so.
    """
    env = {**os.environ, "MISE_TRUSTED_CONFIG_PATHS": dst, **(extra_env or {})}
    try:
        return subprocess.run(cmd, cwd=dst, capture_output=True, text=True, env=env, timeout=60)
    except subprocess.TimeoutExpired:
        return subprocess.CompletedProcess(cmd, 124, "selftest: killed after 60 s\n", "")


def _timing_out_toolchain(seen):
    """A stand-in for run() whose every test run times out, the way Go reports
    it, after the -timeout the command carries or after Go's own default when
    it carries none. Each test command is appended to `seen`."""

    def toolchain(dst, cmd, extra_env=None):
        del dst, extra_env
        if "-run" not in cmd:
            return subprocess.CompletedProcess(cmd, 0, "", "")
        seen.append(cmd)
        after = cmd[cmd.index("-timeout") + 1] if "-timeout" in cmd else "10m0s"
        out = (
            "=== RUN   TestTheGuardHolds\n"
            f"panic: test timed out after {after}\n"
            f"\trunning tests:\n\t\tTestTheGuardHolds ({after})\n"
        )
        return subprocess.CompletedProcess(cmd, 1, out, "")

    return toolchain


def _hang_cases():
    """A test that hangs is not a test that bites (#965). One case per question.

    Before #965 the replay ran `go test` without -timeout and read every
    non-zero exit as "the test bit", so a mutation that made its test hang
    printed `ok` after Go's ten minutes, exactly like one that made it fail at
    once with a reason. The questions:

      - is a declared timeout a duration `go test` can be handed (the lint,
        and main() before the copy): one case per refusal, and the accepting
        half on a mutation and on a spec;
      - is the timeout line recognised, and only at the start of a line;
      - does the declared timeout reach `go test`, the mutation's winning
        over the spec's, and none at all when neither declares one;
      - is a hang HUNG whether or not a timeout was declared, a plain 1, and
        tallied NOT MEASURED by the replay;
      - end to end, through a real `go test`: a mutation that hangs under a
        1 s timeout is HUNG, and one that fails at once under the same
        timeout is still "the test bit".
    """
    failures = 0
    live = {
        "label": "selftest timed",
        "file": "p/subject.go",
        "find": "func guard() bool { return true }",
        "replace": "func guard() bool { return true && false }",
        "test": "TestTheGuardHolds",
    }
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)

        def spec_at(name, spec_timeout=None, mutation=None):
            directory = os.path.join(tmp, name.removesuffix(".json"))
            os.mkdir(directory)
            body = {"package": "./p/", "mutations": [{**live, **(mutation or {})}]}
            if spec_timeout is not None:
                body["timeout"] = spec_timeout
            path = os.path.join(directory, name)
            with open(path, "w", encoding="utf-8") as fh:
                json.dump(body, fh)
            return directory, path

        for name, spec_timeout, mutation, want, why, reason in (
            ("t-mutation.json", None, {"timeout": "20s"}, 0, "a mutation timeout of 20s", None),
            ("t-spec.json", "1m30s", None, 0, "a spec timeout of 1m30s", None),
            (
                "t-number.json",
                None,
                {"timeout": 20},
                2,
                "a timeout that is a JSON number",
                "`timeout` is 20, not a string",
            ),
            (
                "t-unitless.json",
                None,
                {"timeout": "20"},
                2,
                "a timeout with no unit",
                '`timeout` is "20", not a Go duration',
            ),
            (
                "t-zero.json",
                "0s",
                None,
                2,
                "a timeout of zero, which disables the bound",
                "a timeout of zero disables the bound",
            ),
        ):
            directory, _ = spec_at(name, spec_timeout, mutation)
            got, said = _linted(directory, tmp)
            failures += _report(got, want, why, said, reason)

        _, garbage = spec_at("t-garbage.json", None, {"timeout": "soon"})
        here = os.getcwd()
        try:
            os.chdir(tmp)
            got, out, said = _driven(main, [argv0, garbage])
        finally:
            os.chdir(here)
        reason = '`timeout` is "soon", not a Go duration'
        if got != 2 or reason not in said or "copying" in out:
            print(f"!! a single spec with timeout 'soon' answers {got}, saying {said!r}")
            failures += 1
        else:
            print(f"ok  a single spec answers 2 before any copy: {reason}")

        # The tail go1.26.11 printed on 2026-11-15 for a TestMain sleeping past
        # `-timeout 1s`, copied from a real run (61 s, exit 1). Captured rather
        # than replayed here: go waits the timeout plus one minute before it
        # kills, and the harness's own spec runs this selftest once per
        # mutation, so a live 61 s case would cost over an hour per replay.
        # The live half is the 1 s `select {}` run below, same code path.
        killed = (
            "fs     0x0\ngs     0x0\n*** Test killed with quit: ran too long (1m1s).\n"
            "FAIL\tk/p\t61.029s\nFAIL\n"
        )
        for out, err, want, why in (
            (
                "=== RUN   TestX\npanic: test timed out after 1s\n\trunning tests:\n",
                "",
                "panic: test timed out after 1s",
                "the line Go prints on stdout is a hang",
            ),
            (
                "",
                "panic: test timed out after 2m0s\n",
                "panic: test timed out after 2m0s",
                "the same line on stderr too",
            ),
            (
                "=== RUN   TestX\n....panic: test timed out after 1s\n",
                "",
                "panic: test timed out after 1s",
                "the line after characters a test printed without a newline",
            ),
            (
                killed,
                "",
                "*** Test killed with quit: ran too long (1m1s).",
                "go killing a TestMain that blocks before m.Run, captured from a real run",
            ),
            (
                "*** Test killed: ran too long (1m1s).\nFAIL\n",
                "",
                "*** Test killed: ran too long (1m1s).",
                "the same kill without a signal trace, as go's source writes it",
            ),
            (
                "    x_test.go:9: panic: test timed out after 1s\n--- FAIL: TestX (0.00s)\n",
                "",
                "",
                "a test that logs those words is not a hang",
            ),
            (
                "    x_test.go:9: *** Test killed: ran too long (1m1s).\n--- FAIL: TestX (0.00s)\n",
                "",
                "",
                "a test that logs the kill line is not a hang either",
            ),
            (
                '    x_test.go:9: waited for "panic: test timed out after 1s"\n--- FAIL: TestX\n',
                "",
                "",
                "a test that quotes the words with %q is a bite, its line ending in a quote",
            ),
            (
                "*** Test killed: ran too long (forever).\nFAIL\n",
                "",
                "",
                "a kill line whose duration is not a Go duration is not go's",
            ),
            ("--- FAIL: TestX (0.00s)\nFAIL\n", "", "", "a plain failure is not a hang"),
        ):
            got = hung_after(out, err)
            if got != want:
                print(f"!! hung_after answers {got!r} where {why}")
                failures += 1
            else:
                print(f"ok  hung_after: {why}")

        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            for name, spec_timeout, mutation, want, why in (
                ("r-spec.json", "3s", None, ["-timeout", "3s"], "the spec's timeout"),
                ("r-both.json", "3s", {"timeout": "1s"}, ["-timeout", "1s"], "the mutation's wins"),
                ("r-neither.json", None, None, [], "none declared, none passed"),
            ):
                _, path = spec_at(name, spec_timeout, mutation)
                seen = []
                globals()["run"] = _timing_out_toolchain(seen)
                got, out, said = _driven(main, [argv0, path])
                cmd = seen[0] if seen else []
                bound = (
                    cmd[cmd.index("-timeout") : cmd.index("-timeout") + 2]
                    if "-timeout" in cmd
                    else []
                )
                after = want[1] if want else "10m0s"
                if bound != want:
                    print(f"!! go test is handed {bound} for {why}, want {want}")
                    failures += 1
                elif got != 1 or isinstance(got, Verdict) or HUNG not in out:
                    print(f"!! a run that timed out answers {got!r} for {why}, printing {out!r}")
                    failures += 1
                elif f"timed out after {after}" not in out or "hung until" not in said:
                    print(f"!! a hang after {after} is not named so for {why}: {out!r} {said!r}")
                    failures += 1
                else:
                    print(f"ok  {why}: go test gets {bound or 'no -timeout'}, and the run is HUNG")
                BASELINES.clear()

            # Only a non-zero exit can be a hang. A test that passes and
            # prints the words (here at the start of a line) is read for its
            # PASS lines like any green run, so a guard that stopped biting is
            # still TEST STILL PASSED, a Verdict, and not hidden as void.
            def quoting(dst, cmd, extra_env=None):
                del dst, extra_env
                said = (
                    "panic: test timed out after 1s\n--- PASS: TestTheGuardHolds (0.00s)\nPASS\n"
                    if "-run" in cmd
                    else ""
                )
                return subprocess.CompletedProcess(cmd, 0, said, "")

            _, path = spec_at("r-zero.json")
            globals()["run"] = quoting
            got, out, _ = _driven(main, [argv0, path])
            if got != 1 or not isinstance(got, Verdict) or "TEST STILL PASSED" not in out:
                print(f"!! a green run printing the timeout words answers {got!r}: {out!r}")
                failures += 1
            else:
                print("ok  a green run that prints the timeout words is TEST STILL PASSED")
            BASELINES.clear()

            globals()["run"] = _timing_out_toolchain([])
            directory = os.path.join(tmp, "r-neither")
            got, tally, _ = _driven(replay_all, directory)
            if got != 1 or "1 of 1 were NOT MEASURED" not in tally or "no longer hold" in tally:
                print(f"!! the replay answers {got} over a hang and tallies it as:")
                for line in tally.strip().splitlines()[-4:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  the replay tallies a hang NOT MEASURED, neither bitten nor broken")

            globals()["run"] = _unwrapped_toolchain
            _, hang = spec_at(
                "e-hang.json",
                mutation={"replace": "func guard() bool { select {} }", "timeout": "1s"},
            )
            got, out, said = _driven(main, [argv0, hang])
            if (
                got != 1
                or isinstance(got, Verdict)
                or HUNG not in out
                or "timed out after 1s" not in out
                or VOID_WHY[HUNG] not in said
            ):
                print(f"!! a real hang under a 1s timeout answers {got!r}. It printed:")
                for line in (out + said).strip().splitlines()[-6:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  a real `go test` that hangs past its 1s timeout is HUNG, a plain 1")
            _, bite = spec_at("e-bite.json", mutation={"timeout": "1s"})
            got, out, said = _driven(main, [argv0, bite])
            if got != 0 or not isinstance(got, Verdict) or "the test bit" not in out:
                print(f"!! a real test that fails at once under a 1s timeout answers {got!r}:")
                for line in (out + said).strip().splitlines()[-6:]:
                    print(f"   {line}")
                failures += 1
            else:
                print("ok  a real `go test` that fails at once under the same timeout still bit")
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
    return failures


# Where _survivors() reads the process table. A constant so that the macOS
# path, which has none, can be exercised on Linux by pointing it elsewhere.
PROC_ROOT = "/proc"


def _group_left(pgid):
    """Whether any process of the group `pgid` is still there after the kill.

    `os.killpg(pgid, 0)` sends nothing and asks the kernel whether the group
    has a member: ProcessLookupError means none, on Linux and macOS alike. A
    member we may not signal (PermissionError) is still a member. Asked over
    two seconds at most: a process killed by SIGKILL may still be on its way
    out, and a zombie counts until its parent reaps it.
    """
    for _ in range(20):
        try:
            os.killpg(pgid, 0)
        except ProcessLookupError:
            return False
        except PermissionError:
            pass
        time.sleep(0.1)
    return True


def _survivors(mark):
    """The pids still carrying FALSIFY_SELFTEST_MARK=mark, other than this one,
    or None when /proc cannot be read (an unknown, never an empty answer).

    Read twice over two seconds at most: a process killed by SIGKILL may still
    be on its way out when the first look happens.
    """
    if not os.path.isdir(PROC_ROOT):
        return None
    needle = f"FALSIFY_SELFTEST_MARK={mark}\0".encode()
    left = []
    for _ in range(20):
        left = []
        for pid in (p for p in os.listdir(PROC_ROOT) if p.isdigit()):
            if int(pid) == os.getpid():
                continue
            try:
                with open(os.path.join(PROC_ROOT, pid, "environ"), "rb") as fh:
                    if needle in fh.read():
                        left.append(int(pid))
            except OSError:
                continue
        if not left:
            return []
        time.sleep(0.1)
    return left


def _lint_bound_cases():
    """A lint call that hangs is killed and named, never waited out. One case per call.

    Each case is a real module and a real `go test`, with the bound lowered
    to 1 s. The hang is a 20 s sleep rather than `select {}`, so that a
    harness without its bound waits 20 s and then fails on the wrong reason,
    instead of waiting Go's ten minutes: the listing then completes and the
    names are found (lint 0), the parent then passes without its subtest
    (lint 2, "no subtest of it matches").

      - the listing: a package whose TestMain sleeps before m.Run, which
        `go test -list` runs and `go test -timeout` would not bound;
      - the parent run: a parent that sleeps before its t.Run.

    The accepting half is every other lint case, which runs under the
    default bounds and must stay green.
    """
    failures = 0
    if not hasattr(os, "killpg"):
        # Not a pass: said, and counted as nothing. bounded_go() kills with
        # os.killpg, so on such a platform the lint has no kill path to test
        # at all. CI runs this selftest on Linux and macOS only (go.yml).
        print("skip the lint bound cases: no os.killpg here, so bounded_go() cannot kill at all")
        return 0

    # The group check's witness, both halves, because a check for absence
    # that never finds anything reads exactly like one that cannot: a live
    # group of our own must be reported present, and gone once killed.
    #
    # A group whose LEADER has exited and whose MEMBER stays, which is the
    # shape bounded_go() leaves when it kills: `go` dies, the test binary under
    # it is what may survive. With a one-process group, os.kill(pgid, 0) asks
    # the leader alone and answers exactly like os.killpg(pgid, 0), so that
    # mutation kept the selftest green (falsifier, PR #973).
    witness = subprocess.Popen(["sh", "-c", "sleep 30 & exit 0"], start_new_session=True)
    witness.wait()
    try:
        present = _group_left(witness.pid)
    finally:
        with contextlib.suppress(ProcessLookupError):
            os.killpg(witness.pid, signal.SIGKILL)
    gone = not _group_left(witness.pid)
    if present and gone:
        print("ok  the group check sees a group whose leader exited, and no longer once killed")
    else:
        print(f"!! the group check answers present={present}, gone={gone} on a group of its own")
        failures += 1
    sleeping_main = (
        'package s\n\nimport (\n\t"os"\n\t"testing"\n\t"time"\n)\n\n'
        "func TestMain(m *testing.M) {\n\ttime.Sleep(20 * time.Second)\n\tos.Exit(m.Run())\n}\n\n"
        "func TestListedLate(t *testing.T) {}\n"
    )
    sleeping_parent = (
        'package p\n\nimport (\n\t"testing"\n\t"time"\n)\n\n'
        "func TestTheSleepingParent(t *testing.T) {\n\ttime.Sleep(20 * time.Second)\n"
        '\tt.Run("sub", func(t *testing.T) {})\n}\n'
    )
    for package, test, bound, files, why in (
        (
            "./s/",
            "TestListedLate",
            "LIST_BOUND",
            {"s/main_test.go": sleeping_main, "s/s.go": "package s\n"},
            "a listing whose TestMain hangs",
        ),
        (
            "./p/",
            "TestTheSleepingParent/sub",
            "PARENT_BOUND",
            {"p/sleeping_test.go": sleeping_parent},
            "a parent run that hangs before its t.Run",
        ),
    ):
        with tempfile.TemporaryDirectory() as tmp:
            _selftest_module(tmp)
            for name, body in files.items():
                os.makedirs(os.path.join(tmp, os.path.dirname(name)), exist_ok=True)
                with open(os.path.join(tmp, name), "w", encoding="utf-8") as fh:
                    fh.write(body)
            directory = os.path.join(tmp, "specs")
            os.mkdir(directory)
            _one_spec(
                directory,
                "bound.json",
                package=package,
                file="p/subject.go",
                find="func guard() bool { return true }",
                test=test,
            )
            saved = globals()[bound]
            globals()[bound] = 1
            mark = f"{os.getpid()}-{bound}"
            os.environ["FALSIFY_SELFTEST_MARK"] = mark
            KILLED_GROUPS.clear()
            start = time.monotonic()
            try:
                got, said = _linted(directory, tmp)
            finally:
                globals()[bound] = saved
                del os.environ["FALSIFY_SELFTEST_MARK"]
            took = time.monotonic() - start
            # Two survivor checks, and the first is the one every POSIX system
            # answers: the group bounded_go() killed has no member left. It was
            # the /proc scan alone until CI ran this selftest on macos-15,
            # which has no /proc, and failed both cases there as "unknown"
            # (PR #973). The scan stays where /proc is readable, because it
            # also catches a process that left the group.
            if not KILLED_GROUPS:
                print(f"!! {why}: no process group was killed, so the bound never fired")
                failures += 1
            for pgid in KILLED_GROUPS:
                if _group_left(pgid):
                    print(f"!! {why}: the killed group {pgid} still has a process running")
                    failures += 1
                else:
                    print(f"ok  {why}: the killed group {pgid} has no process left")
            left = _survivors(mark)
            if left is None:
                print(
                    f"ok  {why}: no /proc here: a process that left the killed group (setsid) "
                    "is not seen; the group check above is the only one"
                )
            elif left:
                print(f"!! {why}: the killed call left {len(left)} process(es) running: {left}")
                failures += 1
            else:
                print(f"ok  {why}: no process of the killed call survives it, by /proc")
            failures += _report(got, 2, why, said, "did not finish within 1 s, the lint's bound")
            # Killed, not waited out: a kill that reaches `go` but not the
            # test binary under it leaves the pipes open for the full sleep.
            if took >= 15:
                print(
                    f"!! {why}: the lint took {took:.1f} s, so the hang was waited out, not killed"
                )
                failures += 1
            else:
                print(f"ok  {why}: killed after {took:.1f} s, not waited out")
    return failures


def _red_baseline_cases():
    """A red baseline names what made it red, wherever it sits (#967).

    Row by row on red_baseline_report(), one question each, then through
    main() with a real `go test` over a module whose first test fails and
    whose next test prints 4000 characters: the `--- FAIL` line then sits
    beyond the last 2500 characters, which is all the harness used to print.
    The case asserts that too, so it cannot pass on output too short to
    have hidden anything.
    """
    failures = 0
    pad = "x" * 3000
    # (stdout, stderr, lines that must be NAMED, a line that must not be, the
    # words that must reach the tails, why). Named lines are looked for in the
    # named section only: the tails repeat the whole output, and a check over
    # everything would find an indented line there that was never named.
    for stdout, stderr, named, absent, tail, why in (
        (
            f"--- FAIL: TestEarly (0.00s)\n{pad}\nFAIL\tm/p\t0.1s\n",
            "",
            ["  --- FAIL: TestEarly (0.00s)", "  FAIL\tm/p\t0.1s"],
            None,
            None,
            "a failing test far from the end is named, and its package",
        ),
        (
            "    --- FAIL: TestA/sub (0.00s)\n",
            "",
            ["  --- FAIL: TestA/sub (0.00s)"],
            None,
            None,
            "a failing subtest is named",
        ),
        (
            f"{pad}\npanic: test timed out after 1s\n\trunning tests:\n",
            "",
            ["  panic: test timed out after 1s"],
            None,
            None,
            "a panic is named, the timeout's included",
        ),
        (
            "",
            "# m/p\np/x.go:3:1: syntax error\n",
            [],
            None,
            "p/x.go:3:1: syntax error",
            "the stderr tail is printed, and a build failure there is read",
        ),
        (
            "        --- FAIL: TestA/sub/deeper (0.00s)\n",
            "",
            ["  --- FAIL: TestA/sub/deeper (0.00s)"],
            None,
            None,
            "a failing subtest two levels down is named",
        ),
        (
            "fatal error: concurrent map writes\n\ngoroutine 7 [running]:\nFAIL\tm/p\t0.1s\n",
            "",
            ["  fatal error: concurrent map writes"],
            None,
            None,
            "a fatal runtime error is named, which prints no --- FAIL line",
        ),
        (
            "",
            "--- FAIL: TestOnStderr (0.00s)\n",
            ["  --- FAIL: TestOnStderr (0.00s)"],
            None,
            None,
            "a failure line on stderr is named too",
        ),
        (
            "flag provided but not defined: -nope\nUsage of p.test:\n",
            "",
            ["(no line names a test, a package or a panic"],
            None,
            "flag provided but not defined: -nope",
            "the stdout tail is printed, the only clue when no line names a failure",
        ),
        (
            "    x_test.go:9: --- FAIL: written by a test\nok\n",
            "",
            ["(no line names a test, a package or a panic"],
            "x_test.go:9",
            None,
            "a test that logs those words names nothing",
        ),
    ):
        got = red_baseline_report(stdout, stderr)
        head = got.split("stdout, last")[0]
        missing = [w for w in named if w not in head]
        if missing or (absent and absent in head) or (tail and tail not in got):
            print(f"!! red_baseline_report: {why}. It printed: {got[:300]!r}")
            failures += 1
        else:
            print(f"ok  red_baseline_report: {why}")

    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp)
        with open(os.path.join(tmp, "p", "red_test.go"), "w", encoding="utf-8") as fh:
            fh.write(
                'package p\n\nimport (\n\t"fmt"\n\t"strings"\n\t"testing"\n)\n\n'
                "func TestTheBaselineFailsEarly(t *testing.T) {\n"
                '\tt.Fatal("red on purpose, before a chatty neighbour")\n}\n\n'
                "func TestTheBaselineChattyNeighbour(t *testing.T) {\n"
                '\tfmt.Println(strings.Repeat("flag usage text ", 250))\n}\n'
            )
        spec = _one_spec(
            tmp,
            "red-baseline.json",
            package="./p/",
            file="p/subject.go",
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        seen = []

        def recording(dst, cmd, extra_env=None):
            res = _unwrapped_toolchain(dst, cmd, extra_env)
            seen.append(res)
            return res

        here, real_run = os.getcwd(), globals()["run"]
        try:
            os.chdir(tmp)
            globals()["run"] = recording
            got, _, said = _driven(main, [argv0, spec])
        finally:
            globals()["run"] = real_run
            BASELINES.clear()
            os.chdir(here)
        line = "--- FAIL: TestTheBaselineFailsEarly"
        stdout = seen[0].stdout if seen else ""
        if line not in stdout or line in stdout[-2500:]:
            print(
                f"!! the red module's FAIL line is not beyond the last 2500 characters: {stdout!r}"
            )
            failures += 1
        elif got != 1 or isinstance(got, Verdict) or line not in said:
            print(f"!! a red baseline answers {got!r} and does not name its test. It said:")
            for text in said.strip().splitlines()[:6]:
                print(f"   {text}")
            failures += 1
        else:
            print("ok  a red baseline names its failing test, beyond the old 2500-character tail")
    return failures


def load_spec(path):
    """(spec, None), or (None, why) when `path` does not hold a spec this harness can read.

    A malformed spec used to reach json.load() bare: `--lint` and `--all` died
    on a traceback, and `--all` stopped at that spec without replaying the ones
    after it (#954). The fault is in a committed spec, so the caller refuses it
    with 2 and names the file, the line and the column. A document that parses
    and is not the shape a spec has is refused the same way, rather than
    failing later on an AttributeError: an unrecognised shape must refuse.
    """
    try:
        with open(path, encoding="utf-8") as fh:
            spec = json.load(fh)
    except json.JSONDecodeError as err:
        return None, f"{path}:{err.lineno}:{err.colno}: is not valid JSON ({err.msg})"
    except UnicodeDecodeError as err:
        return None, f"{path}: is not UTF-8 text ({err.reason} at byte {err.start})"
    mutations = spec.get("mutations") if isinstance(spec, dict) else None
    if not isinstance(spec, dict):
        return None, f"{path}: holds a JSON {type(spec).__name__}, not the object a spec is"
    if mutations is not None and not (
        isinstance(mutations, list) and all(isinstance(m, dict) for m in mutations)
    ):
        return None, f"{path}: `mutations` is not a list of objects"
    holders = [("the spec", spec)]
    holders += [(f"mutation {i + 1}", m) for i, m in enumerate(mutations or [])]
    for where, holder in holders:
        why = field_problem(holder)
        if why:
            return None, f"{path}: {where}: {why}"
    return spec, None


# The fields read as text. A number in one of them reached `in` or `+` and
# died on a TypeError.
STRING_FIELDS = ("label", "file", "find", "replace", "test", "package")


def field_problem(holder):
    """Why one object of a spec (the spec itself or a mutation) has a field of
    the wrong type, or None.

    `"repeat": "deux"` reached int() in the middle of a replay and `"env":
    {"X": 1}` reached subprocess as an environment value: a ValueError and a
    TypeError, each a traceback that killed `--all` at that spec (measured by
    a falsifier on 2026-11-15). The fault is in the spec, so it is refused
    with 2 before anything is copied, and `--all` goes on to the next spec.
    _field_type_cases() asks each question with its own case.
    """
    for key in STRING_FIELDS:
        if key in holder and not isinstance(holder[key], str):
            return f"`{key}` is {json.dumps(holder[key])}, not a string"
    if "repeat" in holder:
        repeat = holder["repeat"]
        if isinstance(repeat, bool) or not isinstance(repeat, int) or repeat < 1:
            return f"`repeat` is {json.dumps(repeat)}, not a whole number of runs, at least 1"
    if "env" in holder:
        env = holder["env"]
        if not isinstance(env, dict) or not all(isinstance(v, str) for v in env.values()):
            return f"`env` is {json.dumps(env)}, not an object whose every value is a string"
    if "timeout" in holder:
        why = timeout_problem(holder["timeout"])
        if why:
            return why
    return None


# A Go duration as `go test -timeout` reads it (time.ParseDuration), without the
# sign: one or more number-and-unit terms, `1m30s`, `1.5s`, `500ms`. A bare
# number is refused although Go accepts "0": a timeout of zero DISABLES the
# bound, which is the opposite of what declaring one asks for.
GO_DURATION_TERM = re.compile(r"(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)")
GO_DURATION_UNIT = {
    "ns": 1e-9,
    "us": 1e-6,
    "µs": 1e-6,
    "ms": 1e-3,
    "s": 1.0,
    "m": 60.0,
    "h": 3600.0,
}


def timeout_problem(value):
    """Why `value` is not a timeout this harness can hand to `go test`, or None (#965).

    Refused rather than passed through, because `go test -timeout soon` does
    not fail in a way the replay could read: it prints its usage and exits 2,
    and the replay counts a non-zero exit as a run of the test. Three
    questions, one case each in _hang_cases(): is it a string, is it a
    duration Go reads, is it longer than zero.
    """
    if not isinstance(value, str):
        return f'`timeout` is {json.dumps(value)}, not a string such as "20s"'
    if not value or GO_DURATION_TERM.sub("", value) != "":
        return (
            f'`timeout` is {json.dumps(value)}, not a Go duration such as "20s" or "1m30s" '
            "(a number and a unit, ns us ms s m h)"
        )
    seconds = sum(float(n) * GO_DURATION_UNIT[u] for n, u in GO_DURATION_TERM.findall(value))
    if seconds <= 0:
        return f"`timeout` is {json.dumps(value)}, and a timeout of zero disables the bound"
    return None


def not_a_directory(directory):
    """The reason `directory` cannot hold specs, or None. It was a
    FileNotFoundError traceback (#954): exit 1 already, and unreadable."""
    if os.path.isdir(directory):
        return None
    return f"{directory}: is not a directory, so there is no spec to read"


def every_spec(directory):
    """Every spec in the directory, sorted, so two runs report the same order."""
    return sorted(
        os.path.join(directory, name) for name in os.listdir(directory) if name.endswith(".json")
    )


def path_problem(target, root="."):
    """Why a mutation's `file` is not written the one way every reader compares it, or None (#950).

    Three consumers read `file` as a plain string and none of them normalises
    it: `tools/testplan/specs.go` claims a spec for a diff by `changed[m.File]`,
    the figure check of #937 counts distinct files by `files[m.File]`, and the
    replay joins it under its copy. So `./x.go`, `a/../x.go` and `x.go` are one
    file to the compiler and three to the repository: a spec spelled `./x.go`
    is NEVER claimed by `mise run testplan` for a diff that touches `x.go`, and
    the deserved replay drops out of the plan without a word. Measured on
    2026-11-14 on a copy of the 254 specs: one spec naming
    `./tools/testplan/figures.go`, then one naming
    `tools/falsify/../testplan/figures.go`, moved the distinct-file count from
    296 to 297 to 298 for one file.

    An absolute path is worse than unclaimed: `os.path.join(dst, "/abs")` is
    `/abs`, so a single-spec run would rewrite the file in the working
    repository rather than in its copy.

    One check here, at intake, rather than one per consumer. lint_selftest()'s
    "dotted", "climbing" and "absolute" cases fail without it, each with a
    fragment that is alive and a test that exists, so nothing else refuses them.

    A path through a symbolic link is refused for a fourth reason: copy_tree()
    keeps links as links (`symlinks=True`), so a link to an absolute directory
    points out of the copy, and the mutation is written into the WORKING file.
    The replay then measures the unmutated copy and prints TEST STILL PASSED
    about a guard it never removed, and the working tree keeps the mutation.
    So the path must resolve to itself under `root`; lint_selftest()'s
    "symlinked" case fails without this check.
    """
    if not target or not isinstance(target, str):
        return None  # no file at all: fragment_problem() and main() say so
    if os.path.isabs(target):
        return (
            f"{target}: is absolute; write it relative to the repository root, which is the "
            "spelling `mise run testplan` compares with a diff"
        )
    normal = os.path.normpath(target)
    if normal == os.pardir or normal.startswith(os.pardir + os.sep):
        return f"{target}: leaves the repository, so no diff of this tree can ever touch it"
    if normal != target:
        return (
            f"{target}: is not written in its normal form {normal!r}; `mise run testplan` "
            "compares the path with a diff as written, so this spec would never be claimed "
            "for a change to that file"
        )
    # Before the link question: `.` resolves to the root without its `/.`,
    # and was refused as "goes through a symbolic link", which it does not.
    here = os.path.join(root, target)
    if os.path.exists(here) and not os.path.isfile(here):
        return f"{target}: is not a regular file, so there is no fragment to rewrite"
    joined = os.path.join(os.path.realpath(root), target)
    real = os.path.realpath(joined)
    if real != joined:
        return (
            f"{target}: goes through a symbolic link to {real}; the copy keeps the link, "
            "so the mutation would be written outside the copy"
        )
    return None


def fragment_problem(target, find, root="."):
    """Why this fragment cannot be rewritten, or None.

    Two answers, and the second is the one that cost #399. The harness rewrites
    the FIRST occurrence only (`body.replace(find, replace, 1)`), because a
    mutation is meant to name one place. A fragment that matches several places
    therefore neutralises one of them and leaves the rest standing — and the
    guard stays green for a reason that has nothing to do with the guard.

    Measured: `shared-layer-is-enforced.json` names
    `storetest.NoLostUpdate(40, func(trial int) []storetest.Write {`, which was
    one call site in internal/providers/exoscale/lostupdate_test.go on the day
    the spec was written and became three two days later. The mutation kept
    renaming the first, `TestEveryPackRunsTheSharedBarrage` kept reading the
    other two, and the falsification reported STILL GREEN for two months while
    the guard it names was never actually removed. `--lint` could not see it:
    the fragment was still there, so "every declared fragment still applies" was
    true and useless.

    So the fragment must match exactly once. It is the same class as the dead
    fragment above — a spec describing code that is no longer what it thinks —
    and it is checked in the same millisecond.

    `target` is read under `root`, the same tree the Go half of the lint lists
    its packages in (#938). It used to be opened against the process's working
    directory while `go test -list` ran under `root`: one argument, two trees.
    A selftest case naming its subject relatively then answered 2 through "no
    such file" and never reached the check it was written for, and was green
    under the very mutant it existed to catch — twice, while #923 was written.
    lint_selftest() names every subject relatively since, so reading `target`
    against the working directory again turns its accepting cases red and its
    refusing cases into the wrong reason.
    """
    if not target:
        return f"{target}: no such file"
    path = os.path.join(root, target)
    if not os.path.exists(path):
        return f"{target}: no such file"
    with open(path, encoding="utf-8") as fh:
        found = fh.read().count(find)
    if found == 0:
        return f"{target}: fragment not found"
    if found > 1:
        return (
            f"{target}: fragment matches {found} places and only the first is rewritten, "
            f"so the mutation leaves {found - 1} of them standing"
        )
    return None


# What `go test -list` answered for a set of package patterns, for the life of
# this process. See listed_tests().
LISTINGS = {}


# A `-run` pattern is split on `/` into one element per test level, and only the
# first names a top-level test — which is the only level `go test -list` prints.
# Go's own splitRegexp keeps a `/` inside brackets or parentheses, so this does
# too: `TestA/[a/b]` is one element, `TestA/sub` is two.
def run_elements(pattern):
    out, depth, start = [], 0, 0
    for i, ch in enumerate(pattern):
        if ch in "[(":
            depth += 1
        elif ch in "])":
            depth = max(0, depth - 1)
        elif ch == "/" and depth == 0:
            out.append(pattern[start:i])
            start = i + 1
    out.append(pattern[start:])
    return out


def top_level_pattern(pattern):
    return run_elements(pattern)[0]


def names_a_subtest(pattern):
    return len(run_elements(pattern)) > 1


# The `-v` lines that say a test really ran, at whatever depth: `    --- PASS:
# TestA/sub (0.00s)`. SKIP counts as ran: a subtest that skips exists, and the
# question here is existence, not outcome.
RAN = re.compile(r"^\s*--- (?:PASS|FAIL|SKIP): (\S+)")

# The characters that make a `-run` element a regular expression rather than a
# name. An element holding none of them is a name.
REGEX_META = frozenset("\\.^$*+?()[]{}|")


def anchored(pattern):
    """The `-run` pattern a declared test name is handed to `go test` as.

    `-run` is an unanchored regular expression, element by element, so `go test
    -run TestLoose` also runs TestLooseButStrict. A mutation TestLoose does not
    catch then reads "the test bit" because its neighbour failed, and the spec
    exits 0 having proved nothing about the test it names: measured by a
    falsifier on 2026-11-15. So a declared name whose every element is a plain
    name is anchored element by element (`^TestX$/^sub$`). A pattern holding
    any regular-expression character is the author's own and goes through
    unchanged, because an alternation across levels (`TestX/a|TestX/b`, which
    committed specs use) means something else once each element is anchored.

    Both the lint and the replay ask with this, so a name that is only the
    prefix of a test is refused by the lint rather than replayed against its
    neighbour. Measured before it was wired, on 2026-11-15 at bd56053 plus this
    lot: `mise run falsify:lint` over the 257 committed specs answers 0 with
    the anchoring and with it removed, so no committed name relied on a prefix.
    _anchoring_cases() fails without it, through the lint and through the replay.
    """
    elems = run_elements(pattern)
    if any(not e or REGEX_META & set(e) for e in elems):
        return pattern
    return "/".join(f"^{e}$" for e in elems)


# A `-v` result line, with its outcome.
RESULT = re.compile(r"^\s*--- (PASS|FAIL|SKIP): (\S+)")


def passed_leaves(pattern, stdout):
    """The tests `-run pattern` selected that ran to a PASS, deepest level only.

    A green `go test` is a verdict only if the test it names ran. It exits 0
    when that test calls t.Skip, and when nothing was selected at all, and the
    replay read both as TEST STILL PASSED, so `--all` said "no longer hold" over
    a test that measured nothing (measured by a falsifier on 2026-11-15). A
    parent whose every selected subtest skipped measured nothing either, so
    only the deepest selected names count. With -count=N each name is printed N
    times, and one PASS is enough.
    """
    status = {}
    for line in stdout.splitlines():
        m = RESULT.match(line)
        if m and selects(pattern, m.group(2)) and status.get(m.group(2)) != "PASS":
            status[m.group(2)] = m.group(1)
    return sorted(
        name
        for name, outcome in status.items()
        if outcome == "PASS" and not any(other.startswith(name + "/") for other in status)
    )


# How long the lint lets one `go` call run before it kills it (found beside
# #965). Without a bound, a parent test that hangs, or
# a TestMain that hangs before m.Run, held `falsify:lint`, and so `prepush`,
# for Go's ten minutes. Measured on 2026-11-15 on Stéphane's station over
# the 259 committed specs, through the pinned interpreter with
# GOMAXPROCS=4 GOFLAGS=-p=2 (scratchpad time_lint.py wrapping every call):
# the one listing took 2.12 s warm and 19.50 s with an empty GOCACHE, which
# is what a CI job meets after `check` built everything with -race; the
# slowest parent run took 0.47 s warm and cold alike. The bounds are about
# nine times the cold listing and a hundred times the slowest parent run, so
# a slow runner is not mistaken for a hang. _lint_bound_cases() lowers them
# to one second and drives a real hang through each call.
LIST_BOUND = 180
PARENT_BOUND = 60

# The process groups bounded_go() killed, in order: with start_new_session the
# group id is the child's pid. _lint_bound_cases() asks each one afterwards
# whether any member is left, the one survivor check every POSIX system can
# answer (macOS has no /proc, and CI runs this selftest on macos-15).
KILLED_GROUPS = []


def bounded_go(cmd, root, env, seconds):
    """(CompletedProcess, why it was killed or "") of `cmd`, killed after `seconds`.

    Its own session, and the whole group killed: `go test` runs the test
    binary as a child, and killing `go` alone returns at once (go relays the
    child's output, so the pipes close with it) but leaves the hung binary
    running, measured on 2026-11-15 by the selftest's first version. A TestMain
    that hangs before m.Run is never stopped by Go's own alarm either, which
    starts in m.Run; so the group goes, and _lint_bound_cases() requires that
    no process of the call survives it.
    """
    proc = subprocess.Popen(
        cmd,
        cwd=root,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        env=env,
        start_new_session=True,
    )
    try:
        out, err = proc.communicate(timeout=seconds)
        return subprocess.CompletedProcess(cmd, proc.returncode, out, err), ""
    except subprocess.TimeoutExpired:
        os.killpg(proc.pid, signal.SIGKILL)
        KILLED_GROUPS.append(proc.pid)
        out, err = proc.communicate()
        why = (
            f"`{' '.join(cmd)}` did not finish within {seconds} s, the lint's bound, and was "
            "killed: a test or a TestMain that hangs, not a stale name"
        )
        return subprocess.CompletedProcess(cmd, -signal.SIGKILL, out or "", err or ""), why


def ran_under(parent, pkg, root, env=()):
    """The full names `go test -run <parent> -v <pkg>` entered, and why it could
    not be trusted to say, or "".

    `go test -list` prints top-level tests ONLY, so it cannot answer anything
    about a subtest. This is the measurement that can, and it is the same shape
    of question as the listing: run the parent once, verbosely, and read which
    names were entered.

    Measured on 2026-11-14 on Stéphane's station, the three distinct (package,
    parent) pairs the committed specs name: 0.28 s, 0.38 s and 0.28 s, so 0.9 s
    for the whole subtest half of the lint. That is why it is here and not only
    in the replay.

    The exit code is read here although it is not the measurement, and that is
    the same line listed_tests() draws: a parent that panics before reaching its
    `t.Run` enters no subtest and exits non-zero, so "no subtest matches" would
    blame the spec for a tree that could not answer. A parent whose subtests
    merely FAIL also exits non-zero, and its `--- FAIL:` lines name them, so the
    exit code is reported as a doubt and never as an absence.
    _parent_cannot_answer_case() fails without it.

    `env` arms a parent that otherwise skips, and a skipped parent enters no
    subtest: the spec's own `env` has to reach this measurement exactly as it
    reaches run() in the replay, or a valid spec combining `env` and a subtest
    is refused. lint_selftest()'s "env-armed.json" case fails without it.
    """
    key = ("subtests", parent, pkg, os.path.abspath(root), tuple(env))
    if key in LISTINGS:
        return LISTINGS[key]
    res, late = bounded_go(
        ["go", "test", "-count=1", "-run", parent, "-v", pkg],
        root,
        {**os.environ, **dict(env)},
        PARENT_BOUND,
    )
    names = {m.group(1) for m in (RAN.match(line) for line in res.stdout.splitlines()) if m}
    doubt = ""
    if late:
        doubt = late
    elif res.returncode != 0:
        tail = (res.stdout + res.stderr).strip().splitlines()
        doubt = f"`go test -run {parent} -v {pkg}` exited non-zero" + (
            f": {tail[-1].strip()}" if tail else ""
        )
    LISTINGS[key] = (names, doubt)
    return LISTINGS[key]


def selects(pattern, full):
    """Would `-run pattern` select the test whose full name is `full`?

    Two readings, and both are accepted because `go test` accepts both. Go
    matches element i of the pattern against level i of the name, which is what
    `^TestX$/foreign-name$` means. But a pattern carrying an alternation across
    levels — `TestX/a|TestX/b`, which eight committed mutations do — splits into
    three elements that match nothing element-wise, and still runs, because the
    second element `a|TestX` matches `a`. Reading the whole name as one regex
    covers that one, so a valid declaration is not refused.
    """
    elems = run_elements(pattern)
    levels = full.split("/")
    try:
        if len(levels) >= len(elems) and all(
            re.search(e, lv) for e, lv in zip(elems, levels, strict=False)
        ):
            return True
        return bool(re.search(pattern, full))
    except re.error:
        return False


def module_path(root):
    """The module path of the tree, read off go.mod rather than guessed."""
    with open(os.path.join(root, "go.mod"), encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("module "):
                return line.split()[1]
    return ""


def listed_tests(packages, root, env=()):
    """{import path: {top-level test names}} for these package patterns.

    ONE `go test -list` for every pattern a run names, not one per mutation.
    Measured on 2026-11-14, warm build cache, on the 1665 mutations committed
    here: 36 distinct patterns cost 3.1 s grouped (3.9 s for the whole lint)
    against 12.6 s at one call per pattern, and a call per mutation would be a
    different order of magnitude entirely. Cold, the
    cost is whatever compiling those test binaries costs, which `check` has
    already paid in prepush.

    `-list` compiles the test binary but runs nothing, so no mutation is applied
    and no subject is touched. The exit code is NOT the measurement: `go test
    -run NoSuchTest` exits 0 over a package holding no such test, which is the
    defect this whole function exists to close (#923). The listing is.

    Answers are kept for the life of the process, on the same argument as
    BASELINES: `--all` asks this once per spec, against a tree that does not
    change while the replay runs. Counted on 2026-11-14 at 703915c (#951): the
    36 patterns above are what the lint lists in its one call, and `--all`
    meets 70 distinct (package set, env) pairs, one listing each. This
    docstring said "some fifteen" there, a figure carried over from the
    baselines of #890 and contradicted by its own first paragraph. The
    night was already cancelled at its 210-minute ceiling once (#890), and a
    check that closes a hole must not reopen that one.
    """
    key = ("list", tuple(packages), os.path.abspath(root), tuple(env))
    if key in LISTINGS:
        return LISTINGS[key]
    res, late = bounded_go(
        ["go", "test", "-list", ".*", *packages],
        root,
        {**os.environ, **dict(env)},
        LIST_BOUND,
    )
    by_path, pending = {}, []
    for line in res.stdout.splitlines():
        head = line.split("\t")
        # `ok \t<path>\t0.001s`, `? \t<path>\t[no test files]`, `FAIL\t<path>…`:
        # the names of a package are printed before the line that closes it.
        if head[0].rstrip() in {"ok", "?", "FAIL"} and len(head) > 1:
            by_path.setdefault(head[1].strip(), set()).update(pending)
            pending = []
        elif line.strip():
            pending.append(line.strip())
    doubt = res.stderr.strip() if res.returncode != 0 else ""
    LISTINGS[key] = (by_path, late or doubt)
    return LISTINGS[key]


def covered_paths(pattern, module, known):
    """The import paths a spec's package pattern covers, among those listed.

    This is the load-bearing half of the check, and the reason is #923 itself:
    a-transport-fault-is-bounded.json declared ./internal/core/emulator/ while
    the test it named lived in internal/cli. "Does this name exist anywhere in
    the listing" would have accepted it. The question is whether it exists in
    THIS package.

    _cross_package_case() fails without this filter: replace the body with
    `return set(known)` and it answers 0 instead of 2.
    """
    p = pattern.strip().removeprefix("./").rstrip("/")
    if p.endswith("..."):
        base = (module + "/" + p[: -len("...")]).rstrip("/")
        return {k for k in known if k == base or k.startswith(base + "/")}
    full = module if p in ("", ".") else module + "/" + p
    return {k for k in known if k == full}


def tests_that_do_not_exist(entries, root="."):
    """The declared tests `go test` would run nothing for (#923).

    `go test -run <name> <pkg>` over a package holding no such test runs nothing
    and exits 0, and the harness read that zero as "the guard is not held": it
    printed TEST STILL PASSED, a verdict over an empty population, in the
    instrument this repository built to catch exactly that. Measured on
    2026-11-13 on a-transport-fault-is-bounded.json, whose declared package was
    ./internal/core/emulator/ while the test it named lived in internal/cli: run
    by hand against the package that holds it, the same mutation failed in
    5.02 s. The guard was held all along.

    The silent half is the one this closes for good: a mutation whose test was
    RENAMED keeps the old name, matches nothing, and reports TEST STILL PASSED
    forever — until somebody "fixes" it by deleting the mutation, and the guard
    is left with no falsification and nothing saying so.

    `entries` are (origin, label, test, package, env), where env is the spec's
    own `env` as a tuple of sorted pairs. The answer is a list of
    (origin, label, test, package, why), empty when every name is found.

    The env is part of the question and not a detail, for the reason
    baseline_key() already states: it arms tests that otherwise skip, so two
    specs over one package under two environments are two questions. Entries are
    therefore grouped by env, and each group is measured on its own.

    lint_selftest()'s "renamed.json" case fails without this function: it
    points the lint at a live fragment whose test has been renamed, and requires
    2 rather than 0.
    """
    if not entries:
        return []
    groups = {}
    for entry in entries:
        groups.setdefault(entry[4], []).append(entry)
    out = []
    for env, group in groups.items():
        out += _checked_under(group, env, root)
    return out


def _checked_under(entries, env, root):
    """tests_that_do_not_exist() for the entries sharing one environment."""
    packages = sorted({pkg for _, _, _, pkg, _ in entries})
    by_path, err = listed_tests(packages, root, env)
    module = module_path(root)
    missing = []
    for origin, label, test, pkg, _ in entries:
        names = set()
        for path in covered_paths(pkg, module, by_path):
            names |= by_path[path]
        head = top_level_pattern(anchored(test))
        try:
            rx = re.compile(head)
        except re.error as exc:
            # Not a void verdict but a FALSE one, which is worse: `go test -run
            # 'TestX('` rejects the pattern and exits non-zero, the replay reads
            # non-zero as "the test bit", and the harness prints `ok` for a
            # mutation it never ran. lint_selftest()'s "unparsable.json" case
            # fails without this branch.
            missing.append((origin, label, test, pkg, f"{test!r} is not a usable pattern: {exc}"))
            continue
        if not any(rx.search(name) for name in names):
            missing.append(
                (
                    origin,
                    label,
                    test,
                    pkg,
                    f"{pkg} holds no test matching {test!r}, so `go test -run` there runs "
                    f"nothing and exits 0"
                    + (
                        "\n      (and the listing is INCOMPLETE: `go test -list` exited "
                        "non-zero, so this may be the tree failing to build rather than the "
                        f"name being stale: {err.splitlines()[0] if err else ''})"
                        if err
                        else ""
                    ),
                )
            )
    # And the same question one level down, for the names that passed the
    # listing. `go test -run TestX/no-such-sub` runs TestX, no subtest matches,
    # the parent passes, and the run exits 0 — so the replay prints
    # TEST STILL PASSED for a mutation that measured nothing, which is #923
    # exactly, one level down. Measured on 2026-11-14 against this harness:
    # TestTheGuardHolds/named-sub gave "the test bit",
    # TestTheGuardHolds/no-such-sub gave TEST STILL PASSED.
    #
    # Counted on 2026-11-14: 13 committed mutations name a subtest, across three
    # (package, parent) pairs. Eight of the 13 are rows of one table in
    # s3-bucket-configuration.json and name nine of its rows between them, one
    # mutation covering two by alternation — so renaming one row of that table
    # is the single gesture that would silence a mutation with nothing to say
    # so.
    #
    # lint_selftest()'s "no-such-sub" case fails without this block.
    already = {(m[1], m[2], m[3]) for m in missing}
    for origin, label, test, pkg, _ in [] if err else entries:
        if not names_a_subtest(test) or (label, test, pkg) in already:
            continue  # no subtest named, or its top level is already named above
        parent = top_level_pattern(anchored(test))
        entered, doubt = ran_under(parent, pkg, root, env)
        if any(selects(anchored(test), full) for full in entered):
            continue
        missing.append(
            (
                origin,
                label,
                test,
                pkg,
                (
                    f"{pkg} runs {parent!r}, but no subtest of it matches {test!r}: the "
                    f"parent then passes and `go test` exits 0, having measured nothing"
                )
                if not doubt
                else (
                    f"{pkg} entered no subtest matching {test!r}, and no subtest was really "
                    f"confirmed either way:\n      {doubt}"
                ),
            )
        )

    # A listing that could not be taken answers nothing, and "nothing" must not
    # read as "every name was found". Saying it here keeps the two apart: a tree
    # that does not build is the harness's problem, not the spec's.
    #
    # _unbuildable_case() fails without this block: one broken package among
    # several, `go test -list` exits non-zero, the other packages' names are
    # printed, and every declared name reads as found over a listing nobody can
    # trust.
    if err and not missing:
        origin, label, test, pkg, _ = entries[0]
        missing.append(
            (
                origin,
                label,
                test,
                pkg,
                "`go test -list` exited non-zero for "
                + " ".join(packages)
                + ", so no name was really confirmed:\n      "
                + (err.splitlines()[0] if err.splitlines() else err),
            )
        )
    return missing


def entries_of(spec, origin):
    """(origin, label, test, package, env) for every mutation of one spec.

    The env travels with the entry because the measurement needs it: a spec
    declares `env` for a subject whose tests skip without it, the replay already
    hands it to run(), and a check that did not would refuse a valid spec whose
    subtest only exists when the parent is armed.
    """
    default = spec.get("package", "./...")
    env = tuple(sorted((spec.get("env") or {}).items()))
    return [
        (
            origin,
            m.get("label", "?"),
            m.get("test") or "",
            m.get("package", default),
            env,
        )
        for m in spec.get("mutations") or []
        if m.get("test")
    ]


def lint(directory, root="."):
    """Every declared fragment still exists in the file it names, and every
    declared test still exists in the package its mutation names.

    The cheap half of `--all`, and the half that catches the failure `--all`
    catches twelve hours late. Replaying a spec copies the tree and runs `go
    test` once per mutation, so it lives on a nightly cron: declaring one more
    mutation must never be what makes pull requests slower. But a fragment that
    no longer matches any code is not a slow question — it is a string search,
    and the answer is the same in a millisecond as in eight minutes.

    It has already cost this repository twice, both found on 17 August 2026 and
    neither by the people who caused them:

      - transition.json stopped applying when #211 reduced Transition to a
        wrapper around Observe;
      - serialise-then-commit.json stopped applying the same afternoon, when
        #257 merged the two NIC doors and `server.ID` became `serverID`.

    Between the change and the nightly run, `falsify:all` was red and the tree
    looked green to everyone working in it. A dead spec is worse than a missing
    one: `falsify:all` prints DID NOT APPLY, the count of proven guards silently
    drops, and the specs directory keeps reading like a register of proofs.

    So this runs in prepush, where it costs seconds.

    Since #923 it asks a second question of the same family, at two depths, and
    the reason both belong here is the same: a name that selects no test is
    answered in seconds rather than in eight minutes, and the lint is where a
    stale name is cheapest to catch — `--all` would otherwise print
    TEST STILL PASSED for it, twelve hours later, and blame the guard.

      - a top-level name, from one `go test -list` over every package pattern
        the specs name;
      - a subtest name, from one `go test -run <parent> -v <pkg>` per distinct
        (package, parent) pair, because `-list` never prints a subtest.

    Measured on 2026-11-14 on Stéphane's station, 251 specs and 1665 mutations,
    13 of them naming a subtest: 2.5 s to 2.6 s through `mise run falsify:lint`,
    2.8 s to 2.9 s through a bare `python3`, of which the three parent runs are
    0.9 s. Beside it, after #950 the same day, one run through
    `mise run falsify:lint` with GOMAXPROCS=4 GOFLAGS=-p=2 and 19 subtest
    names: 4.6 s. The two are not the same condition, and the gap is not
    explained.

    `root` is the tree the packages are resolved in; the selftest points it at a
    module of its own so this function is driven rather than imitated.
    """
    absent = not_a_directory(directory)
    if absent:
        return refuse(absent, code=1)
    specs = every_spec(directory)
    if not specs:
        return refuse(f"{directory} holds no spec, so this checked nothing", code=1)

    stale, entries, malformed, count = [], [], [], 0
    for path in specs:
        spec, why = load_spec(path)
        if why:
            malformed.append(why)
            continue
        count += len(spec.get("mutations") or [])
        entries += entries_of(spec, path)
        for m in spec.get("mutations") or []:
            why = path_problem(m.get("file"), root) or fragment_problem(
                m.get("file"), m.get("find"), root
            )
            if why:
                stale.append((path, m.get("label", "?"), why))

    if malformed:
        print(
            f"falsify: {len(malformed)} spec(s) cannot be read, so none of their mutations "
            "is measured:",
            file=sys.stderr,
        )
        for why in malformed:
            print(f"  {why}", file=sys.stderr)
    if stale:
        print(
            f"falsify: {len(stale)} declared mutation(s) no longer apply. The code moved and "
            "the spec did not, so these guards have stopped being measured:",
            file=sys.stderr,
        )
        for path, label, why in stale:
            print(f"  {os.path.basename(path)}: {why}\n      {label}", file=sys.stderr)
        print(
            "\nRetarget the fragment at the code that carries the guard today, or delete the "
            "mutation and say in the spec why it no longer has a subject.",
            file=sys.stderr,
        )
    if malformed or stale:
        return 2

    # The fourth state, and it is neither of the three the replay already draws.
    # A copy red before any mutation measured no guard; a refused spec measured
    # no guard either; a mutation that applied and left its test green is a
    # verdict. This one is none of them: the mutation was never applied and the
    # test was never run, because the name belongs to no test in that package.
    missing = tests_that_do_not_exist(entries, root)
    if missing:
        print(
            f"falsify: {len(missing)} declared mutation(s) NAME A TEST THAT DOES NOT EXIST in "
            "the package they declare. Nothing would be run for them, and `go test` exits 0 "
            "over an empty set of tests, so the replay would call each of these guards "
            "unguarded without ever measuring one:",
            file=sys.stderr,
        )
        for origin, label, _test, _pkg, why in missing:
            print(f"  {os.path.basename(origin)}: {why}\n      {label}", file=sys.stderr)
        print(
            "\nName the package that holds the test, or the test the guard has been renamed "
            "to. Deleting the mutation leaves the guard with no falsification, which is the "
            "state this check exists to make visible.",
            file=sys.stderr,
        )
        return 2

    print(f"every declared fragment still applies: {count} mutation(s) across {len(specs)} spec(s)")
    subs = sum(1 for _, _, test, _, _ in entries if names_a_subtest(test))
    print(
        f"every declared test exists in the package its mutation names: {len(entries)} name(s), "
        f"{subs} of them a subtest really entered under its parent"
    )
    return 0


def replay_all(directory):
    """Run every declared falsification (#169).

    A falsification proves a test bites on the day it is run. Nothing ran them
    again, so each one was a claim about the past — and this repository has
    already shipped a falsification claim that was false: "neutralise any of the
    three locks and the barrage goes red on the first attempt", thirty green runs
    with the lock removed, recorded in the 0.8.0 CHANGELOG.

    Every spec, one after another. A spec whose fragment no longer applies is a
    failure and not a skip: code moved under it, so it has silently stopped
    measuring, which is the exact thing this replay exists to catch.
    """
    absent = not_a_directory(directory)
    if absent:
        return refuse(absent, code=1)
    specs = every_spec(directory)
    if not specs:
        return refuse(f"{directory} holds no spec, so a replay would measure nothing", code=1)

    print(f"replaying {len(specs)} falsification(s)\n")
    # Two outcomes that must never be added together. A Verdict of 1 is a
    # verdict: a mutation applied, compiled, and the test stayed green, so that
    # guard is not guarded. Anything else non-zero is the ABSENCE of a verdict:
    # the copy was red before any mutation (a plain 1 since #954), or the spec
    # was refused (2), so nothing was measured at all. replay_outcome() tells
    # them apart by type, because the value no longer can.
    #
    # They were counted as one, and on 2026-09-14 a mise shim that would not
    # start in the copy turned into "199 of 199 falsifications no longer hold" —
    # a sentence that reads like every guard in the repository broke at once,
    # printed by a harness that had just said, 199 times, that it was measuring
    # nothing. An absence of measurement is not a result, which is the whole
    # argument this repository makes everywhere else.
    #
    # TestAnUnmeasuredSpecIsNotReportedAsBroken holds this.
    failed, unmeasured = [], []
    for spec in specs:
        print("=" * 72)
        print(f"== {os.path.basename(spec)}")
        print("=" * 72)
        outcome = replay_outcome(main([argv0, spec]))
        if outcome == "stopped biting":
            failed.append(os.path.basename(spec))
        elif outcome == "not measured":
            unmeasured.append(os.path.basename(spec))
        print()

    print("#" * 72)
    if unmeasured:
        print(f"{len(unmeasured)} of {len(specs)} were NOT MEASURED: {', '.join(unmeasured)}")
        print(
            "A spec refused or unreadable, a copy red before any mutation, a mutation "
            "that did not apply or did not build, a test that was skipped or never ran, "
            "a test that hung until its timeout, a restore that broke: none of these "
            "measures a guard, so these say nothing about the guards they name. Read "
            "the reason above each: it is the spec, the harness or the environment, "
            "not the subject."
        )
    if failed:
        print(f"{len(failed)} of {len(specs)} falsifications no longer hold: {', '.join(failed)}")
        print(
            "A guard whose test stopped biting is a guard that stopped working, "
            "and the test is what has to be fixed rather than the spec."
        )
    if failed or unmeasured:
        return 1
    print(f"all {len(specs)} falsifications still hold")
    return 0


argv0 = "falsify.py"


USAGE = "usage: falsify.py <spec.json> | --all <dir> | --lint <dir> | --selftest"


def main(argv):
    if argv[1:] in (["--help"], ["-h"]):
        print(USAGE)
        return 0
    if len(argv) == 2 and argv[1] == "--selftest":
        return selftest()
    if len(argv) == 3 and argv[1] == "--all":
        return replay_all(argv[2])
    if len(argv) == 3 and argv[1] == "--lint":
        return lint(argv[2])
    if len(argv) != 2 or argv[1].startswith("-"):
        return refuse(USAGE, code=1)
    spec_path = argv[1]
    if not os.path.isfile(spec_path):
        return refuse(f"{spec_path}: is not a regular file, so there is no spec to read", code=1)
    spec, why = load_spec(spec_path)
    if why:
        return refuse(why)

    mutations = spec.get("mutations") or []
    if not mutations:
        return refuse(f"{spec_path} declares no mutation, so a run would measure nothing")

    # Every shape check first, before a single file is copied. A run that is
    # going to be void should cost nothing and say why.
    for m in mutations:
        for key in ("label", "file", "find", "replace", "test"):
            if not m.get(key):
                return refuse(f"a mutation is missing {key!r}: {m.get('label', m)}")
        if m["find"] == m["replace"]:
            return refuse(f"{m['label']!r} replaces a fragment with itself")
        # A path spelled two ways, and an absolute one that would be rewritten in
        # the working repository rather than in the copy: see path_problem.
        # Ambiguity is refused before anything is copied, on the same terms as a
        # dead fragment: see fragment_problem.
        ambiguous = path_problem(m["file"]) or fragment_problem(m["file"], m["find"])
        if ambiguous:
            return refuse(f"{m['label']!r}: {ambiguous}")
        lost = dropped_identifiers(m["find"], m["replace"])
        if lost:
            return refuse(
                f"{m['label']!r} drops {', '.join(lost)} from the fragment.\n"
                f"        This harness requires every name in `find` to survive into "
                f"`replace`. It has not checked whether Go would mind here — it cannot "
                f"see the rest of the file — and the rule is conservative on purpose: "
                f"a deleted term that orphans a name fails every test in the package, "
                f"which reads exactly like the guard being proven.\n"
                f"        Neutralise the condition instead of deleting the term: keep "
                f"every name evaluated and make the expression never true "
                f"(`… && false`, `(… || true)`). The mutation then changes a truth "
                f"value and nothing else."
            )

    # And the test it names has to exist where it says it does (#923). Asked
    # here, before the copy, because a run whose verdict is going to be void
    # should cost nothing and say why — and because the alternative is `go test
    # -run` exiting 0 over no tests at all, which this harness read as
    # TEST STILL PASSED.
    #
    # _single_spec_case() fails without THIS wiring, and not merely without the
    # function it calls: with `unknown = []`, that case's run copies the tree,
    # runs the mutation, and prints `compiled=yes  TEST STILL PASSED` for a name
    # nobody declared — measured on 2026-11-14 — while every lint case stays
    # green.
    unknown = tests_that_do_not_exist(entries_of(spec, spec_path), os.getcwd())
    if unknown:
        _, label, test, _, why = unknown[0]
        return refuse(
            f"{label!r} names a test that does not exist: {why}.\n"
            f"        Nothing would be run for this mutation, and an empty run exits 0, "
            f"which this harness used to print as TEST STILL PASSED. That is a verdict "
            f"over an empty population and it is not one.\n"
            f"        Name the package that holds {test!r}, or the name the test carries "
            f"today."
        )

    src = os.getcwd()
    # mkdtemp rather than a name built from the spec: a predictable path under
    # /tmp is a symlink somebody else can plant, and this copy is about to be
    # compiled and executed. bandit refused the first version and was right.
    dst = tempfile.mkdtemp(
        prefix="falsify-" + os.path.basename(spec_path).removesuffix(".json") + "-"
    )
    print(f"copying the repository to {dst}", flush=True)
    copy_tree(src, dst)

    default_pkg = spec.get("package", "./...")
    packages = sorted({m.get("package", default_pkg) for m in mutations})
    # A spec may arm what its subject's tests need — see run().
    spec_env = spec.get("env") or {}

    # "This copy is green before any mutation" is the same question for every
    # spec naming the same packages under the same environment: every copy is a
    # fresh copy of one unchanged tree. It was asked once per spec, so a replay
    # of the whole directory asked it 242 times for some fifteen distinct
    # answers — sixty of them for ./internal/cli/ alone (#890).
    #
    # What this gives up, and it is a real cost: a package that fails
    # intermittently used to get one chance per spec to be caught before a
    # mutation, and now gets one per replay. A flaky test is a defect to fix
    # rather than a thing to average over, and a spec whose subject is a race
    # declares `repeat`, which is how this harness measures one on purpose.
    key = baseline_key(packages, spec_env)
    if BASELINES.get(key):
        print(f"baseline: green, proved earlier in this replay for {' '.join(packages)}\n")
    else:
        base = run(dst, ["go", "test", "-count=1", *packages], spec_env)
        if base.returncode != 0:
            print(
                "the copy is red before any mutation, so nothing below measures a guard",
                file=sys.stderr,
            )
            # Every failing name, wherever it sits, then both tails (#967).
            # _red_baseline_cases() fails without it, through a real `go test`.
            print(red_baseline_report(base.stdout, base.stderr), file=sys.stderr)
            shutil.rmtree(dst, ignore_errors=True)
            # 1 and not 2: the tree or the environment is red, no spec drifted.
            # A plain int, not a Verdict, so the replay counts it as not
            # measured (replay_outcome).
            return 1
        BASELINES[key] = True
        print("baseline: green\n")

    verdicts, pristine = [], {}
    for m in mutations:
        path = os.path.join(dst, m["file"])
        if m["file"] not in pristine:
            with open(path, encoding="utf-8") as fh:
                pristine[m["file"]] = fh.read()
        body = pristine[m["file"]]
        if m["find"] not in body:
            verdicts.append((m["label"], "DID NOT APPLY", "-"))
            print(f"!! {m['label']}: fragment not found in {m['file']}\n")
            continue

        with open(path, "w", encoding="utf-8") as fh:
            fh.write(body.replace(m["find"], m["replace"], 1))

        pkg = m.get("package", default_pkg)
        vet = run(dst, ["go", "vet", pkg])
        if vet.returncode != 0:
            # The identifier check above should make this unreachable. If it
            # fires, the rule missed a shape and the rule is what needs work.
            verdicts.append((m["label"], "DID NOT COMPILE (void)", "no"))
            print(f"!! {m['label']}: does not build, verdict void")
            print(vet.stderr[-900:])
        else:
            # A race is measured, not observed once. With its per-server lock
            # neutralised, TestAttachingANICDoesNotResurrectADeletedServer went
            # red on three of five identical runs on 17 August 2026 — so a
            # harness that runs it once reports a coin toss and prints it as a
            # verdict, which is worse than no verdict because it reads like a
            # proof. It had already printed "TEST STILL PASSED" twice for a
            # guard that does bite.
            #
            # A spec whose subject is a race declares `repeat`, and the odds of
            # a false green fall from p to p**n.
            repeat = int(m.get("repeat") or spec.get("repeat") or 1)
            # The mutation's timeout wins over the spec's; with neither, Go's
            # own default (10 minutes) bounds the run. See the module docstring.
            timeout = m.get("timeout") or spec.get("timeout")
            bound = ["-timeout", timeout] if timeout else []
            # Anchored, and read with -v: see anchored() and passed_leaves().
            # _anchoring_cases() and _skipped_cases() fail without each.
            pattern = anchored(m["test"])
            res = run(
                dst,
                ["go", "test", f"-count={repeat}", *bound, "-run", pattern, "-v", pkg],
                spec_env,
            )
            # Asked before "the test bit", since a hang also exits non-zero:
            # every non-zero exit read "the test bit", so a mutation that made
            # the test hang printed `ok` after Go's ten minutes (#965). Only a
            # non-zero exit can be a hang: a test that exits 0 and merely
            # prints the words is still read for its PASS lines.
            # _hang_cases() fails without this branch, through a real `go test`.
            after = hung_after(res.stdout, res.stderr)
            if res.returncode != 0 and after:
                verdict = HUNG
                said = f"hung until it was killed ({after}): NOT MEASURED, not a bite"
            elif res.returncode != 0:
                verdict, said = "the test bit", "red"
            elif passed_leaves(pattern, res.stdout):
                verdict, said = "TEST STILL PASSED", "STILL GREEN"
            else:
                verdict, said = SKIPPED, "green, but no test it names ran to a PASS: NOT MEASURED"
            verdicts.append((m["label"], verdict, "yes"))
            print(
                f"{'ok ' if verdict == 'the test bit' else '!! '}{m['label']}\n    {pattern}: "
                f"{said}\n"
            )

        with open(path, "w", encoding="utf-8") as fh:
            fh.write(pristine[m["file"]])

    broken = unrestored(dst, pristine)
    print("=" * 72)
    for label, verdict, compiled in verdicts:
        print(f"  compiled={compiled:3}  {verdict:24}  {label}")
    ok_final = not broken
    if ok_final:
        print("\nafter restoration: green")
    else:
        print(f"\nafter restoration: RED — the restore is broken: {', '.join(broken)}")
        # The suite, only now, and only to show what a broken restore did. A
        # green byte comparison needs no suite: the bytes are the copy the
        # baseline was green on.
        final = run(dst, ["go", "test", "-count=1", *packages], spec_env)
        print(final.stdout[-2000:], file=sys.stderr)
    shutil.rmtree(dst, ignore_errors=True)

    code, why = spec_outcome(verdicts, broken)
    if why:
        print(f"falsify: {why}", file=sys.stderr)
    return code


# The lines that name what failed in a `go test` run, wherever they sit: a
# failing test at any depth, a package that failed, a panic at the start of a
# line (a test's panic, or `panic: test timed out after …`).
# Read with .match(), which anchors at the start of the line: a test that
# logs "--- FAIL:" writes it after its file and line, and is not a failure.
NAMES_A_FAILURE = re.compile(r"(?:\s*--- FAIL: |FAIL\t|panic: |fatal error: )")


def red_baseline_report(stdout, stderr):
    """What a red baseline prints, so the test that made it red can be named (#967).

    It printed the last 2500 characters of stdout and nothing else, and the
    copy is deleted right after. During #928 on 2026-11-15 a baseline came
    back red once in four runs of a-failure-below-http.json, the tail was
    flag usage text a test had logged, and nobody could say which test it
    was: a flake that cannot be named cannot be fixed.

    So: every line that names a failure, from the whole output, then the two
    tails. A run that names no failure says so instead of printing nothing,
    because "no name found" is not "nothing failed".
    """
    named = [line for line in (stdout or "").splitlines() if NAMES_A_FAILURE.match(line)]
    named += [line for line in (stderr or "").splitlines() if NAMES_A_FAILURE.match(line)]
    out = ["what failed, every line that names it:"]
    out += [f"  {line.strip()}" for line in named] or [
        "  (no line names a test, a package or a panic: read the tails below)"
    ]
    out += ["stdout, last 1500 characters:", (stdout or "")[-1500:].rstrip() or "  (empty)"]
    out += ["stderr, last 1500 characters:", (stderr or "")[-1500:].rstrip() or "  (empty)"]
    return "\n".join(out)


def spec_outcome(verdicts, broken):
    """(exit code, reason) of one spec's run, from its verdicts and its restore.

    Only a mutation that applied, built, and whose test ran is a verdict
    (Verdict). A mutation that did not apply or did not build measured nothing,
    and so did one whose test was skipped or never ran (SKIPPED, since a
    falsifier measured it reported as a broken guard on 2026-11-15), and one
    whose test hung until `go test -timeout` killed it (HUNG, #965),
    and neither did a run whose restore broke, since the copy every later test
    ran in is then not the tree the baseline was green on. They were all a
    Verdict(1) until #954, so the replay tallied a harness fault as a guard that
    stopped biting. They answer a plain 1 now: an error of the run, read by
    replay_outcome() as not measured.

    A broken restore outranks everything: nothing that ran after it can be
    trusted. A mutation that applied and left its test green is still a verdict
    when another mutation of the spec was void, because that one was measured.
    _spec_outcome_cases() holds each row.
    """
    if broken:
        return 1, f"the restore broke ({', '.join(broken)}), so no verdict of this run holds"
    if any(v[1] == "TEST STILL PASSED" for v in verdicts):
        return Verdict(1), None
    void = [(label, verdict) for label, verdict, _ in verdicts if verdict != "the test bit"]
    if void:
        return 1, (
            f"{len(void)} mutation(s) measured nothing: "
            + "; ".join(f"{label} ({VOID_WHY.get(verdict, verdict)})" for label, verdict in void)
        )
    return Verdict(0), None


# A mutation whose test was green without any test it names running to a PASS.
SKIPPED = "SKIPPED OR NEVER RAN (void)"

# A mutation whose test run was killed at its timeout (#965).
HUNG = "HUNG UNTIL TIMEOUT (void)"

# The two lines a timeout ends a run with, measured with go1.26.11 on
# 2026-11-15, both on stdout:
#
#   - `panic: test timed out after 1s`, printed by the test binary's own
#     alarm when -timeout (declared, or Go's 10-minute default) expires;
#   - `*** Test killed with quit: ran too long (1m1s).`, printed by `go test`
#     when it kills a binary whose alarm never fired, which is a TestMain or
#     an init that blocks before m.Run. go kills at the timeout plus one
#     minute (src/cmd/go/internal/test/test.go, 1740-1742 and 841); without
#     the signal trace it reads `*** Test killed: ran too long (…).`
#
# Either may follow characters a test printed without a newline
# (`....panic: test timed out after 1s`), so the line may start with
# anything that does not end in a space. A line where a space comes right
# before the words is a test logging them, t.Log and t.Fatal output being
# indented, and it must stay a bite.
#
# The duration is read as a Go duration, never as "anything up to the end":
# `t.Fatalf("waited for %q", "panic: test timed out after 1s")` is a bite whose
# line ends in `1s"`, and `\S+` took the quote for part of the duration.
GO_DURATION = r"\d[\d.]*(?:ns|us|µs|ms|s|m|h)(?:[\d.]+(?:ns|us|µs|ms|s|m|h))*"
TIMED_OUT = re.compile(
    r"^(?:.*\S)?("
    + r"panic: test timed out after "
    + GO_DURATION
    + r"|\*\*\* Test killed(?: with \w+)?: ran too long \("
    + GO_DURATION
    + r"\)\."
    + r")$",
    re.MULTILINE,
)


def hung_after(stdout, stderr):
    """The line saying the run was killed at its timeout, or "" when none does.

    Both streams are read: `go test` prints both forms on its own stdout
    today, and a harness that read one stream only would miss them the day
    that changes, and read the hang as a bite again.
    """
    m = TIMED_OUT.search(stdout or "") or TIMED_OUT.search(stderr or "")
    return m.group(1) if m else ""


# Why each void verdict measured nothing, in the words spec_outcome() prints.
# A verdict missing here is printed as itself and still counted void.
VOID_WHY = {
    "DID NOT APPLY": "did not apply",
    "DID NOT COMPILE (void)": "did not build",
    SKIPPED: "its test was skipped, or nothing it names ran",
    HUNG: "its test hung until the timeout killed it, which is not a bite",
}


if __name__ == "__main__":
    DROPPED_GIT = drop_inherited_git(os.environ)
    sys.exit(main(sys.argv))
