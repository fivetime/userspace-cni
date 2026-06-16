# syntax=docker/dockerfile:1.7
###############################################################################
# Stage 1: builder — compile the userspace CNI binary
#
# Notes:
#   1) ARG TARGETOS/TARGETARCH/BUILDOS/BUILDARCH are auto-injected by
#      docker buildx during multi-arch builds; the output binary platform
#      matches what buildx asks for.
#   2) CGO_ENABLED=0 produces a fully static binary — runtime image needs
#      no libc.
#   3) No `make generate` step — the cnivpp side now imports VPP binapi
#      packages directly from go.fd.io/govpp/binapi/*, so there is no
#      local code generation pass and no VPP toolchain dependency.
#   4) The legacy docker/userspacecni/Dockerfile used ligato/vpp-base
#      (~500 MB) for the builder. This Dockerfile drops that entirely.
###############################################################################
FROM --platform=$BUILDPLATFORM golang:1.25-alpine3.23 AS builder

ARG VERSION="dev"

# Compatibility with `make docker-build goarch=...` invocations.
ARG goarch=

ARG TARGETOS
ARG TARGETARCH
ARG BUILDOS
ARG BUILDARCH

ENV GOOS=${TARGETOS:-linux} \
    GOARCH=${goarch:-${TARGETARCH:-amd64}} \
    CGO_ENABLED=0

WORKDIR /workdir

COPY . .

RUN set -eux \
    && go build -ldflags="-s -w -X main.version=${VERSION}" -o /workdir/bin/userspace ./userspace

###############################################################################
# Stage 2: runtime — minimal Alpine, single binary
#
#   - No findutils / ca-certificates needed: userspace CNI doesn't run a
#     liveness probe with `find -mmin`, and it doesn't call any K8s API
#     directly (annotations are read from a mounted file in the sharedDir
#     mode, kubeconfig mode does call apiserver but kubelet provides the
#     cert path on the host).
#   - CMD copies the binary onto the host's /opt/cni/bin when invoked as
#     an init container in the DaemonSet, matching the legacy image's
#     deployment pattern.
###############################################################################
FROM alpine:3.23

ARG VERSION="dev"

LABEL org.opencontainers.image.title="userspace-cni" \
      org.opencontainers.image.description="Userspace CNI plugin (OVS-DPDK + VPP backends, Alpine build)" \
      org.opencontainers.image.source="https://github.com/fivetime/userspace-cni" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="Apache-2.0"

COPY --from=builder /workdir/bin/userspace /userspace

CMD ["cp", "-f", "/userspace", "/opt/cni/bin/userspace"]
