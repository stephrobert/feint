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
"""

import inspect
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

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


def replay_outcome(code):
    """What one spec's exit code means for the replay's tally.

    Three answers, and the middle one is the whole point. Exit 1 is a VERDICT:
    a mutation applied, compiled, and the test stayed green, so that guard is
    not guarded. Any other non-zero is the ABSENCE of a verdict: the copy was
    red before any mutation, or the spec was refused, so nothing was measured.

    They were added together until 2026-09-14, and a mise shim that would not
    start in the copy turned that arithmetic into "199 of 199 falsifications no
    longer hold" — every guard in the repository reported broken, by a harness
    that had just said 199 times that it was measuring nothing.

    The selftest drives THIS function rather than a copy of its logic, or the
    check would pass while the replay went on adding the two together.
    """
    if code == 0:
        return "bit"
    if code == 1:
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


def refuse(message):
    print(f"falsify: {message}", file=sys.stderr)
    return 2


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


def selftest():
    """Prove the rule on the three mistakes it was written for."""
    failures = 0
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
        f"the rule refuses all {len(HISTORY)} historical mistakes, accepts all "
        f"{len(HISTORY)} fixes, and accepts {len(ACCEPTED)} valid mutations it "
        f"has refused at some point"
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


def _report(got, want, why):
    if got != want:
        print(f"!! the lint answers {got} on {why}, want {want}")
        return 1
    print(f"ok  the lint answers {got} on {why}")
    return 0


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
    as the committed run lists 36 patterns in one call. Replace the body of
    covered_paths() with `return set(known)` and this case answers 0.
    """
    with tempfile.TemporaryDirectory() as tmp:
        _selftest_module(tmp, second_package=True)
        directory = os.path.join(tmp, "two")
        os.mkdir(directory)
        # The name lives in q. One spec declares p, which is the mistake; the
        # other declares q, which is correct and is what makes the lint list
        # both packages in the same call.
        _one_spec(
            directory,
            "names-p.json",
            package="./p/",
            file=os.path.join(tmp, "p", "subject.go"),
            find="func guard() bool { return true }",
            test="TestTheGuardLivesInQ",
        )
        _one_spec(
            directory,
            "names-q.json",
            package="./q/",
            file=os.path.join(tmp, "q", "elsewhere.go"),
            find="func elsewhere() bool { return true }",
            test="TestTheGuardLivesInQ",
        )
        got = lint(directory, tmp)
    return _report(got, 2, "a name that exists in another package than the one declared")


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
            file=os.path.join(tmp, "p", "subject.go"),
            find="func guard() bool { return true }",
            test="TestTheGuardHolds",
        )
        got = lint(directory, tmp)
    return _report(
        got, 2, "a name that exists, over a listing one broken package made untrustworthy"
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
    return 0


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
            file=os.path.join(tmp, "p", "subject.go"),
            find="func guard() bool { return true }",
            test="TestGatedByEnv/armed-sub",
            env={"FEINT_SELFTEST_ARMED": "1"},
        )
        got = lint(directory, tmp)
    return _report(got, 0, "a subtest that exists only when the spec's own env arms its parent")


def lint_selftest():
    """The lint answers 2 on a dead fragment and 0 on a live one.

    A control that never fires reads exactly like a control that passes, which
    is the failure this whole program exists to name. So the lint is pointed at
    two specs built here: one naming a fragment that is in the file, one naming
    a fragment that is not.

    Written against a temporary directory rather than the repository's own
    specs, because those are supposed to be green — a selftest that depended on
    them would go red the day somebody legitimately retargets a mutation, and
    that is the noise which teaches people to skip the check.
    """
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        subject, twice = _selftest_module(tmp)

        cases = [
            (
                subject,
                "live.json",
                "func guard() bool { return true }",
                "TestTheGuardHolds",
                0,
                "a fragment that is in the file",
            ),
            (
                subject,
                "dead.json",
                "func guard() bool { return gone }",
                "TestTheGuardHolds",
                2,
                "a fragment that is not",
            ),
            (
                twice,
                "ambiguous.json",
                "bool { return true }",
                "TestTheGuardHolds",
                2,
                "a fragment that matches two places, where only the first is rewritten",
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
                "func guard() bool { return true }",
                "TestTheGuardHoldsRenamed",
                2,
                "a live fragment whose test has been renamed out from under it",
            ),
            # And the accepting half, on the same module: a name that is really
            # there must stay green, or the check would redden every spec and
            # teach people to skip it.
            (
                subject,
                "subtest.json",
                "func guard() bool { return true }",
                "TestTheGuardHolds/named-sub",
                0,
                "a subtest of a test that exists, which `-list` never prints on its own",
            ),
            # #923 one level down, and it was live in the first version of this
            # check: `go test -run TestX/no-such-sub` runs TestX, no subtest
            # matches, the parent passes, the run exits 0, and the replay prints
            # TEST STILL PASSED. Measured against this harness on 2026-11-14:
            # named-sub gave "the test bit", no-such-sub gave TEST STILL PASSED.
            (
                subject,
                "no-such-sub.json",
                "func guard() bool { return true }",
                "TestTheGuardHolds/no-such-sub",
                2,
                "a subtest name no subtest of that parent carries",
            ),
            # A `test` field that is not a usable pattern. Not a void verdict but
            # a FALSE one: `go test -run 'TestX('` rejects the pattern and exits
            # non-zero, the replay reads non-zero as "the test bit", and the
            # harness prints `ok` for a mutation it never ran.
            (
                subject,
                "unparsable.json",
                "func guard() bool { return true }",
                "TestTheGuardHolds(",
                2,
                "a test field that is not a usable pattern, which would read as a proof",
            ),
        ]
        for target, name, find, test, want, why in cases:
            spec = {
                "package": "./p/",
                "mutations": [
                    {
                        "label": "selftest",
                        "file": target,
                        "find": find,
                        "replace": find.replace("true", "false"),
                        "test": test,
                    }
                ],
            }
            directory = os.path.join(tmp, name.removesuffix(".json"))
            os.mkdir(directory)
            with open(os.path.join(directory, name), "w", encoding="utf-8") as fh:
                json.dump(spec, fh)

            got = lint(directory, tmp)
            if got != want:
                print(f"!! the lint answers {got} on {why}, want {want}")
                failures += 1
            else:
                print(f"ok  the lint answers {got} on {why}")

    failures += _cross_package_case()
    failures += _parent_cannot_answer_case()
    failures += _env_armed_case()
    failures += _unbuildable_case()
    failures += _single_spec_case()
    print()
    failures += _replay_reporting_cases()
    print()
    failures += _budget_cases()
    return failures


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
    # branches back together fails this.
    for code, want, why in (
        (1, "stopped biting", "a mutation that applied and left its test green is a verdict"),
        (2, "not measured", "a copy red before any mutation measured no guard"),
        (3, "not measured", "a refused spec measured no guard either"),
        (0, "bit", "a mutation that bit is neither failed nor unmeasured"),
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


def every_spec(directory):
    """Every spec in the directory, sorted, so two runs report the same order."""
    return sorted(
        os.path.join(directory, name) for name in os.listdir(directory) if name.endswith(".json")
    )


def fragment_problem(target, find):
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
    """
    if not target or not os.path.exists(target):
        return f"{target}: no such file"
    with open(target, encoding="utf-8") as fh:
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
    res = subprocess.run(
        ["go", "test", "-count=1", "-run", parent, "-v", pkg],
        cwd=root,
        capture_output=True,
        text=True,
        env={**os.environ, **dict(env)},
    )
    names = {m.group(1) for m in (RAN.match(line) for line in res.stdout.splitlines()) if m}
    doubt = ""
    if res.returncode != 0:
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
    BASELINES: `--all` asks this once per spec, 251 of them for some fifteen
    distinct package patterns, against a tree that does not change while the
    replay runs. The night was already cancelled at its 210-minute ceiling once
    (#890), and a check that closes a hole must not reopen that one.
    """
    key = ("list", tuple(packages), os.path.abspath(root), tuple(env))
    if key in LISTINGS:
        return LISTINGS[key]
    res = subprocess.run(
        ["go", "test", "-list", ".*", *packages],
        cwd=root,
        capture_output=True,
        text=True,
        env={**os.environ, **dict(env)},
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
    LISTINGS[key] = (by_path, res.stderr.strip() if res.returncode != 0 else "")
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
        head = top_level_pattern(test)
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
        parent = top_level_pattern(test)
        entered, doubt = ran_under(parent, pkg, root, env)
        if any(selects(test, full) for full in entered):
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
    0.9 s.

    `root` is the tree the packages are resolved in; the selftest points it at a
    module of its own so this function is driven rather than imitated.
    """
    specs = every_spec(directory)
    if not specs:
        return refuse(f"{directory} holds no spec, so this checked nothing")

    stale, entries = [], []
    for path in specs:
        with open(path, encoding="utf-8") as fh:
            spec = json.load(fh)
        entries += entries_of(spec, path)
        for m in spec.get("mutations") or []:
            why = fragment_problem(m.get("file"), m.get("find"))
            if why:
                stale.append((path, m.get("label", "?"), why))

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

    count = 0
    for path in specs:
        with open(path, encoding="utf-8") as fh:
            count += len(json.load(fh).get("mutations") or [])
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
    specs = every_spec(directory)
    if not specs:
        return refuse(f"{directory} holds no spec, so a replay would measure nothing")

    print(f"replaying {len(specs)} falsification(s)\n")
    # Two outcomes that must never be added together. Exit 1 is a verdict: a
    # mutation applied, compiled, and the test stayed green, so that guard is
    # not guarded. Exit 2 is the ABSENCE of a verdict: the copy was red before
    # any mutation, or the spec was refused, so nothing was measured at all.
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
            "A copy that is red before any mutation measures no guard, so these say "
            "nothing about the guards they name. Read the first failure above: it is "
            "the harness or the environment, not the subject."
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


def main(argv):
    if len(argv) == 2 and argv[1] == "--selftest":
        return selftest()
    if len(argv) == 3 and argv[1] == "--all":
        return replay_all(argv[2])
    if len(argv) == 3 and argv[1] == "--lint":
        return lint(argv[2])
    if len(argv) != 2:
        return refuse("usage: falsify.py <spec.json> | --all <dir> | --lint <dir> | --selftest")
    spec_path = argv[1]
    with open(spec_path, encoding="utf-8") as fh:
        spec = json.load(fh)

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
        # Ambiguity is refused before anything is copied, on the same terms as a
        # dead fragment: see fragment_problem.
        ambiguous = fragment_problem(m["file"], m["find"])
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
            print(base.stdout[-2500:], file=sys.stderr)
            shutil.rmtree(dst, ignore_errors=True)
            return 2
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
            res = run(dst, ["go", "test", f"-count={repeat}", "-run", m["test"], pkg], spec_env)
            bit = res.returncode != 0
            verdicts.append((m["label"], "the test bit" if bit else "TEST STILL PASSED", "yes"))
            print(
                f"{'ok ' if bit else '!! '}{m['label']}\n    {m['test']}: "
                f"{'red' if bit else 'STILL GREEN'}\n"
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

    everything_bit = all(v[1] == "the test bit" for v in verdicts)
    return 0 if (everything_bit and ok_final) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
