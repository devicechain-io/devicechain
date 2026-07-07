# DeviceChain — Repository Guide

## Repository layout

A Go monorepo using **Go Workspaces** (`go.work`, Go 1.26). The workspace modules are:

```
backend/
  core/                       shared library — entity, auth, messaging, config, rdb, graphql
  services/                   one module per microservice:
    device-management/        devices, device types + versioned device profiles (un-fused, ADR-045),
                              the typed relationship graph, the alarm engine (ADR-041), event resolution
    user-management/          identities, per-tenant memberships, roles, two-tier JWT/JWKS
    event-management/         persists + queries time-series events (TimescaleDB hypertables;
                              data-lifecycle reconciler + measurement_rollups continuous aggregate, ADR-026)
    event-sources/            inbound device transports (MQTT/NATS), decode → pipeline; per-tenant
                              ingest rate limiting w/ platform-default ceiling (ADR-023)
    device-state/             live last-known-state projection per device
    command-delivery/         persistent two-way command dispatch (typed command defs on the profile, ADR-043)
    dashboard-management/     dashboard CRUD + versioning (draft/publish/rollback); definitions are
                              versioned tenant resources stored as opaque JSON (ADR-039)
    notification-management/  alarm→human last mile: per-tenant policy, SMTP + webhook adapters,
                              HA-safe escalation scheduler (ADR-017)
  k8s/                        controller-runtime operator (Instance CRD; tenants are control-plane DB rows, ADR-033)
  cli/                        dcctl — bootstrap/destroy + admin tooling
deploy/                       Helm chart (deploy/helm) + OpenTofu modules (deploy/opentofu)
frontend/                     npm workspace (React 19 + Vite + Tailwind + shadcn/ui, client-preset codegen):
                              apps/console (authoring — canvas editor, versioning, synthetic preview, slot
                              authoring, export) + apps/dashboard (the /dash app — a VIEWER-ONLY reference
                              external embedder with its own login) + packages/{client,dashboards,widgets}
                              (SDK, dashboard runtime + slot/binding-manifest model, ECharts widgets; ADR-039)
docs/                         Docusaurus site
hack/                         license header + dev scripts
_legacy/                      archived pre-migration SiteWhere code — NOT in the workspace, not built; do not edit
```

## Planning & decisions — read before proposing architecture

> **`.agent-os/` is a gitignored symlink to a private planning workspace** — it exists only in
> maintainer checkouts, not in a public clone, so the paths below resolve only when that workspace is
> linked. The durable architectural rationale is cited inline across the codebase as `ADR-0xx`.

Source-of-truth planning docs (maintainer-only) live under `.agent-os/product/`. Consult them before
designing anything non-trivial; the codebase follows the ADRs.

- **decisions.md** — ADRs (the "why"). Referenced throughout as `ADR-0xx`.
- **roadmap.md** — only **open** (`[ ]`) and **in-progress** (`[~]`) work, organized by phase.
- **shipped.md** — delivered features (the `[x]` log).
- **mission.md** / **mission-lite.md** / **tech-stack.md** — product framing and stack rationale.

The product/strategy narrative docs can be reconciled with the `/sync-product-docs` skill.

## Build, test, and lint (these are the CI gates — run before committing)

Go (from the repo root; the workspace resolves all modules — no vendor step):

```bash
gofmt -l .          # must print nothing
go build ./...
go vet ./...
go test ./...
```

Other areas:

```bash
# dcctl
cd backend/cli && make build

# frontend
cd frontend && npm ci && npm run codegen && npm run typecheck && npm run build

# helm
helm lint deploy/helm/devicechain && helm template deploy/helm/devicechain >/dev/null

# opentofu
cd deploy/opentofu && tofu fmt -check -recursive && tofu init -backend=false && tofu validate
```

## Conventions

- **Pre-GA (v1.0.0):** all models and APIs are changeable. Prefer decisive cutovers over compat shims,
  backfills, or migration scaffolding for old shapes.
- **Fail closed:** typed config rejects unknown/invalid keys at startup; the DB tenant-scope callback
  rejects any tenant-scoped query with no tenant in context.
- **Multi-tenancy:** a single shared set of services serves all tenants; isolation is enforced at the
  storage (`tenant_id` predicate) and messaging (per-tenant subjects) layers, not by per-tenant pods.

## License headers

DeviceChain is licensed under **Apache License 2.0** (see [LICENSE](LICENSE) and [NOTICE](NOTICE)).

**Every source file must begin with this two-line SPDX header:**

```go
// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0
```

Notes:
- Copyright is held by **"The DeviceChain Authors"** — there is no single corporate owner. Do not add a year and do not attribute to any individual or company.
- New `.go` files **must** include the header (followed by a blank line, then the build tags / `package` clause).
- For files with `//go:build` constraints, the header goes **above** the build tag, separated by a blank line.
- Generated code carries the same header. The controller-gen boilerplate lives at
  [backend/k8s/hack/boilerplate.go.txt](backend/k8s/hack/boilerplate.go.txt); keep it in sync with the header above.

To apply or check headers in bulk:

```bash
# add header to any missing files
go run github.com/google/addlicense@latest -f hack/license-header.txt backend

# verify all files have it (CI will enforce this)
go run github.com/google/addlicense@latest -check -f hack/license-header.txt backend
```

(The CI `license` job enforces this on every PR and push to `main` — see
[.github/workflows/ci.yml](.github/workflows/ci.yml). It checks Go + hand-authored
GraphQL/proto sources; controller-gen-generated YAML and the operator Dockerfile are
excluded because a header would drift away on every regeneration.)
