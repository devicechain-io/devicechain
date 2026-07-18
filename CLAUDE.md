# DeviceChain — Repository Guide

## Repository layout

A Go monorepo using **Go Workspaces** (`go.work`, Go 1.26). The workspace modules are:

```
backend/
  core/                       shared library — entity, auth, messaging, config, rdb, graphql, secrets
                              (pluggable envelope-encrypted secret store, ADR-059)
  services/                   one module per microservice:
    device-management/        devices, device types + versioned device profiles (un-fused, ADR-045),
                              the typed relationship graph, alarm objects as level-state integrators
                              (ADR-057; detection/authoring now live in event-processing), event resolution
    user-management/          identities, per-tenant memberships, roles, two-tier JWT/JWKS; OAuth 2.1
                              authorization server (PKCE, RFC 8414/8707) securing MCP access (ADR-047);
                              tenant TIER — operator-defined packaging entity subsystems READ but never
                              redefine (governance ceilings + AI model entitlement, ADR-065); tenant
                              branding/white-label control record (ADR-038)
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
    event-processing/         DETECT + REACT pipeline extracted from device-management (ADR-051); the
                              SOLE live alarm engine (measurement evaluator retired, ADR-057 cutover).
                              Resolved-events tap → keyed-streaming CEL detection core (owned, cel-go
                              predicates) → REACT actions (raise-alarm, send-command). Rules authored on
                              the profile as forms, on a visual automation canvas w/ replay preview (ADR-053),
                              or by a natural-language "Describe" door that compiles via ai-inference (ADR-056)
    outbound-connectors/      dedicated REACT outbound sink (ADR-060): durable NATS consumer of the REACT
                              connector-dispatch stream → hand-rolled SSRF-hardened httpCall webhook +
                              embedded-Bento (MIT warpstreamlabs/bento, kept out of the DETECT binary)
                              publish to MQTT/Kafka/AWS-SNS/AWS-SQS over a versioned Connector entity,
                              secret-authed (ADR-059) + per-tenant egress-governed (ADR-023)
    ai-inference/             opt-in inference service (ADR-056): drafts a DETECT rule from natural language and
                              runs it through the SAME cel-go compiler (bounded compile/repair loop) — AI proposes,
                              compiler disposes, never in the replay-correct path. Operator-registered AIProviders
                              w/ write-only key handles; external use per-tenant opt-in + fail-closed; the model in
                              use is a (tenant,function)→model assignment falling back to the tenant TIER's default
                              (ADR-065) — server never infers a default, no menu ⇒ no model. Two GraphQL planes:
                              tiny tenant data plane (inferRuleCandidate) + identity-token /admin/graphql
    mcp/                      opt-in OAuth 2.1 Resource Server exposing read-only tools (devices, state,
                              telemetry, alarms, commands) to AI agents over MCP, fronting per-area GraphQL
                              under the caller's own token — no service token, confused-deputy red line (ADR-047)
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

# C# SDK (public .NET/Unity client SDK — sdks/csharp, ADR-035 slice 3; AOT/IL2CPP-safe)
cd sdks/csharp && dotnet build -c Release && dotnet test -c Release

# Unity package tier-1 check (sdks/unity, ADR-035 slice 4): compile the package C# against real
# UnityEngine reference assemblies in both platform branches — no Editor/license needed.
sdks/unity/tools/UnityCompileCheck/compile-check.sh

# helm
# The chart requires an instance root key (ADR-059) for any profile carrying a
# secret-store area (notification-management is in the `default` profile), so a bare
# render needs a throwaway one. dcctl bootstrap mints the real one.
helm lint deploy/helm/devicechain
helm template deploy/helm/devicechain \
  --set "instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)" >/dev/null

# opentofu
cd deploy/opentofu && tofu fmt -check -recursive && tofu init -backend=false && tofu validate
```

## Conventions

- **Schema/migrations — two rules, and they are not negotiable:**
  1. **A migration declares its OWN structs, never the live models.** A migration's shapes are a
     snapshot of a point in time; the live models are the current incarnation of the same datatypes.
     A migration that points at a live model is silently rewritten whenever that model changes — which
     breaks *fresh* installs (`column already exists`) while every existing database applies cleanly
     and looks healthy. Seeds count: insert through the snapshot, with literal values.
  2. **Never edit an existing migration.** Append a new one; flatten before GA. A flatten creates the
     final structure directly — each table once, no `ALTER`s inside it — and carries no version
     suffixes (post-flatten they are all v1).

  `hack/migration-diff.sh verify` is the ONLY thing that exercises the migrations at all (the unit
  tests AutoMigrate live structs on SQLite and never run a chain). It runs in CI. Maintainers: the
  reasoning, the recipes and the gorm traps are in `.agent-os/product/data-modeling.md`.
- **Pre-GA (v1.0.0):** all models and APIs are changeable. Prefer decisive cutovers over compat shims,
  backfills, or migration scaffolding for old shapes.
- **Fail closed:** typed config rejects unknown/invalid keys at startup; the DB tenant-scope callback
  rejects any tenant-scoped query with no tenant in context.
- **Multi-tenancy:** a single shared set of services serves all tenants; isolation is enforced at the
  storage (`tenant_id` predicate) and messaging (per-tenant subjects) layers, not by per-tenant pods.

## Forked dependencies

**`github.com/graph-gophers/graphql-go` → `github.com/devicechain-io/graphql-go@v1.10.2-dc.2`**

The upstream library silently discards input-object entries the schema does not define when the
value arrives through a **variable**. (Values written as *literals* in the query are rejected
correctly; every real client — console, SDKs, dcctl, codegen — sends variables.) The GraphQL spec
requires a request error here, so this is a fail-open: a misnamed field returns success and
produces a half-configured entity indistinguishable from a correct one. It bit us live — sending
`deviceProfileToken` instead of `profileToken` created a device type with no profile, which left
its devices with no command vocabulary and made a correct enqueue gate look broken.

The fork carries exactly one commit on top of the upstream `v1.10.2` **tag** (branch
`dc-release-v1.10.2`), mirroring the iteration direction the library's own literal path already
uses. Cutting from the tag rather than upstream `main` is deliberate: `main` carries unreleased
changes — including a refactor of the resolver execution path — that we would otherwise be running
in production without having chosen them. The same patch sits on `fix/reject-unknown-input-object-fields-in-variables`,
branched from `main`, which is what the upstream PR proposes. When it lands in a release, **drop
every `replace` and the CI guard, and bump to that release** — nothing else depends on the fork.

Note the fork inherits upstream's tags, so `devicechain-io/graphql-go@v1.10.2` exists and is
*unpatched*. Only the `-dc.N` tags carry the fix, which is why the CI guard matches the exact
version and not just the module path.

Two things to know:

1. A `replace` applies **only in the main module**, so each of the 12 modules that compile
   graphql-go (core plus the 11 GraphQL-serving services) needs its own. Four more — `cli`,
   `services/mcp`, `sims/dc-simulator`, `tools/migrationdiff` — carry the replace too. They resolve
   graphql-go transitively through core but currently compile none of it (`mcp` fronts GraphQL over
   HTTP rather than executing a schema), so their replace is defensive: it means the day one of them
   *does* import the library, it gets the patched one rather than silently getting upstream. Because
   they have no graphql-go line in their `go.mod`, a grep cannot find them — which is exactly how a
   module ends up on the unpatched library unnoticed. The `graphql-go fork guard` step in the `go`
   CI job enforces this per module with `GOWORK=off`, so a module that loses its replace fails CI
   instead of reverting silently.
2. Dependabot does not track the fork, so upstream security advisories for graphql-go will not
   raise an alert here. Re-check it by hand when reviewing GraphQL-layer CVEs.

The behaviour is pinned by `TestUnknownInputFieldIsRejected` and `TestValidInputIsNotRejected` in
[backend/core/graphql/unknown_input_fields_test.go](backend/core/graphql/unknown_input_fields_test.go).
The second test is the counterweight: rejecting unknown fields is only safe while well-formed input
still passes untouched.

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
