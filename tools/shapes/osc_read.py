"""Read an Outscale account and write the answers as a proxy-shaped transcript.

READ-ONLY by construction: the only entry point refuses any call whose name does
not start with Read, so this file cannot create, modify or delete anything even
if it is edited carelessly later.

Why this exists rather than `oapi-cli` through `feint proxy`, which is how the
other two providers are recorded: **SigV4 signs the Host header.** A client
configured to reach 127.0.0.1 signs 127.0.0.1, and the real cloud recomputes the
signature from the Host it received, so it refuses. Measured on the same
credential and the same body: signing `api.<region>.outscale.com` answers 200,
signing the proxy's address answers 401 AccessDenied.

A reverse proxy cannot make a client sign a host it was never given. Lifting
that needs DNS interception and TLS termination, which is issue #76. Until then
this signs the real host and talks to it directly, and the transcript it writes
is the same `internal/trace.Exchange` shape the proxy emits, so `feint shapes`
folds it without knowing the difference.

Do not confuse that refusal with the one PACE exists for, below. They look
nothing alike in cause and identical in symptom, and an hour was lost to reading
one as the other.

No credential ever reaches the file: only the response body is recorded, and the
request headers are not written at all.
"""

import datetime
import hashlib
import hmac
import json
import os
import sys
import time
import urllib.error
import urllib.request

READS = [
    "ReadVms",
    "ReadNets",
    "ReadSubnets",
    "ReadVolumes",
    "ReadKeypairs",
    "ReadSecurityGroups",
    "ReadPublicIps",
    "ReadImages",
    "ReadVmTypes",
    "ReadTags",
    "ReadRouteTables",
    "ReadInternetServices",
    "ReadNatServices",
    "ReadSnapshots",
    "ReadNics",
    "ReadQuotas",
    "ReadDhcpOptions",
    "ReadNetPeerings",
    "ReadLoadBalancers",
    "ReadPublicIpRanges",
    "ReadSubregions",
    "ReadAccounts",
    "ReadVmsState",
    "ReadNetAccessPoints",
    "ReadNetAccessPointServices",
    "ReadVmsHealth",
    "ReadListenerRules",
    "ReadServerCertificates",
    "ReadVolumeUpdateTasks",
    "ReadRegions",
]


def load_profile(name=None):
    with open(os.path.expanduser("~/.osc/config.json")) as f:
        src = json.load(f)
    if name and name in src:
        chosen = name
    elif "souverain" in src:
        chosen = "souverain"
    else:
        chosen = "default"
    p = src[chosen]
    region = p.get("region_name") or p.get("region")
    return chosen, p["access_key"], p["secret_key"], region


def sign(access, secret, region, host, call, body):
    now = datetime.datetime.now(datetime.UTC)
    stamp = now.strftime("%Y%m%dT%H%M%SZ")
    day = now.strftime("%Y%m%d")
    canonical = (
        f"POST\n/api/v1/{call}\n\n"
        f"content-type:application/json\nhost:{host}\nx-osc-date:{stamp}\n\n"
        f"content-type;host;x-osc-date\n" + hashlib.sha256(body).hexdigest()
    )
    scope = f"{day}/{region}/api/osc4_request"
    to_sign = (
        "OSC4-HMAC-SHA256\n"
        + stamp
        + "\n"
        + scope
        + "\n"
        + hashlib.sha256(canonical.encode()).hexdigest()
    )

    def step(key, msg):
        return hmac.new(key, msg.encode(), hashlib.sha256).digest()

    key = step(step(step(step(("OSC4" + secret).encode(), day), region), "api"), "osc4_request")
    signature = hmac.new(key, to_sign.encode(), hashlib.sha256).hexdigest()
    return {
        "Content-Type": "application/json",
        "X-Osc-Date": stamp,
        "Authorization": (
            f"OSC4-HMAC-SHA256 Credential={access}/{scope}, "
            f"SignedHeaders=content-type;host;x-osc-date, "
            f"Signature={signature}"
        ),
    }


def call_read(access, secret, region, host, call):
    # The guard is here, at the only place a request is built, rather than in
    # the caller: a control that lives where the effect happens cannot be
    # bypassed by a new caller who did not know about it.
    if not call.startswith("Read"):
        raise ValueError(f"refusing {call}: this reader is read-only")

    body = b"{}"
    url = f"https://{host}/api/v1/{call}"
    # The scheme is checked rather than assumed: `host` comes from a
    # configuration file, and a urlopen that accepted file:// or a custom scheme
    # would turn a bad profile into a local file read. Bandit B310 is right to
    # ask, and the answer is a guard rather than a suppression.
    if not url.startswith("https://"):
        raise ValueError(f"refusing a non-HTTPS endpoint: {url}")
    req = urllib.request.Request(
        f"https://{host}/api/v1/{call}",
        data=body,
        headers=sign(access, secret, region, host, call, body),
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=45) as r:  # nosec B310 — https enforced above
            return r.status, json.load(r)
    except urllib.error.HTTPError as exc:
        try:
            return exc.code, json.load(exc)
        except Exception:
            return exc.code, None


# PACE is the gap between calls, and it is not politeness.
#
# Firing this list back to back made Outscale answer InvalidParameterValue 4120
# on every authenticated call while the public ones kept working — which reads
# exactly like a revoked credential and is not one. Measured: the same
# operations, the same key, the same signature, spaced out, all answer 200.
#
# The failure mode is what makes it worth a constant: a throttle that reports
# itself as a parameter error, on the authenticated half only, sends a reader
# looking at their credentials for an hour.
PACE = 0.4


def main():
    out_path = sys.argv[1]
    profile, access, secret, region = load_profile(os.environ.get("OSC_PROFILE"))
    host = f"api.{region}.outscale.com"
    print(f"   profile {profile}, region {region}, direct to {host}", flush=True)

    ok = 0
    with open(out_path, "w") as out:
        for call in READS:
            print(f"   {call:34} ", end="", flush=True)
            try:
                status, body = call_read(access, secret, region, host, call)
            except Exception as exc:
                print(f"error: {type(exc).__name__}")
                continue
            print("ok" if 200 <= status < 300 else f"HTTP {status}")
            time.sleep(PACE)
            if 200 <= status < 300:
                ok += 1
            out.write(
                json.dumps(
                    {
                        "t": "1970-01-01T00:00:00Z",
                        "method": "POST",
                        "path": f"/api/v1/{call}",
                        "operation": f"osc/Client.{call}",
                        "provider": "outscale",
                        "status": status,
                        "ms": 0,
                        "mounted": True,
                        "res": {"body": body},
                    },
                    separators=(",", ":"),
                )
                + "\n"
            )
    print(f"   {ok} of {len(READS)} answered", flush=True)


main()
