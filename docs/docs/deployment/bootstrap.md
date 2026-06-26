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
5. **Seed & report** — the admin credential is seeded automatically by the
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
with [`ko`](https://ko.build) into a local registry and deploys by reference:

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
| `--profile <profile>` | Functional-area profile: `full` (default), `telemetry`, or `ingest-only`. |
| `--build` | Build images from source into a local registry (developer path; needs the source tree + Docker + ko). |
| `--registry` / `--version` | Override the image registry / tag (defaults: published `ghcr.io/devicechain-io`, or `localhost:5000` + `dev` with `--build`). |
| `--dry-run` | Print what each step would do without changing anything. |
| `--skip-preflight` | Skip the environment checks. |

## After bootstrap

The command prints the namespace, the bootstrapped admin credential, and how to
reach the instance through the cluster ingress. The admin is seeded with a default
password — **change it immediately**.

To inspect the running instance:

```bash
kubectl --context <kube-context> get pods -n my-instance
```

Load example data into a running instance over the API with:

```bash
dcctl seed construction --server localhost --instance my-instance
```
