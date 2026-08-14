# The control-plane image: `feint serve` with `--vm off`, and nothing else.
#
# This image emulates the three control planes. It does not and will not start
# machines: a container that promised to run Incus containers from inside
# itself would be exactly the half-truth this project refuses. Anyone who needs
# `--vm` runs the binary on a host with Incus, which is the documented path
# (docs/install.md) and stays so. The image exists so the emulator can enter a
# `services:` block, a compose file or a testcontainers module — never to
# replace the self-detaching binary, which is the one thing none of the
# comparable emulators can do (docs/roadmap.md carries that decision).
#
# The binary is copied in, not built here. Two reasons, both measured:
#
#  1. The bytes in the image are the bytes in the release. The release workflow
#     builds `feint-linux-<arch>` once, checksums it, signs the checksum list
#     and attests its provenance; a second build inside a Dockerfile would be a
#     second artefact with none of those properties, differing in ways nobody
#     audits (toolchain patch level, build flags drifting between two files).
#  2. No second trust root. Building here would mean trusting a `golang` base
#     image from Docker Hub, pinned and refreshed forever, for a compilation
#     that CI already performs with the toolchain it already pins.
#
# `scratch` rather than distroless, and that is measured, not habit: the binary
# is static (CGO_ENABLED=0, enforced by the release build), the control plane
# makes no outbound call (nothing to verify with CA certificates), answers in
# fixed zones it never converts (no tzdata), and runs as a numeric UID (no
# /etc/passwd). Distroless would add ~2 MB that nothing reads. The proof is not
# this comment: the conformance suite runs against this image in CI
# (.github/workflows/conformance.yml, job `image`), and would fail on a missing
# runtime file the way it fails on a wrong response shape.
#
# Build from the repository root, binary first:
#
#   CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/feint-linux-amd64 ./cmd/feint
#   docker build -t feint .
#   docker run --rm -p 127.0.0.1:4599:4599 feint
#
# The multi-arch release build passes TARGETOS/TARGETARCH per platform and a
# VERSION that must match the tag — release.yml refuses to push when the
# binary's own `feint version` disagrees with the tag being released.

FROM scratch

# Predefined by buildx per platform; defaulted so a plain `docker build` works.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev

LABEL org.opencontainers.image.title="feint" \
      org.opencontainers.image.description="Local emulator for the European clouds (Scaleway, Outscale, Exoscale). Control plane only: --vm needs the binary on an Incus host." \
      org.opencontainers.image.source="https://github.com/stephrobert/feint" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

COPY dist/feint-${TARGETOS}-${TARGETARCH} /feint

# Nothing here runs as root, because nothing here needs to: the process binds
# an unprivileged port and writes no file. 65532 is the conventional nonroot
# UID (distroless's), numeric because scratch has no /etc/passwd.
USER 65532:65532

EXPOSE 4599

# `wait` exits 0 when /_feint/health answers and 1 when it does not — `status`
# deliberately exits 0 either way, so it cannot be a health probe. GitHub's
# `services:` and `docker compose` both hold dependents until this passes.
HEALTHCHECK --interval=5s --timeout=3s --start-period=2s --retries=3 \
  CMD ["/feint", "wait", "--addr", "127.0.0.1:4599", "--timeout", "2s"]

ENTRYPOINT ["/feint"]

# `--expose-to-network` is deliberate, and its cost is different here than on a
# workstation. The flag disarms the browser-rebinding guard, which off loopback
# cannot tell a local page from a hostile one; `serve` therefore refuses
# 0.0.0.0 without it. Inside this container the listener must be off loopback
# to be reachable through a published port at all, `--vm` is off (this image
# carries no runtime to protect), and what the network may reach is decided by
# whoever publishes the port (-p 127.0.0.1:4599:4599 keeps it host-local).
# The UI page mounts on loopback only, so this image serves the API and
# /_feint/*, not /_feint/ui.
CMD ["serve", "--addr", "0.0.0.0:4599", "--expose-to-network"]
