#!/usr/bin/env python3
"""Scan the pinned Go toolchain for advisories, and move every pin together.

Why this is a schedule and not a gate (#865). govulncheck's verdict changes
without a single line of this repository changing: four times in 2026 a
standard-library advisory reddened pull requests that touched no Go file, and
each time the red landed on whoever pushed next. Nothing here was the cause and
nothing here could fix it; the pin is what moves. So the question is asked on a
schedule, the way drift.yml asks about the upstream surface, and `mise run
vulncheck` stays the way to ask it by hand.

What this script does, and does not:

- it reads the pinned version from mise.toml, which is the source of truth the
  workflows are checked against (TestTheGoWorkflowPinsTheVersionMiseDoes);
- it runs govulncheck and reads its JSON, taking only advisories whose fix is a
  Go version later than the pin. An advisory in a dependency is not this
  script's business: this module has no dependencies, which a pre-commit hook
  enforces;
- with --apply it rewrites every pin, in every file, to the earliest version
  that fixes all of them.

**It rewrites pins, never prose.** .github/workflows/go.yml carries the history
of the four previous moves in its comments — "MOVED TO 1.26.9 on 2026-10-19 ...
GO-2026-6617 ... found in go1.26.6 and fixed in go1.26.9" — and a global
search-and-replace would rewrite that history into a lie. Only the two exact pin
spellings are touched, and TestOnlyPinsMoveAndNotTheHistory holds that.

Exit codes follow the repository's contract: 0 nothing to do, 1 error,
2 an advisory moves the pin.
"""

import argparse
import json
import os
import pathlib
import re
import subprocess
import sys

# The two spellings a pin takes. Anchored on the whole assignment so a version
# named inside a sentence is not a pin.
YAML_PIN = re.compile(r"^(?P<lead>\s*go-version:\s*')(?P<version>\d+\.\d+\.\d+)(?P<tail>')$", re.M)
TOML_PIN = re.compile(r'^(?P<lead>go\s*=\s*")(?P<version>\d+\.\d+\.\d+)(?P<tail>")', re.M)

PIN_FILES = ("mise.toml",)
PIN_GLOBS = (".github/workflows/*.yml",)


def version_key(v):
    return tuple(int(p) for p in v.split("."))


def pinned_version(root):
    """The version mise names, which every workflow is checked against."""
    text = (root / "mise.toml").read_text()
    m = TOML_PIN.search(text)
    if not m:
        raise SystemExit(
            "mise.toml names no Go version; this script has no reference to compare to"
        )
    return m.group("version")


def pin_files(root):
    out = [root / name for name in PIN_FILES]
    for pattern in PIN_GLOBS:
        out.extend(sorted(root.glob(pattern)))
    return [p for p in out if p.exists()]


def scan(root, pinned):
    """Advisories whose fix is later than the pin, newest fix first.

    govulncheck exits non-zero when it finds something, which is its normal
    answer and not a failure. A failure is a non-zero exit with no JSON, and
    that is reported rather than read as "nothing found" — the silence this
    repository keeps being burned by.
    """
    proc = subprocess.run(
        ["govulncheck", "-format", "json", "./..."],
        cwd=root,
        capture_output=True,
        text=True,
        check=False,
    )
    if not proc.stdout.strip():
        raise SystemExit(
            f"govulncheck produced no JSON (exit {proc.returncode}). stderr:\n{proc.stderr}"
        )

    found = {}
    decoder = json.JSONDecoder()
    text = proc.stdout
    i = 0
    while i < len(text):
        while i < len(text) and text[i] in " \t\r\n":
            i += 1
        if i >= len(text):
            break
        obj, end = decoder.raw_decode(text, i)
        i = end
        osv = obj.get("osv")
        if not osv:
            continue
        for affected in osv.get("affected", []):
            pkg = (affected.get("package") or {}).get("name")
            if pkg != "stdlib":
                continue
            for rng in affected.get("ranges", []):
                for event in rng.get("events", []):
                    fixed = event.get("fixed")
                    if not fixed:
                        continue
                    fixed = fixed.removeprefix("go")
                    if version_key(fixed) > version_key(pinned):
                        found[osv["id"]] = fixed
    return found


def rewrite(root, target):
    """Move every pin to target. Returns the files changed."""
    changed = []
    for path in pin_files(root):
        text = path.read_text()
        new = YAML_PIN.sub(lambda m: m.group("lead") + target + m.group("tail"), text)
        new = TOML_PIN.sub(lambda m: m.group("lead") + target + m.group("tail"), new)
        if new != text:
            path.write_text(new)
            changed.append(str(path.relative_to(root)))
    return changed


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    # Two verbs rather than one flag, because they are two jobs carrying two
    # risks: `scan` only asks a question whose answer moves on its own, and
    # `bump` is the half that writes to files. The separation is a
    # contributor's idea, from the shell implementation this replaced (#906).
    ap.add_argument(
        "verb",
        nargs="?",
        default="scan",
        choices=("scan", "bump"),
        help="scan reports and writes nothing; bump also moves every pin",
    )
    ap.add_argument(
        "--apply",
        action="store_true",
        help="the older spelling of `bump`, kept so an existing caller does not break",
    )
    ap.add_argument("--root", default=os.environ.get("FEINT_ROOT", "."))
    args = ap.parse_args()
    rewriting = args.apply or args.verb == "bump"

    root = pathlib.Path(args.root).resolve()
    pinned = pinned_version(root)
    advisories = scan(root, pinned)

    if not advisories:
        # Said out loud rather than left to the absence of output: a silent
        # schedule is indistinguishable from a dead one, and #705 is that story.
        print(f"govulncheck concluded: no advisory moves the pin, which is go{pinned}")
        return 0

    target = max(advisories.values(), key=version_key)
    for advisory, fixed in sorted(advisories.items()):
        print(f"{advisory}: found in go{pinned}, fixed in go{fixed}")
    print(f"the pin moves from go{pinned} to go{target}")

    if rewriting:
        changed = rewrite(root, target)
        print(f"{len(changed)} file(s) moved: {', '.join(changed)}")
    return 2


if __name__ == "__main__":
    sys.exit(main())
