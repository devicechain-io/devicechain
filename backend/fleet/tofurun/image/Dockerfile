# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0

# The fleet runner image: OpenTofu at ONE exact version, the fleet roots baked in with their
# provider lock files, the providers baked into a filesystem mirror, and the runner binary. Nothing
# else: no terraform, no gcloud, no kubectl, no shell, no package manager.
#
#   - The roots are baked, never fetched at run time. Their digest is what a run records
#     (tofurun.RunInfo.ModuleDigest), so a run is bound to the tree it executed.
#   - The providers are mirrored at build time and tofurun's CLI config installs from the mirror
#     only (`direct { exclude = ["*/*/*"] }`), so a running Job never reaches a registry.
#   - tofu is a static Go binary, so the final image can be distroless/static and carries no libc.
#
# Build from the REPOSITORY ROOT (the roots live under deploy/fleet and the runner under
# backend/fleet):
#
#   docker build -f backend/fleet/tofurun/image/Dockerfile -t fleet-runner .
#
# This image runs nothing but `fleet-runner check` in this build: the manager's runner API (claim,
# heartbeat, facts, go) does not exist yet, and any other invocation exits 78 saying so.
#
# Bases are pinned by digest and the directory is watched by Dependabot's docker ecosystem, so both
# roll forward. TOFU_VERSION is the premise record's version (tofurun.PinnedVersion; a test in this
# directory keeps the two equal), and each architecture's archive is verified against a SHA-256
# taken from the release's own SHA256SUMS.

FROM golang:1.27.4@sha256:205c7636d74e0ffbe3d007162df7d74f859f5a8c8322dfb9f907977686c126cf AS build

ARG TOFU_VERSION=1.10.10
ARG TOFU_SHA256_AMD64=6c839b92be842e190083a1616226191f6d61bc4b7be5f5a30f09851b5318234c
ARG TOFU_SHA256_ARM64=936ff281d42fe34e01da5bf334ad15b65aa80175b3f4a34adffefba963cd2590
ARG TARGETARCH=amd64

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip && rm -rf /var/lib/apt/lists/*

# OpenTofu, verified.
RUN set -eu; \
    case "${TARGETARCH}" in amd64) sum="${TOFU_SHA256_AMD64}" ;; arm64) sum="${TOFU_SHA256_ARM64}" ;; *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;; esac; \
    curl -fsSL -o /tmp/tofu.zip "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.zip"; \
    echo "${sum}  /tmp/tofu.zip" | sha256sum -c -; \
    mkdir -p /out/usr/local/bin; \
    unzip -q /tmp/tofu.zip tofu -d /out/usr/local/bin; \
    rm /tmp/tofu.zip; \
    /out/usr/local/bin/tofu version | grep -q "^OpenTofu v${TOFU_VERSION}$"

# The tree: the roots AND the module they reference by relative path
# ("../../opentofu/modules/gke-cluster"), laid out as in the repository so the reference resolves.
# Each root's COMMITTED lock file (linux_amd64 and linux_arm64 hashes) is what the build installs
# under: `-lockfile=readonly` fails the build if the provider does not match it, and never rewrites
# it. The providers are then mirrored for the platform this image runs on, and `fleet-runner check`
# re-initialises every root offline from that mirror.
COPY deploy/fleet/gke-gcs /out/opt/fleet/tree/fleet/gke-gcs
COPY deploy/fleet/gke-pg /out/opt/fleet/tree/fleet/gke-pg
COPY deploy/opentofu/modules/gke-cluster /out/opt/fleet/tree/opentofu/modules/gke-cluster
# Tests are not part of a baked root.
RUN set -eu; \
    rm -rf /out/opt/fleet/tree/fleet/*/tests; \
    for r in /out/opt/fleet/tree/fleet/*; do \
      ( cd "$r" && \
        /out/usr/local/bin/tofu init -backend=false -input=false -no-color -lockfile=readonly && \
        /out/usr/local/bin/tofu providers mirror -platform="linux_${TARGETARCH}" /out/opt/fleet/providers && \
        rm -rf .terraform ); \
    done

# The runner (a static binary).
WORKDIR /src
COPY backend/core/go.mod backend/core/go.sum /src/backend/core/
COPY backend/fleet /src/backend/fleet
RUN cd /src/backend/fleet && GOWORK=off CGO_ENABLED=0 go build -trimpath -o /out/usr/local/bin/fleet-runner ./tofurun/cmd/fleet-runner

# Prove the layout before it ships: the check is the one command this image runs.
RUN FLEET_RUNNER_TOFU=/out/usr/local/bin/tofu FLEET_RUNNER_TREE=/out/opt/fleet/tree /out/usr/local/bin/fleet-runner check

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/ /
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/fleet-runner"]
CMD ["check"]
