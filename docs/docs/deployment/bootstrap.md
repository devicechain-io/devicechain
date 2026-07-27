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

1. **Render configuration** — resolve the instance id, namespace, profile, a
   generated database password, and the **secret-store root key**: minted on a first
   install and escrowed to an encrypted file you keep, or reused as-is when the
   instance already exists. See [Disaster Recovery](./disaster-recovery.md).
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

## Prerequisites {#prerequisites}

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
| `--ha` | Messaging high availability — see below. Needs at least **3 schedulable nodes**. |
| `--dry-run` | Print what each step would do without changing anything. |
| `--skip-preflight` | Skip the environment checks. |
| `--escrow-passphrase-file <path>` | Read the root-key escrow passphrase from a file instead of prompting. See below. |
| `--escrow-file <path>` | Write the escrow artifact somewhere other than `~/.devicechain/escrow/`. |
| `--no-escrow` | Do **not** escrow the root key. For throwaway instances only; implied by `--dev`. |
| `--restore-root-key <path>` | Disaster recovery: seed this instance's root key from an escrow artifact instead of minting one. |

### The root-key escrow {#escrow}

Bootstrap writes an encrypted copy of the instance's **secret-store root key** to
`~/.devicechain/escrow/<instance>-rootkey.escrow`, sealed under a passphrase you
choose. It will prompt for that passphrase, or take it from
`--escrow-passphrase-file` or `DCCTL_ESCROW_PASSPHRASE`.

This is on by default, and a non-interactive run with no passphrase **fails** rather
than proceeding without one:

```bash
# automation
DCCTL_ESCROW_PASSPHRASE="$(pass show devicechain/prod-escrow)" \
  dcctl bootstrap local prod --yes

# a throwaway instance
dcctl bootstrap local scratch --dev
```

:::danger This file is not optional for anything you care about
The root key encrypts every secret the instance stores, it lives only in the
cluster's etcd, and **no DeviceChain backup contains etcd**. Without this file, a
database backup restored to a new cluster rehydrates secrets that nothing can
decrypt — with no error at restore time. [Disaster
Recovery](./disaster-recovery.md) explains the whole procedure; read it before you
need it.
:::

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

### `--ha` {#ha}

Runs the message broker as a 3-node RAFT cluster, one server per node, with **every
JetStream stream and KV bucket replicated across it**. The instance then survives the
loss of any one node without losing messages, device sessions, or live state.

```bash
dcctl bootstrap local my-instance --ha
```

Both halves are set from that one flag, and that is the point of it. The broker's size
is infrastructure (OpenTofu); the per-stream replica factor is instance configuration
(Helm). They live in different tools, neither of which can see the other, and raising
only the first is the failure mode this flag exists to prevent: a three-node cluster
whose every stream is still single-replica costs three times the compute, reports three
healthy peers, and survives nothing.

:::caution It survives exactly ONE node loss
Three servers commit on a majority, so two remain a quorum and one does not. Losing a
second node — including losing one to a rolling node upgrade while another is already
down — stops writes until a node returns. Plan maintenance one node at a time. Surviving
two concurrent losses needs a 5-server cluster, which is not a supported topology today.
:::

**Three schedulable nodes, not three nodes.** The servers carry a hard anti-affinity
constraint, so if the cluster cannot place one per node the surplus stays `Pending`
rather than doubling up — co-located replicas would cost what replication costs and
protect against nothing. `dcctl` counts schedulable nodes and refuses before provisioning
anything. On a local `kind` cluster this means **three workers**: kind only removes the
control-plane's taint on a single-node cluster, so a control plane plus two workers is a
three-node cluster with two usable nodes.

**What it does not do.** It replicates the *messaging* tier only. Postgres and TimescaleDB
remain single-instance, and the number of service replicas is unchanged.

#### Verifying it {#verifying-it}

An HA claim is only worth what the broker actually holds, so check it there rather than
in the rendered configuration:

```bash
dcctl ha verify --instance my-instance
```

This reads the live broker and asserts that every stream, KV bucket and durable consumer
carries the declared replica factor **with all peers current**, and that the three servers
are on three distinct nodes. It exits non-zero if anything falls short, and prints what it
examined so a pass over an empty set is not mistaken for a pass.

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
