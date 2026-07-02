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

# --- build stage: produce frontend/dist ------------------------------------
FROM node:22-alpine AS build
WORKDIR /app

# Install deps against the lockfile first so the layer caches across source-only
# changes. This is an npm workspace (apps/*, packages/*), so every member's
# manifest must be present for `npm ci` to resolve the workspace tree; copy them
# alongside the root before the source. npm ci needs devDependencies (tsc, vite)
# — do not pass --omit=dev.
COPY package.json package-lock.json ./
COPY apps/console/package.json apps/console/
COPY packages/client/package.json packages/client/
RUN npm ci

COPY . .
# Root `build` delegates to the console workspace (`tsc --noEmit && vite build`);
# the generated GraphQL client under apps/console/src/gql is committed, so no
# codegen step is needed here. Output lands at apps/console/dist.
RUN npm run build

# --- serve stage: static nginx ---------------------------------------------
# nginx-unprivileged listens on 8080 as uid 101 and writes its pid + temp paths
# under /tmp, so it runs cleanly as non-root with a read-only root filesystem
# (the chart mounts an emptyDir at /tmp). See deploy/helm templates/frontend.yaml.
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/apps/console/dist /usr/share/nginx/html
EXPOSE 8080
