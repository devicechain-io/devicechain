---
sidebar_position: 1
title: Bootstrap an Instance
---

# Bootstrap an Instance

`dcctl bootstrap` stands up a complete DeviceChain instance — infrastructure, the
operator, and all the service workloads — with a single command:

```bash
dcctl bootstrap local my-instance
```

`dcctl` is a **self-contained binary**. The OpenTofu infrastructure config, the
Helm chart, and the operator manifests are all embedded inside it, so you do not
need a checkout of the source tree, `git`, `kubectl`, `kustomize`, or `helm` on
your machine — just `dcctl` and a cluster to deploy to.

:::note Status
DeviceChain is pre-release. `dcctl bootstrap local` is implemented and validated
end-to-end on local Kubernetes (kind). The `gcp` provider and automatic local
cluster creation are planned follow-ups — see [Prerequisites](#prerequisites).
:::

## What it does

The bootstrap runs as an ordered, **idempotent** pipeline — re-running it converges
to the same state and tells you which step failed if one does:

1. **Render configuration** — resolve the instance id, namespace, profile, and a
   generated database password.
2. **Apply infrastructure** — `tofu apply` the embedded OpenTofu config (NATS,
   PostgreSQL, TimescaleDB, NGINX ingress, cert-manager) via
   [terraform-exec](https://github.com/hashicorp/terraform-exec). State is kept in
   `~/.devicechain/<instance>/infra`, so subsequent runs are incremental.
3. **Install core** — render the operator (CRDs + RBAC + controller) and apply it
   with the Kubernetes API directly.
4. **Install the instance** — deploy the Helm chart via the Helm Go SDK, blocking
   until the workloads are ready.
5. **Seed & report** — the superuser credential is seeded automatically by the
   user-management service on first start; the command prints it (and the
   namespace and access pointers) at the end.

Because the embedded artifacts are the *same* ones the platform ships, a
bootstrapped instance exercises the real deployment — it cannot drift from a
production deploy.

## Prerequisites

- **A Kubernetes cluster and a kube-context** pointing at it. For the `local`
  provider this is a local cluster (kind / minikube / k3d / docker-desktop).
  `dcctl` auto-detects a local context; pass `--kube-context <name>` to choose one
  explicitly. (Today the `local` provider selects an existing context; creating the
  cluster for you is a planned addition.)
- **OpenTofu** (the `tofu` binary; `terraform` also works) on your `PATH`.
  `dcctl` drives it to provision infrastructure. Install it from
  [opentofu.org](https://opentofu.org). Run `dcctl preflight local` to check this
  and the rest of your environment up front.

## Image source

By default, bootstrap deploys the **published images** from
`ghcr.io/devicechain-io` — nothing to build:

```bash
dcctl bootstrap local my-instance
```

Developers working from a source checkout can build the images from source and
deploy those instead with `--build`, which builds each service and the operator
with [`ko`](https://ko.build) — plus the web console with `docker build` — into a
local registry and deploys by reference:

```bash
# from a source checkout; requires Docker + ko
dcctl bootstrap local my-instance --build
```

The only difference between the two paths is the registry the pods pull from — the
pipeline, chart, and operator are identical.

## Useful flags

| Flag | Purpose |
|------|---------|
| `--kube-context <name>` | Target a specific kube-context (default: auto-detect a local one). |
| `--profile <profile>` | Functional-area profile: `default` (the standard system, used when omitted), `full` (everything — adds AI inference, outbound connectors, and MCP), `telemetry`, or `ingest-only`. |
| `--build` | Build images from source into a local registry (developer path; needs the source tree + Docker + ko). |
| `--registry` / `--version` | Override the image registry / tag (defaults: published `ghcr.io/devicechain-io`, or `localhost:5000` + `dev` with `--build`). |
| `--host <name>` | Ingress host to expose the instance on (default `devicechain.local`). Use `localhost` on a local cluster to reach the console with **no `/etc/hosts` edit**. |
| `--no-tls` | Serve plain HTTP instead of a self-signed cert. With `--host localhost`, a zero-config `http://localhost/` (no cert warning). |
| `--compact` | Small-footprint preset — see below. |
| `--dry-run` | Print what each step would do without changing anything. |
| `--skip-preflight` | Skip the environment checks. |

### `--compact`

A preset for small clusters. It composes levers that already exist rather than adding a
tuning axis of its own:

- lower JetStream and KV per-stream ceilings, and the smaller volumes those permit
  (2Gi JetStream, 2Gi relational Postgres, 4Gi TimescaleDB);
- lower scheduling **requests** (25m / 64Mi), so pods fit a small node — limits are
  untouched, since lowering the memory limit converts pressure into OOMKills and lowering
  the CPU limit throttles, neither of which shrinks anything;
- no monitoring stack, the single largest consumer;
- no cert-manager, since with TLS off nothing needs a certificate issued (keep TLS and
  cert-manager stays — see below).

It does **not** change which services run — that stays on `--profile`, where it is named
and visible. A profile *larger* than `default` — today only `full` — is rejected: the
published compact numbers are measured on `default`, so they would not describe an
instance running three more services. The smaller profiles (`telemetry`, `ingest-only`)
are accepted.

Both TLS and monitoring can be kept: an explicit `--no-tls=false` or `--no-monitoring=false`
is honoured, and every other compact lever still applies. Keeping TLS also keeps
cert-manager, which is what issues the certificate. `--grafana-sso` needs the monitoring
stack Grafana lives in, so it is rejected unless you keep it with `--no-monitoring=false`.

:::caution Volume sizes are a time budget, not a capacity budget
The JetStream volume is derived: the per-stream ceilings are reserved up front, so it is
sized to hold their sum. The two database volumes are not. Nothing prunes the command or
alarm tables, and `retentionDays` defaults to `0` — keep data forever — so on a compact
instance meant to run indefinitely, set a retention window rather than relying on the
volume size.
:::

:::caution Apply it to a fresh cluster
Lowering a ceiling below what a stream or KV bucket already holds succeeds silently,
truncates nothing, and refuses writes until the data ages out. `--compact` is safe on a
first bring-up; it is not the same operation applied to a running instance.
:::

:::tip Zero-config local URL
`dcctl bootstrap local my-instance --build --host localhost --no-tls` exposes the
console at `http://localhost/` — no hosts-file entry and no certificate warning.
:::

## After bootstrap

The command prints the namespace, the **superuser** credential, and how to reach
the instance through the cluster ingress. The superuser is seeded with a default
password — **change it immediately**.

The instance includes the **web console**: the ingress serves it at the host root
(`https://<host>/`) and routes `https://<host>/api/<area>/graphql` to each
functional-area service. Open the console in a browser and sign in with the
superuser's email and password. A fresh instance is **tenant-less**, so you land
in the admin console (`/admin`) to create your first tenant and assign
memberships; switch into a tenant to reach the tenant console. (For a
headless/ingest-only instance, deploy with the console disabled — see the chart's
`frontend.enabled` value.)

To inspect the running instance:

```bash
kubectl --context <kube-context> get pods -n my-instance
```

Load example data into a running instance over the API with:

```bash
dcctl seed construction --server localhost --instance my-instance
```
