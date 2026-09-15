#!/usr/bin/env bash
# Copyright Authors of Cilium
# SPDX-License-Identifier: Apache-2.0
#
# Repeatable build & (optional) push of the mock-cilium-agent image, pinned
# to whatever Cilium OSS source commit/tag is currently checked out.
#
# This is the process to use whenever the Cilium version this mock agent
# tracks changes (new upstream release, or a rebase of this branch onto a
# newer cilium/main): re-run this script from a checkout at the desired
# commit/tag. The resulting image tag always encodes that commit, so builds
# are reproducible and retrievable without ad hoc pushes.
#
# Usage:
#   ./cmd/mock-cilium-agent/build.sh [--push] [--registry REGISTRY]
#
# Env overrides:
#   REGISTRY   (default: ghcr.io/srodi/mock-cilium-agent)
#   PLATFORM   (default: linux/amd64)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${REPO_ROOT}"

REGISTRY="${REGISTRY:-ghcr.io/srodi/mock-cilium-agent}"
PLATFORM="${PLATFORM:-linux/amd64}"
PUSH=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --push) PUSH=1; shift ;;
    --registry) REGISTRY="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

# Version = the exact cilium/cilium commit this image was built from, so the
# tag alone tells you which upstream state produced it.
CILIUM_VERSION="$(git describe --tags --always --dirty)"
CILIUM_SHA="$(git rev-parse --short HEAD)"
IMAGE_TAG="${REGISTRY}:${CILIUM_VERSION}"
IMAGE_TAG_SHA="${REGISTRY}:sha-${CILIUM_SHA}"

echo "Building mock-cilium-agent"
echo "  cilium source version: ${CILIUM_VERSION} (${CILIUM_SHA})"
echo "  image tags:            ${IMAGE_TAG}, ${IMAGE_TAG_SHA}, ${REGISTRY}:latest"

docker build \
  --platform "${PLATFORM}" \
  --build-arg "CILIUM_VERSION=${CILIUM_VERSION}" \
  -f cmd/mock-cilium-agent/Dockerfile \
  -t "${IMAGE_TAG}" \
  -t "${IMAGE_TAG_SHA}" \
  -t "${REGISTRY}:latest" \
  .

if [[ "${PUSH}" -eq 1 ]]; then
  docker push "${IMAGE_TAG}"
  docker push "${IMAGE_TAG_SHA}"
  docker push "${REGISTRY}:latest"
fi

echo "Done. Built: ${IMAGE_TAG}"
