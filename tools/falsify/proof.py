#!/usr/bin/env python3
"""Falsify the replay itself: weaken a test so it can no longer fail, and require
the replay to name it while the ordinary suite stays green.

    mise run falsify:proof

This is #169's acceptance criterion, word for word:

    "Deliberately break one test — weaken an assertion so it can no longer fail
     — and the harness goes red naming that test, while `mise run check` stays
     green."

Both halves are asserted, and the second is the one that carries the claim. A
run where `go test` also went red would prove nothing about the direction this
harness looks in: it would only prove that a broken tree is a broken tree.

Why this lives in the repository rather than in a script beside a pull request:
that is the exact defect #169 was filed for. Every falsification of the 0.9 train
was run once, by hand, in a file deleted with its branch, so each verdict became
a claim about the past. Writing the proof of the replay as one more throwaway
would have reproduced the fault while closing it.

The weakening is deliberately the shape a test really rots into. Nobody deletes
a test; somebody loosens an assertion while chasing an unrelated red, and what is
left still runs, still reads the value, and can no longer tell the two answers
apart.
"""

import os
import shutil
import subprocess
import sys
import tempfile

EXCLUDE = {".git", ".upstream", ".terraform", "notes", ".claude"}

TEST_FILE = "internal/core/emulator/mispointed_test.go"
TEST = "TestAmbiguityIsStatedRatherThanResolved"
PKG = "./internal/core/emulator/"
SPEC = "tools/falsify/specs/mispointed.json"

# `got` is the sentence the hint produced. The strong form requires both
# candidate prefixes to appear, which is the property #179 added; the weak form
# passes on any sentence holding a slash, so it passes equally on the answer that
# names both and on the answer that names one.
#
# The first attempt at this weakening was not one: `&&` over the same substring
# still failed under the mutation, because that mutation really does remove
# "/v2/" from the answer. A falsification has to be checked against what its
# mutation produces, not against what the mutation is called — which is the same
# mistake, one level up, that the replay exists to catch.
STRONG = '\tif !strings.Contains(got, "/v2/") || !strings.Contains(got, "/other/") {'
WEAK = '\tif !strings.Contains(got, "/") {'


def ignore(src):
    def inner(directory, names):
        dropped = [n for n in names if n in EXCLUDE]
        # The built binary, when the working tree has one: it is large, it is
        # not an input, and copying it doubles the cost of every run.
        if os.path.abspath(directory) == os.path.abspath(src) and os.path.isfile(
            os.path.join(directory, "feint")
        ):
            dropped.append("feint")
        return dropped

    return inner


def run(dst, cmd):
    # The same containment falsify.py uses: three concurrent Go builds once took
    # the author's station down, and this one nests a build inside a build.
    return subprocess.run(
        ["systemd-run", "--user", "--scope", "-q", "-p", "MemoryMax=8G", "-p", "MemorySwapMax=0"]
        + cmd,
        cwd=dst,
        capture_output=True,
        text=True,
    )


def main():
    src = os.getcwd()
    if not os.path.isfile(os.path.join(src, SPEC)):
        print(f"falsify: run this from the repository root ({SPEC} not found)", file=sys.stderr)
        return 2

    # mkdtemp rather than a predictable path: this copy is about to be compiled
    # and executed, and a name somebody else can plant a symlink at is not one.
    dst = tempfile.mkdtemp(prefix="falsify-proof-")
    try:
        shutil.copytree(src, dst, ignore=ignore(src), symlinks=True, dirs_exist_ok=True)
        print(f"copied the tree to {dst}")

        path = os.path.join(dst, TEST_FILE)
        with open(path, encoding="utf-8") as fh:
            source = fh.read()
        if STRONG not in source:
            print(
                f"falsify: the assertion to weaken is no longer in {TEST_FILE}.\n"
                f"        {TEST} was rewritten, so this proof has to be rewritten with "
                f"it — which is the point, and not a reason to delete it.",
                file=sys.stderr,
            )
            return 2
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(source.replace(STRONG, WEAK, 1))
        print(f"weakened {TEST}: the assertion runs and can no longer distinguish\n")

        failures = 0

        suite = run(dst, ["go", "test", "-count=1", PKG])
        if suite.returncode == 0:
            print(f"ok  `go test {PKG}` is green with {TEST} unable to fail")
        else:
            print(f"!! `go test {PKG}` went red, so this proves nothing about the replay")
            print(suite.stdout[-2000:])
            failures += 1

        replay = run(dst, ["python3", "tools/falsify/falsify.py", SPEC])
        named = TEST in replay.stdout and "TEST STILL PASSED" in replay.stdout
        if replay.returncode != 0 and named:
            print(f"ok  the replay is red and names {TEST}")
        else:
            print(f"!! the replay did not catch it (exit {replay.returncode}, named={named})")
            print(replay.stdout[-3000:])
            failures += 1

        print()
        if failures:
            print(f"{failures} half(s) failed: #169's claim is not proven on this tree")
            return 1
        print("a test that stopped biting fails the replay and not the suite")
        return 0
    finally:
        shutil.rmtree(dst, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
