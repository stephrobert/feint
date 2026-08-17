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


def run(dst, cmd):
    """Every command is capped, so a runaway cannot take the station with it."""
    return subprocess.run(
        ["systemd-run", "--user", "--scope", "-q", "-p", "MemoryMax=8G", "-p", "MemorySwapMax=0"]
        + cmd,
        cwd=dst,
        capture_output=True,
        text=True,
    )


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
    if failures:
        print(f"{failures} failure(s): the rule does not hold its own history")
        return 1
    print(
        f"the rule refuses all {len(HISTORY)} historical mistakes, accepts all "
        f"{len(HISTORY)} fixes, and accepts {len(ACCEPTED)} valid mutations it "
        f"has refused at some point"
    )
    return 0


def every_spec(directory):
    """Every spec in the directory, sorted, so two runs report the same order."""
    return sorted(
        os.path.join(directory, name) for name in os.listdir(directory) if name.endswith(".json")
    )


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
    failed = []
    for spec in specs:
        print("=" * 72)
        print(f"== {os.path.basename(spec)}")
        print("=" * 72)
        if main([argv0, spec]) != 0:
            failed.append(os.path.basename(spec))
        print()

    print("#" * 72)
    if failed:
        print(f"{len(failed)} of {len(specs)} falsifications no longer hold: {', '.join(failed)}")
        print(
            "A guard whose test stopped biting is a guard that stopped working, "
            "and the test is what has to be fixed rather than the spec."
        )
        return 1
    print(f"all {len(specs)} falsifications still hold")
    return 0


argv0 = "falsify.py"


def main(argv):
    if len(argv) == 2 and argv[1] == "--selftest":
        return selftest()
    if len(argv) == 3 and argv[1] == "--all":
        return replay_all(argv[2])
    if len(argv) != 2:
        return refuse("usage: falsify.py <spec.json> | --all <dir> | --selftest")
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

    base = run(dst, ["go", "test", "-count=1", *packages])
    if base.returncode != 0:
        print(
            "the copy is red before any mutation, so nothing below measures a guard",
            file=sys.stderr,
        )
        print(base.stdout[-2500:], file=sys.stderr)
        shutil.rmtree(dst, ignore_errors=True)
        return 2
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
            res = run(dst, ["go", "test", f"-count={repeat}", "-run", m["test"], pkg])
            bit = res.returncode != 0
            verdicts.append((m["label"], "the test bit" if bit else "TEST STILL PASSED", "yes"))
            print(
                f"{'ok ' if bit else '!! '}{m['label']}\n    {m['test']}: "
                f"{'red' if bit else 'STILL GREEN'}\n"
            )

        with open(path, "w", encoding="utf-8") as fh:
            fh.write(pristine[m["file"]])

    final = run(dst, ["go", "test", "-count=1", *packages])
    print("=" * 72)
    for label, verdict, compiled in verdicts:
        print(f"  compiled={compiled:3}  {verdict:24}  {label}")
    ok_final = final.returncode == 0
    print(f"\nafter restoration: {'green' if ok_final else 'RED — the restore is broken'}")
    if not ok_final:
        print(final.stdout[-2000:], file=sys.stderr)
    shutil.rmtree(dst, ignore_errors=True)

    everything_bit = all(v[1] == "the test bit" for v in verdicts)
    return 0 if (everything_bit and ok_final) else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
