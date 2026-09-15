# check=error=true
# Copyright Authors of Cilium
# SPDX-License-Identifier: Apache-2.0
#
# Lightweight, repeatable build for mock-cilium-agent.
#
# Unlike images/cilium/Dockerfile, this does NOT need cilium-builder/-runtime,
# BPF toolchains, or Envoy: the mock agent never loads BPF, iptables, or the
# proxy (option.Config.DryMode=true), so a plain Go toolchain is sufficient.
#
# Build args let this be rebuilt reproducibly against any point-in-time
# checkout of the cilium/cilium source tree (see cmd/mock-cilium-agent/README.md
# "Build & release process").
ARG GO_IMAGE=golang:1.26-bookworm

FROM --platform=${BUILDPLATFORM} ${GO_IMAGE} AS builder
ARG TARGETOS
ARG TARGETARCH
# Set by the build script to `git -C <cilium-checkout> describe --tags --always --dirty`.
# Baked into the binary's version output for traceability of which upstream
# commit/tag the image was produced from.
ARG CILIUM_VERSION=unknown

WORKDIR /go/src/github.com/cilium/cilium
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -ldflags "-X github.com/cilium/cilium/pkg/version.ciliumVersion=${CILIUM_VERSION}-mock" \
      -o /out/mock-cilium-agent \
      ./cmd/mock-cilium-agent

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=builder /out/mock-cilium-agent /usr/bin/mock-cilium-agent
ENTRYPOINT ["/usr/bin/mock-cilium-agent"]
