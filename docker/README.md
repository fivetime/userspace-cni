# userspace-cni container image (Alpine build)

This directory builds the `userspace-cni` plugin image on **Alpine** instead
of the upstream `docker/userspacecni/Dockerfile` which uses
`ligato/vpp-base:24.02` (~500 MB) as the builder. Published to
`ghcr.io/fivetime/userspace-cni`.

| Tag | Meaning |
| --- | --- |
| `main` | Newest commit on `main` |
| `<git-sha>` | 8-char short SHA, matches `git log --oneline` |

Images are multi-arch: `linux/amd64`, `linux/arm64`. ppc64le / s390x are
skipped because DPDK upstream support for those archs is partial.

> **Note:** The upstream `intel/userspace-cni-network-plugin` repository
> has been archived. This fork has diverged and follows its own roadmap;
> there is no automated upstream sync and no upstream-derived release
> tags. The image is rolled forward by pushes to `main`.

## Why this Dockerfile exists

The upstream `docker/userspacecni/Dockerfile` pulls `ligato/vpp-base` and
runs `make generate` to build the VPP binapi Go bindings from VPP API JSON
files shipped in that image. After this fork's refactor, the cnivpp side
imports VPP binapi directly from `go.fd.io/govpp/binapi/*` (govpp ships
pre-generated packages for every VPP API), so:

- no `make generate` step
- no VPP toolchain at build time
- builder image is `golang:1.25-alpine3.23` (~250 MB) instead of
  `ligato/vpp-base:24.02` (~500 MB)
- runtime image is `alpine:3.23` (~10 MB) with a single static binary

Combined with `CGO_ENABLED=0`, the produced runtime image is around 25 MB
versus 500+ MB upstream.

## What's the same

- Binary path inside the image: `/userspace`
- `CMD` copies it to `/opt/cni/bin/userspace` (init-container pattern,
  same as the legacy image)
- `userspace` is the unchanged plugin name — drop-in compatible with the
  upstream DaemonSet manifest, just swap the image reference

## Building locally

The build context must be the repo root (the Dockerfile does `COPY . .`):

```bash
# from repo root
docker build -f docker/Dockerfile -t userspace-cni:dev .
```

Multi-arch (matches what CI does):

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f docker/Dockerfile \
  -t ghcr.io/fivetime/userspace-cni:dev \
  --push .
```

## Deploying

CNI binaries land on the host via the DaemonSet's init container — the
image is not a long-running process. See `example.yml` for a minimal
NetworkAttachmentDefinition + business Pod that consumes a memif socket
from a node-local VPP. The upstream `examples/` directory has more
elaborate samples (testpmd, VPP-to-VPP memif ping, etc.) which work
unchanged against this image.

The CNI plugin's job here is creating the socket file (memif or
dpdkvhostuser) in the sharedDir. **What the in-pod process does with that
socket — DPDK libmemif, VPP host stack, raw vhost-user, EVPN PE software,
etc. — is a business-side concern outside the CNI's scope.**
