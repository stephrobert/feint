#!/usr/bin/env python3
"""Drive the emulator's own page in a headless browser: assert, then photograph.

Why this exists, and why it is not a screenshot script.

`docs/limits.md` recorded a real hole: the page is held by four Go tests that all
read the asset as *text* — no provider name in it, no markup built from a string,
every identifier the script writes to exists, every route a GET — and not one of
them executes a line of JavaScript. A change that left the page blank, or put a
number in the wrong node, passed CI green.

A browser that loads the page to photograph it is exactly the one that can say
the nodes carry the right values. So the assertions come first and the images are
a by-product: a script that only photographs proves nothing, and would rot into a
gallery of screens nobody checks.

Why the DevTools protocol rather than `chromium --dump-dom`. Measured on this
station: `--dump-dom` serialises the document before a single fetch resolves —
`#served` still holds its placeholder and the health badge still says
"connecting", with `--timeout`, with `--virtual-time-budget`, in old and new
headless alike. It cannot see a page that renders from data, which is every
interesting part of this one. The protocol can wait for a condition and then
evaluate in the page, so it is the tool that answers the question.

Why Python, and why no dependency. The browser is tooling, like `scw`, `exo` and
`terraform` that the conformance suites already drive; it never enters `go.mod`,
which stays three lines. The client below speaks enough of RFC 6455 and of the
protocol to navigate, wait, evaluate and capture — about a hundred lines of
standard library, in the language this repository already uses for its contract
extraction.

Usage:
    tools/ui/check-page.py --endpoint http://127.0.0.1:4599 --out docs/assets/ui
    tools/ui/check-page.py --endpoint http://127.0.0.1:4599 --check   # assert only
"""

from __future__ import annotations

import argparse
import base64
import contextlib
import json
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from typing import IO

# The browsers this looks for, in order. A GitHub runner ships google-chrome; a
# Debian or Ubuntu workstation ships chromium or chromium-browser. Naming them
# rather than probing $BROWSER: the value of this check is that it runs the same
# engine everywhere, and $BROWSER on a desktop is whatever opened a link last.
BROWSERS = ("chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome")

# A fixed viewport, because a screenshot's size must not depend on the window a
# person happened to have open. Chosen to fit the two-column layout without a
# horizontal scrollbar.
VIEWPORT = (1200, 900)

READY_TIMEOUT = 20.0


class Failure(Exception):
    """An assertion that did not hold, or a browser that would not answer."""


# --------------------------------------------------------------------------
# A very small WebSocket client, enough for the DevTools protocol.
# --------------------------------------------------------------------------


class WebSocket:
    """Text frames over a socket, client side, no extensions negotiated.

    DevTools answers one JSON object per frame and sends unmasked frames; this
    end masks its own, as RFC 6455 requires of a client. Fragmented and large
    frames are both handled because a screenshot arrives base64-encoded and is
    megabytes long, which is precisely where a naive reader breaks.
    """

    def __init__(self, url: str, timeout: float = 30.0) -> None:
        rest = url.removeprefix("ws://")
        host_port, _, path = rest.partition("/")
        host, _, port = host_port.partition(":")
        self.sock = socket.create_connection((host, int(port or 80)), timeout=timeout)
        self.sock.settimeout(timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        handshake = (
            f"GET /{path} HTTP/1.1\r\n"
            f"Host: {host_port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n\r\n"
        )
        self.sock.sendall(handshake.encode())
        self.buffer = b""
        while b"\r\n\r\n" not in self.buffer:
            chunk = self.sock.recv(4096)
            if not chunk:
                raise Failure("the browser closed the connection during the handshake")
            self.buffer += chunk
        head, _, rest_bytes = self.buffer.partition(b"\r\n\r\n")
        if b"101" not in head.split(b"\r\n")[0]:
            raise Failure(f"the browser refused the upgrade: {head.splitlines()[0]!r}")
        self.buffer = rest_bytes

    def _read(self, n: int) -> bytes:
        while len(self.buffer) < n:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise Failure("the browser closed the connection")
            self.buffer += chunk
        out, self.buffer = self.buffer[:n], self.buffer[n:]
        return out

    def send(self, payload: str) -> None:
        data = payload.encode()
        header = bytearray([0x81])  # FIN + text
        mask = os.urandom(4)
        length = len(data)
        if length < 126:
            header.append(0x80 | length)
        elif length < 1 << 16:
            header.append(0x80 | 126)
            header += struct.pack(">H", length)
        else:
            header.append(0x80 | 127)
            header += struct.pack(">Q", length)
        header += mask
        masked = bytes(b ^ mask[i % 4] for i, b in enumerate(data))
        self.sock.sendall(bytes(header) + masked)

    def recv(self) -> str:
        chunks: list[bytes] = []
        while True:
            first, second = self._read(2)
            fin = first & 0x80
            opcode = first & 0x0F
            length = second & 0x7F
            if length == 126:
                (length,) = struct.unpack(">H", self._read(2))
            elif length == 127:
                (length,) = struct.unpack(">Q", self._read(8))
            if second & 0x80:  # a server must not mask, but be exact anyway
                mask = self._read(4)
                body = bytes(b ^ mask[i % 4] for i, b in enumerate(self._read(length)))
            else:
                body = self._read(length)
            if opcode == 0x8:
                raise Failure("the browser closed the connection")
            if opcode in (0x9, 0xA):  # ping/pong, not our business
                continue
            chunks.append(body)
            if fin:
                return b"".join(chunks).decode()

    def close(self) -> None:
        with contextlib.suppress(OSError):
            self.sock.close()


# --------------------------------------------------------------------------
# The browser, and the page inside it.
# --------------------------------------------------------------------------


@dataclass
class Page:
    ws: WebSocket
    counter: int = 0
    # Console errors are collected rather than ignored: a page that throws still
    # renders whatever ran before the throw, so a silent exception is exactly how
    # half a screen goes missing without a single assertion noticing.
    console: list[str] = field(default_factory=list)

    def call(self, method: str, **params: object) -> dict:
        self.counter += 1
        self.ws.send(json.dumps({"id": self.counter, "method": method, "params": params}))
        while True:
            message = json.loads(self.ws.recv())
            if message.get("method") == "Runtime.exceptionThrown":
                detail = message["params"]["exceptionDetails"]
                described = str(detail.get("exception", {}).get("description", ""))
                self.console.append(detail.get("text", "") + " " + described)
                continue
            if message.get("id") != self.counter:
                continue
            if "error" in message:
                raise Failure(f"{method}: {message['error'].get('message')}")
            return message.get("result", {})

    def evaluate(self, expression: str) -> object:
        result = self.call(
            "Runtime.evaluate",
            expression=expression,
            returnByValue=True,
            awaitPromise=True,
        )
        if result.get("exceptionDetails"):
            thrown = result["exceptionDetails"].get("text")
            raise Failure(f"the page threw evaluating {expression!r}: {thrown}")
        return result.get("result", {}).get("value")

    def wait_for(self, expression: str, what: str, timeout: float = READY_TIMEOUT) -> None:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.evaluate(expression) is True:
                return
            time.sleep(0.1)
        raise Failure(f"timed out after {timeout:.0f}s waiting for {what}")

    def screenshot(self, path: str, selector: str | None = None) -> None:
        # captureBeyondViewport, because every region worth photographing but the
        # first is below the fold: without it the clip is intersected with the
        # visible window and the file comes out blank. Measured — the first run
        # of this harness wrote a 2.7 kB log.png of nothing.
        params: dict[str, object] = {"format": "png", "captureBeyondViewport": True}
        if selector:
            target = json.dumps(selector)
            box = self.evaluate(
                f"(() => {{ const el = document.querySelector({target});"
                " if (!el) return null; const r = el.getBoundingClientRect();"
                " return {x: r.x, y: r.y, width: r.width, height: r.height}; })()"
            )
            if not box:
                raise Failure(f"nothing to photograph: {selector} is not in the document")
            # Rounded outward, and padded by a few pixels, so a shadow or a
            # border is not sliced off by a fractional layout position.
            pad = 8
            params["clip"] = {
                "x": max(box["x"] - pad, 0),
                "y": max(box["y"] - pad, 0),
                "width": box["width"] + 2 * pad,
                "height": box["height"] + 2 * pad,
                "scale": 1,
            }
        data = self.call("Page.captureScreenshot", **params)["data"]
        with open(path, "wb") as handle:
            handle.write(base64.b64decode(data))


def find_browser() -> str | None:
    for name in BROWSERS:
        found = shutil.which(name)
        if found:
            return found
    return None


def start_browser(binary: str, profile: str) -> tuple[subprocess.Popen, str]:
    """Start the browser and return it with the URL of its page target.

    Tried twice, on a fresh port each time. The port is chosen by binding zero
    and closing, so between that close and the browser's own bind the number is
    anybody's — on a busy runner it is a race this cannot win, only retry.
    """
    failures = []
    for attempt in range(2):
        try:
            # A subdirectory rather than a sibling: main() removes `profile`
            # and only that, so a sibling would survive every run as a leak.
            # A fresh one per attempt, because a first launch that died holding
            # the profile lock makes the second fail for the wrong reason.
            return start_browser_once(binary, os.path.join(profile, str(attempt)))
        except Failure as why:
            failures.append(f"attempt {attempt + 1}: {why}")
    raise Failure("the browser never opened a page target.\n" + "\n".join(failures))


def start_browser_once(binary: str, profile: str) -> tuple[subprocess.Popen, str]:
    """One start: spawn the browser and wait for its debugging port."""
    port = free_port()
    # The browser's own account of why it failed. It used to go to DEVNULL,
    # which is how four consecutive CI failures produced one sentence between
    # them — "the browser never opened a page target" — and no way to tell a
    # taken port from a slow cold start from a missing shared library. A
    # harness that cannot say why it failed gets re-run instead of read.
    #
    # Closed on the way out of this function, which the browser does not mind:
    # the descriptor it writes to is its own, duplicated at spawn.
    with tempfile.TemporaryFile() as diagnosis:
        return spawn_and_wait(binary, profile, port, diagnosis)


def spawn_and_wait(binary: str, profile: str, port: int, diagnosis: IO[bytes]):
    """Spawn the browser, and wait for a page target or a reason there is none."""
    process = subprocess.Popen(  # noqa: S603 - a fixed binary, list arguments, no shell
        [
            binary,
            "--headless=new",
            "--disable-gpu",
            "--no-sandbox",
            "--no-first-run",
            "--disable-extensions",
            # Nothing here should ever reach the network, and a browser that
            # phones home makes the run depend on somebody's connectivity.
            "--disable-background-networking",
            "--disable-component-update",
            "--disable-sync",
            # Determinism: the same viewport everywhere, and no animation to
            # catch mid-flight. The page honours prefers-reduced-motion, so this
            # is what stops a capture landing on frame three of a fade.
            "--force-prefers-reduced-motion",
            f"--window-size={VIEWPORT[0]},{VIEWPORT[1]}",
            f"--user-data-dir={profile}",
            f"--remote-debugging-port={port}",
            "about:blank",
        ],
        stdout=subprocess.DEVNULL,
        stderr=diagnosis,
    )

    def why() -> str:
        """What the browser said, for a message that can be acted on."""
        try:
            diagnosis.seek(0)
            said = diagnosis.read().decode("utf-8", "replace").strip()
        except OSError:
            return "and said nothing readable"
        if not said:
            return "and said nothing"
        return "and said: " + " / ".join(said.splitlines()[-4:])

    # 60 seconds rather than 20. The old budget covered a cold start on an idle
    # machine and nothing else: this job runs after a Go build and an emulator
    # start, on a shared runner, and headless Chrome's first launch is the part
    # that gets slow when the runner is busy.
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise Failure(
                f"{binary} exited with {process.returncode} before it opened "
                f"its debugging port on {port}, {why()}"
            )
        try:
            # The scheme and host are literals here: the browser this script
            # just started, on the loopback interface.
            devtools = f"http://127.0.0.1:{port}/json/list"
            with urllib.request.urlopen(devtools, timeout=1) as answer:  # nosec B310
                targets = json.load(answer)
            for target in targets:
                if target.get("type") == "page" and target.get("webSocketDebuggerUrl"):
                    return process, target["webSocketDebuggerUrl"]
        except (urllib.error.URLError, OSError, json.JSONDecodeError):
            time.sleep(0.2)

    process.kill()
    process.wait(timeout=10)
    raise Failure(
        f"{binary} was still running after 60s without a page target on port {port}, {why()}"
    )


def free_port() -> int:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def get_json(url: str) -> dict:
    """Read one of the emulator's own endpoints.

    The URL is built from --endpoint, which tools/ui/screenshots.sh sets to the
    address it just started an emulator on. Nothing here follows a link out of
    an answer, so there is no scheme to be surprised by.
    """
    if not url.startswith("http://"):
        raise Failure(f"refusing to read {url!r}: this reads plain HTTP from a local emulator")
    # The scheme is checked on the line above.
    with urllib.request.urlopen(url, timeout=10) as answer:  # nosec B310
        return json.load(answer)


# --------------------------------------------------------------------------
# The assertions. This is the part that is not a screenshot script.
# --------------------------------------------------------------------------


@dataclass
class Checks:
    passed: int = 0
    failures: list[str] = field(default_factory=list)

    def equal(self, what: str, got: object, want: object) -> None:
        if got == want:
            self.passed += 1
            return
        self.failures.append(f"{what}: the page shows {got!r}, the API answered {want!r}")

    def truthy(self, what: str, got: object) -> None:
        if got:
            self.passed += 1
            return
        self.failures.append(what)


def any_text_equals(page: Page, selector: str, wanted: str) -> bool:
    """Is there an element matching `selector` whose text is exactly `wanted`?

    The five assertions that ask this question ask it of five different regions,
    and writing the traversal once keeps each of them one readable line.
    """
    return bool(
        page.evaluate(
            f"Array.from(document.querySelectorAll({json.dumps(selector)}))"
            f".some(el => el.textContent.trim() === {json.dumps(wanted)})"
        )
    )


def any_text_contains(page: Page, selector: str, wanted: str) -> bool:
    return bool(
        page.evaluate(
            f"Array.from(document.querySelectorAll({json.dumps(selector)}))"
            f".some(el => el.textContent.includes({json.dumps(wanted)}))"
        )
    )


def text_of(page: Page, selector: str) -> str:
    target = json.dumps(selector)
    value = page.evaluate(
        f"(() => {{ const el = document.querySelector({target});"
        " return el ? el.textContent.trim() : null; })()"
    )
    return value if value is not None else "<no such element>"


def assert_page(page: Page, endpoint: str, checks: Checks, opened_product: str | None) -> None:
    """Compare what the page displays with what the endpoints answered.

    Two independent observers of one emulator, which is the same shape as the
    conformance suite's own argument: the numbers are read here over HTTP and
    there out of the document, and a divergence means one of them is lying.
    """
    conformance = get_json(f"{endpoint}/_feint/conformance")
    health = get_json(f"{endpoint}/_feint/health")
    resources = get_json(f"{endpoint}/_feint/resources")
    data = get_json(f"{endpoint}/_feint/ui/data")

    # The headline numbers, which are the ones the page exists to show.
    checks.equal("routes mounted", text_of(page, "#served"), str(conformance["served"]))
    checks.equal(
        "driven by a real client", text_of(page, "#n-driven"), str(conformance["exercised"])
    )
    checks.equal("probed", text_of(page, "#n-probed"), str(conformance["probed"]))
    checks.equal(
        "never proven",
        text_of(page, "#n-unproven"),
        str(conformance["served"] - conformance["exercised"] - conformance["probed"]),
    )

    # The header facts.
    checks.equal("driver", text_of(page, "#driver"), health["machines"])
    checks.equal("resources", text_of(page, "#resources"), str(health["resources"]))
    checks.equal("version", text_of(page, "#version"), data["version"])

    # The machines region follows the driver, and nothing else. With no runtime
    # it must be absent from the document rather than hidden by a rule.
    has_region = page.evaluate("document.querySelector('#machines-card') !== null")
    checks.equal(
        "the machines region follows the declared driver",
        has_region,
        health["machines"] != "none",
    )

    # A resource the client created, with its identifier and its attributes,
    # inside a card that is closed by default.
    if resources["resources"]:
        first = resources["resources"][0]
        found = any_text_equals(page, "#inventory-groups .res-id", first["id"])
        checks.truthy(f"the inventory shows the resource {first['id']}", found)
        kind_shown = any_text_equals(page, "#inventory-groups .group-kind", first["kind"])
        checks.truthy(f"the inventory names the kind {first['kind']!r}", kind_shown)
        if first.get("attrs"):
            key = sorted(first["attrs"])[0]
            attr_shown = any_text_equals(page, "#inventory-groups .tree-key", key)
            checks.truthy(f"the attribute {key!r} is rendered", attr_shown)

    # A refusal reason, which is the thing the drill-down was built for. Read
    # from the artefact and looked for in the opened product.
    declined = [
        op
        for op in data["upstream"]["operations"]
        if op["status"] == "declined" and op.get("reason")
    ]
    if declined:
        declined = [
            op for op in declined if f"{op['provider']} / {op['product']}" == opened_product
        ]
    if declined:
        reason = declined[0]["reason"]
        shown = any_text_equals(page, "#upstream-rows .op-reason", reason.strip())
        checks.truthy("a refusal reason is displayed in full", shown)
        operation_shown = any_text_equals(page, "#upstream-rows .op-name", declined[0]["operation"])
        named = declined[0]["operation"]
        checks.truthy(f"the declined operation {named} is listed", operation_shown)

    # The call log: a call that went through, and the field no handler read.
    trace = get_json(f"{endpoint}/_feint/trace")["exchanges"]
    if trace:
        last = trace[-1]
        path_shown = any_text_equals(page, "#log .path", last["path"])
        checks.truthy(f"the log shows the call to {last['path']}", path_shown)
    unread = [x for x in trace if x.get("unread")]
    if unread:
        field_name = unread[0]["unread"][0]
        shown = any_text_contains(page, "#log .verdict.unread .fields", field_name)
        checks.truthy(f"the log names the unread field {field_name!r}", shown)
    missing = [x for x in trace if not x.get("mounted")]
    if missing:
        shown = any_text_equals(page, "#log .op", "no route mounted")
        checks.truthy("the log says which call found no route mounted", shown)

    # The drill-down behind the headline number.
    listed = page.evaluate("document.querySelectorAll('#proof-drill-list .op').length")
    driven_count = len([1 for n in conformance["calls"].values() if n > 0])
    checks.equal("the driven drill-down lists every driven operation", listed, driven_count)

    # And nothing threw on the way.
    checks.truthy("the page raised no exception: " + "; ".join(page.console), not page.console)


def product_to_open(upstream: dict) -> str | None:
    """Choose the upstream product to unfold.

    A product that serves nothing and declines everything, when there is one:
    its list opens on a refusal and its reason, which is what the drill-down was
    built for and what the picture has to show. Anywhere else the served
    operations come first, correctly — a reader scanning for work should meet the
    work first — and the sentence is a scroll away, which a still image cannot
    take.

    Deterministic, and read from the artefact rather than hardcoded: the choice
    survives a pack starting to serve the product it currently declines.
    """
    declining = {
        f"{op['provider']} / {op['product']}"
        for op in upstream["operations"]
        if op["status"] == "declined" and op.get("reason")
    }
    for product in upstream["products"]:
        label = f"{product['provider']} / {product['product']}"
        if product["served"] == 0 and product["declined"] > 0 and label in declining:
            return label
    for product in upstream["products"]:
        label = f"{product['provider']} / {product['product']}"
        if label in declining:
            return label
    return None


def open_disclosures(page: Page, product_label: str | None) -> None:
    """Open what a reader would click, so the assertions and the images see it.

    Setting .open on a <details> and calling .click() on a button is what the
    reader's own gestures do; nothing here is a back door the page would not
    otherwise offer.

    The upstream row is chosen by name rather than by position, because the
    assertion that follows reads a refusal reason out of a particular product:
    opening one row and looking for another product's sentence is a test that
    fails for a reason that has nothing to do with the page. Measured, on the
    first run of this harness.
    """
    page.evaluate("document.getElementById('open-driven').click()")
    if product_label:
        wanted = json.dumps(product_label)
        opened = page.evaluate(
            "(() => { const rows = Array.from("
            "document.querySelectorAll('#upstream-rows .row-details'));"
            " const row = rows.find(r =>"
            f" r.querySelector('.name').textContent.trim() === {wanted});"
            " if (!row) return false; row.open = true; return true; })()"
        )
        if not opened:
            raise Failure(f"no upstream row is named {product_label!r}")
    page.evaluate(
        "(() => { const first = document.querySelector('#inventory-groups .resource');"
        " if (first) { first.open = true; } })()"
    )
    # One frame for the layout to settle before anything is measured or framed.
    page.evaluate("new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")


def capture(page: Page, out: str) -> list[str]:
    """Write the images the documentation shows. Few, and each one a whole region.

    Framed after the assertions, never before: what is photographed is a reader's
    view, and what is asserted is the page as it arrives. Pressing "problems
    only" here is the same click a reader makes, and it is the frame worth
    keeping — the log scrolls to its newest line, so the ordinary flow pushes the
    two lines that are the point of the feature out of view.
    """
    page.evaluate("document.getElementById('filter-problems').click()")
    page.evaluate("new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))")
    os.makedirs(out, exist_ok=True)
    shots = [
        ("page.png", "#proof-card"),
        ("resources.png", "#inventory-card"),
        ("upstream.png", "#upstream-card"),
        ("log.png", ".log-card"),
    ]
    written = []
    for name, selector in shots:
        page.screenshot(os.path.join(out, name), selector)
        written.append(name)
    return written


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--endpoint", default="http://127.0.0.1:4599")
    parser.add_argument("--out", default="docs/assets/ui")
    parser.add_argument("--check", action="store_true", help="assert only, write no image")
    args = parser.parse_args()

    binary = find_browser()
    if binary is None:
        print(
            "feint: no browser found (tried: " + ", ".join(BROWSERS) + ").\n"
            "       The page's DOM is therefore unchecked in this run. Install one, or run\n"
            "       this on a machine that has one: docs/limits.md says what goes unmeasured.",
            file=sys.stderr,
        )
        return 3

    profile = tempfile.mkdtemp(prefix="feint-ui-")
    process, ws_url = start_browser(binary, profile)
    checks = Checks()
    try:
        page = Page(WebSocket(ws_url))
        page.call("Page.enable")
        page.call("Runtime.enable")
        # The viewport is set through the protocol as well as on the command
        # line: the flag sizes the window, this sizes the layout, and only the
        # second one is what a capture is measured against.
        page.call(
            "Emulation.setDeviceMetricsOverride",
            width=VIEWPORT[0],
            height=VIEWPORT[1],
            deviceScaleFactor=1,
            mobile=False,
        )
        page.call("Page.navigate", url=f"{args.endpoint}/_feint/ui")
        # Ready means the data arrived, not that the document parsed. Every
        # interesting region of this page is rendered from a fetch, which is
        # exactly what a --dump-dom snapshot misses.
        page.wait_for(
            "document.querySelector('#served').textContent.trim() !== '—'"
            " && document.querySelectorAll('#log .entry').length > 0"
            " && document.querySelectorAll('#inventory-groups .group').length > 0"
            " && document.querySelectorAll('#upstream-rows .row-details').length > 0",
            "the page to render the emulator's data",
        )
        # The product to open is chosen from the artefact, so the assertion below
        # reads a sentence out of the row this actually opened.
        upstream = get_json(f"{args.endpoint}/_feint/ui/data")["upstream"]
        opened_product = product_to_open(upstream)
        open_disclosures(page, opened_product)
        assert_page(page, args.endpoint, checks, opened_product)

        if not args.check and not checks.failures:
            written = capture(page, args.out)
            print(f"  wrote {len(written)} image(s) to {args.out}: " + ", ".join(written))
    finally:
        process.terminate()
        try:
            process.wait(timeout=10)
        except subprocess.TimeoutExpired:
            process.kill()
        shutil.rmtree(profile, ignore_errors=True)

    for failure in checks.failures:
        print(f"FAIL: {failure}", file=sys.stderr)
    print(f"  {checks.passed} assertion(s) held, {len(checks.failures)} failed")
    return 1 if checks.failures else 0


if __name__ == "__main__":
    sys.exit(main())
