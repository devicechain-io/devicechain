#!/usr/bin/env bash
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Build the operand image for the local architecture, from the pins in
# versions.conf. One place that knows how to turn versions.conf into build args,
# because the alternative is three copies of the same --build-arg list (here,
# smoke.sh, two CI workflows) drifting apart until one of them builds something
# nobody asked for.
#
#   ./build.sh [tag]     # default tag: dc-tsdb-operand:local
#
# Multi-arch publishing is NOT done here — that is docker/build-push-action in
# .github/workflows/operand-image.yml, which needs buildx's container driver.
# This builds into the local Docker daemon so the result can actually be RUN.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
set -a; . "$HERE/versions.conf"; set +a

TAG="${1:-dc-tsdb-operand:local}"

echo "==> Building ${TAG}"
echo "    base:        ${PG_IMAGE}"
echo "    postgres:    ${PG_MAJOR}"
echo "    timescaledb: ${TIMESCALEDB_VERSION}"
echo "    compat:      ${TIMESCALEDB_COMPAT_VERSIONS:-<none>}"

# --load is explicit rather than implied. Under buildx's docker-container driver
# (which setup-buildx-action installs in CI) a build without it produces an image
# that exists only in the build cache — so `docker run` fails with "image not
# found" on an image that just built successfully.
#
# Probe whether THIS `docker build` accepts the flag, rather than whether a
# buildx plugin happens to be installed. Those are different questions: with the
# plugin present but BuildKit disabled (DOCKER_BUILDKIT=0, or Docker < 23 with a
# sideloaded plugin) `docker build` is still the legacy builder and dies with
# `unknown flag: --load`. Asking the binary what it supports cannot be wrong.
load=()
if docker build --help 2>/dev/null | grep -q -- '--load'; then
  load=(--load)
fi

# "${load[@]}" on an empty array is an unbound-variable error under `set -u` on
# bash < 4.4 (stock macOS is 3.2), hence ${load[@]+"${load[@]}"}.
docker build ${load[@]+"${load[@]}"} \
  --build-arg PG_IMAGE="$PG_IMAGE" \
  --build-arg PG_MAJOR="$PG_MAJOR" \
  --build-arg TIMESCALEDB_VERSION="$TIMESCALEDB_VERSION" \
  --build-arg TIMESCALEDB_COMPAT_VERSIONS="$TIMESCALEDB_COMPAT_VERSIONS" \
  -t "$TAG" "$HERE"

echo "==> Built ${TAG}"
