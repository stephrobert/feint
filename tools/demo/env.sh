#!/usr/bin/env bash
# The environment the official Scaleway client needs to reach a local emulator.
#
# It is sourced by the demo tape rather than typed into it: six exports of values
# that mean nothing teach a viewer nothing, and the apply on screen needs them —
# the first recording of docs/assets/ci.gif failed precisely because they were
# missing, and the failure was published.
#
# The values are deliberately public and deliberately meaningless. The emulator
# verifies no credential; what the SDK *does* check is the shape, which is why
# the access key looks like SCWXXXXXXXXXXXXXXXXX and the secret is a UUID. The
# same pair lives in tools/conformance/scaleway/fake-credentials.env, which
# CONTRIBUTING.md and SECURITY.md both name as intentionally published.
#
# `feint env scaleway` prints the same thing from the binary and is what a reader
# should use. This file exists because a tape cannot run the binary before the
# container it is about to start.

export SCW_ACCESS_KEY=SCWXXXXXXXXXXXXXXXXX
export SCW_SECRET_KEY=11111111-1111-1111-1111-111111111111
export SCW_DEFAULT_PROJECT_ID=11111111-1111-1111-1111-111111111111
export SCW_DEFAULT_ORGANIZATION_ID=11111111-1111-1111-1111-111111111111
export SCW_DEFAULT_ZONE=fr-par-1
export SCW_DEFAULT_REGION=fr-par
export SCW_API_URL=http://127.0.0.1:4599
export SCW_INSECURE=true
