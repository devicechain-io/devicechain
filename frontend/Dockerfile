# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# Two-stage build for the DeviceChain web console: compile the Vite/React SPA to
# static assets, then serve them from a hardened, unprivileged nginx.
#
# The SPA is a pure static bundle — it talks to the backend at runtime over
# relative /api/<area>/graphql URLs (see frontend/src/lib/graphql/client.ts),
# which the cluster ingress routes to each functional-area service. Nothing is
# baked in at build time, so one image serves any instance.

# Base images are pinned by digest as well as tag: a tag is mutable, so an
# unpinned rebuild of an unchanged commit can pick up a different base. The
# digests below are the multi-arch INDEX digests, so pinning them still lets this
# image build for both amd64 and arm64. Dependabot's `docker` ecosystem covers
# this directory and rolls both the tag and the digest forward weekly — without
# that, a digest pin would freeze the base at whatever was current when it was
# written, which is worse for security than a floating tag.

# --- build stage: produce frontend/dist ------------------------------------
# 🔴 --platform=$BUILDPLATFORM IS LOAD-BEARING, AND ITS ABSENCE COST A RELEASE.
# What this stage emits is `vite build` output — HTML, JS and CSS — which is the
# same bytes for every target architecture. Without this pin, buildx runs the whole
# stage ONCE PER TARGET, so `npm ci` and `npm run build` were executed a second time
# under arm64 QEMU emulation to produce a byte-identical dist.
#
# It is not merely wasted: emulating a JIT is where it breaks. On 2026-08-22 the
# v0.12.0 release hung because arm64 `npm ci` took `qemu: uncaught target signal 4
# (Illegal instruction) - core dumped` 45s in — and buildx did not propagate the
# child's SIGILL, so the step neither failed nor progressed. With no timeout on the
# job it would have sat for the 6-hour default looking exactly like slow work. The
# same commit had built cleanly 11 hours earlier, so it is an intermittent emulation
# fault, not a regression — which is precisely why removing the emulation is the fix
# rather than a retry.
#
# Only the serve stage below is genuinely per-architecture: it carries an nginx
# binary. This stage carries none, so it belongs on the builder.
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:aadf416b2cdce311a8811ba3f0608a61b77dbf997500e2eafe781b51f6a0b019 AS build
WORKDIR /app

# Install deps against the lockfile first so the layer caches across source-only
# changes. This is an npm workspace (apps/*, packages/*), so every member's
# manifest must be present for `npm ci` to resolve the workspace tree; copy them
# alongside the root before the source. npm ci needs devDependencies (tsc, vite)
# — do not pass --omit=dev.
COPY package.json package-lock.json ./
COPY apps/console/package.json apps/console/
COPY apps/dashboard/package.json apps/dashboard/
COPY packages/client/package.json packages/client/
COPY packages/dashboards/package.json packages/dashboards/
COPY packages/widgets/package.json packages/widgets/
RUN npm ci

COPY . .
# Root `build` runs every workspace's build (`tsc --noEmit && vite build`): the
# console (served at /) and the dashboard app (served at /dash, base '/dash/').
# The generated GraphQL client under apps/console/src/gql is committed, so no
# codegen step is needed here. Output lands at apps/<app>/dist.
RUN npm run build

# --- serve stage: static nginx ---------------------------------------------
# nginx-unprivileged listens on 8080 as uid 101 and writes its pid + temp paths
# under /tmp, so it runs cleanly as non-root with a read-only root filesystem
# (the chart mounts an emptyDir at /tmp). See deploy/helm templates/frontend.yaml.
# Both SPAs ship in one image: the console at the web root, the dashboard app under
# /dash (its own SPA-fallback tree). The shared ingress routes everything non-/api
# here, so /dash needs no separate ingress rule.
FROM nginxinc/nginx-unprivileged:1.31-alpine@sha256:d9083fe47768377ef55dedafd67d4da7c2f2bc2bece7554954f29359deb0dce9
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/apps/console/dist /usr/share/nginx/html
COPY --from=build /app/apps/dashboard/dist /usr/share/nginx/html/dash
EXPOSE 8080
