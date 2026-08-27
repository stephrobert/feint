#!/usr/bin/env python3
"""A limit that no longer holds is worse than an undocumented one (#558).

`docs/limits.md` is where this project writes what it does not do, with the
measurement that establishes it, and nothing re-checks a hand-written section:
`feint docs --check` guards the generated blocks only. The 2026-08-27 audit of
all fifty sections found three that had gone false without anyone noticing — one
for a fortnight, one for ten days.

This is the cheap half of the answer, and it is a *signal, never a proof*. Most
citations of a closed issue are perfectly legitimate: #202 (no NAT on a routed
NIC), #283 (Kubernetes refused) and #285 (placement recorded) are closed
*decisions* whose limits hold. So this refuses to demand a rewrite. It demands a
**dated acknowledgement**: somebody looked at this section after that issue
closed, and said it still stands.

What it catches, measured against the audit's own findings: the #498 section,
which would have described a lifted limit the day its fix merged; and the
"planned to follow (#7)" clause, delivered on 2026-08-13 and read as future for
fourteen days. What it is **blind to**, and the reason #558 must not be closed
by this file alone: "Outscale and Exoscale are starters" cited no issue at all —
the product simply grew past the sentence. Nothing here would ever see that.

Where it runs is part of the design. This belongs in the weekly drift workflow,
never in `prepush` or a hook: a red that interrupts daily work over
documentation freshness is the gate people learn to bypass with `--no-verify`,
which disarms every other hook at once. The failure mode it hunts is measured in
days and weeks, not in commits.

Usage:
    tools/docs/limits-acks.py check    # exit 2 when an acknowledgement is stale
    tools/docs/limits-acks.py refresh  # re-date every ack to today, after a real audit

Offline: `check --offline` reads only the ledger and the file, and reports what
it could not ask. That distinction matters — "nobody could look" is not
"nothing is stale", and this prints the two differently.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from datetime import date
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
LIMITS = ROOT / "docs" / "limits.md"
LEDGER = ROOT / "docs" / "limits-acks.json"
REPO = "stephrobert/feint"

GREEN, RED, YELLOW, DIM, OFF = "\033[32m", "\033[31m", "\033[33m", "\033[2m", "\033[0m"
if not sys.stdout.isatty() or os.environ.get("NO_COLOR"):
    GREEN = RED = YELLOW = DIM = OFF = ""


def sections() -> list[tuple[str, set[int]]]:
    """Every `## ` heading of limits.md, with the issue numbers its body cites.

    The heading is the key rather than a line number, because a section that
    moves is the same section and a re-numbered ledger would go stale on every
    insertion — which is the failure this file exists to prevent, one level up.
    """
    text = LIMITS.read_text(encoding="utf-8")
    out: list[tuple[str, set[int]]] = []
    for block in re.split(r"^## ", text, flags=re.M)[1:]:
        heading, _, body = block.partition("\n")
        cited = {int(n) for n in re.findall(r"#(\d{1,4})\b", heading + body)}
        out.append((heading.strip(), cited))
    return out


def closed_at(numbers: set[int]) -> tuple[dict[int, str | None], set[int], set[int]]:
    """When each issue closed, plus the numbers that are not ours and the ones nobody could ask.

    Three outcomes, and conflating them is exactly the failure this file exists
    to prevent one level up. A `#573` in this document is Exoscale's upstream
    issue, written `[#573][exo-573]` — it is **not ours**, and reporting it as
    "could not be asked" would teach a reader to ignore that line. A network
    failure is a fourth state of the world: nobody could look, which is not the
    same as nothing is stale.
    """
    if not numbers:
        return {}, set(), set()
    found: dict[int, str | None] = {}
    foreign: set[int] = set()
    unreachable: set[int] = set()
    for n in sorted(numbers):
        try:
            run = subprocess.run(
                ["gh", "api", f"repos/{REPO}/issues/{n}", "--jq", ".state,.closed_at"],
                capture_output=True,
                text=True,
                timeout=30,
            )
        except (subprocess.TimeoutExpired, FileNotFoundError):
            unreachable.add(n)
            continue
        if run.returncode != 0:
            # 404 is a number that belongs to another repository; anything else
            # is a tool or network fault, and the two get different words.
            (foreign if "404" in run.stderr or "Not Found" in run.stderr else unreachable).add(n)
            continue
        lines = run.stdout.strip().splitlines()
        shut = lines[1].strip() if len(lines) > 1 else "null"
        found[n] = None if shut in ("null", "") else shut[:10]
    return found, foreign, unreachable


def load_ledger() -> dict[str, str]:
    if not LEDGER.exists():
        return {}
    return json.loads(LEDGER.read_text(encoding="utf-8")).get("acknowledged", {})


def write_ledger(acks: dict[str, str]) -> None:
    doc = {
        "comment": (
            "When each section of docs/limits.md was last confirmed against the "
            "issues it cites. A citation is a signal, never a proof: most closed "
            "issues are decisions whose limits hold. Re-date a line only after "
            "looking at the section. See tools/docs/limits-acks.py and #558."
        ),
        "acknowledged": dict(sorted(acks.items())),
    }
    LEDGER.write_text(json.dumps(doc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")


def check(offline: bool) -> int:
    secs = sections()
    acks = load_ledger()
    every = {n for _, cited in secs for n in cited}
    if offline:
        closed, foreign, missed = {}, set(), every
    else:
        closed, foreign, missed = closed_at(every)

    stale: list[str] = []
    unacked: list[str] = []

    for heading, cited in secs:
        shut = {n: closed[n] for n in cited if closed.get(n)}
        if not shut:
            continue
        ack = acks.get(heading)
        newest = max(shut.values())
        if ack is None:
            unacked.append(
                f"{heading}\n      cites {', '.join(f'#{n}' for n in sorted(shut))}, "
                f"newest closed {newest}, never acknowledged"
            )
        elif ack < newest:
            worst = sorted(n for n, d in shut.items() if d > ack)
            stale.append(
                f"{heading}\n      acknowledged {ack}, but "
                f"{', '.join(f'#{n}' for n in worst)} closed after it "
                f"(newest {newest})"
            )

    print(f"limits.md: {len(secs)} sections, {len(every)} issues cited")
    if foreign:
        print(
            f"  {DIM}·{OFF} {len(foreign)} number(s) belong to another repository, "
            f"not ours: {', '.join(f'#{n}' for n in sorted(foreign))}"
        )
    if missed:
        print(
            f"  {YELLOW}?{OFF} {len(missed)} issue(s) could not be asked"
            f"{' (offline)' if offline else ''}: nobody could look is not nothing is stale"
        )
    for line in unacked:
        print(f"  {RED}!{OFF} {line}")
    for line in stale:
        print(f"  {RED}!{OFF} {line}")

    if stale or unacked:
        print(
            f"\n{RED}{len(stale) + len(unacked)} section(s) cite an issue that closed after "
            f"anyone last looked.{OFF}"
        )
        print("A closed issue is not a lifted limit — most are decisions whose bounds hold.")
        print("Read the section, then either fix it or re-date its line:")
        print(f"  {DIM}tools/docs/limits-acks.py refresh{OFF}")
        print(f"  {DIM}(re-dates every section — only after a real pass){OFF}")
        return 2

    print(f"  {GREEN}ok{OFF} every section citing a closed issue was confirmed after it closed")
    return 0


def refresh() -> int:
    today = date.today().isoformat()
    acks = load_ledger()
    for heading, cited in sections():
        if cited:
            acks[heading] = today
    for heading in list(acks):
        if heading not in {h for h, _ in sections()}:
            del acks[heading]  # a section that went is a line that goes
    write_ledger(acks)
    print(f"re-dated {len(acks)} section(s) to {today} in {LEDGER.relative_to(ROOT)}")
    print("This says somebody looked. It is only true if somebody did.")
    return 0


def main() -> int:
    argv = sys.argv[1:]
    if not argv or argv[0] not in ("check", "refresh"):
        print(__doc__)
        return 64
    if argv[0] == "refresh":
        return refresh()
    return check(offline="--offline" in argv)


if __name__ == "__main__":
    sys.exit(main())
