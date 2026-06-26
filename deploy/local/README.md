# DeviceChain — local cluster baseline

A reproducible recipe for standing up the **full DeviceChain stack** on a local
[kind](https://kind.sigs.k8s.io/) cluster for end-to-end testing: the operator,
all microservices, the in-cluster data plane (NATS, Postgres, TimescaleDB),
ingress + TLS, and the `dcctl` bootstrap flow.

It is **cluster-agnostic by design** — the OpenTofu infra and Helm chart run the
same on kind, k3s, EKS, or GKE (see [`deploy/opentofu`](../opentofu)). This
directory just captures the *local kind* target and the host tweaks it needs, so
anyone on the team can reproduce a known-good dev cluster and so new requirements
get captured here as we find them.

> **Why kind?** You're running your real OpenTofu + Helm + `dcctl` path locally,
> so the highest-fidelity target is vanilla upstream Kubernetes — which is exactly
> what `kindest/node` is. kind also starts blank (no bundled ingress/LB to fight),
> matches CI, and the `dcctl` `local` provider auto-detects `kind-` contexts.

---

## TL;DR

```bash
cd deploy/local
./preflight.sh          # check the host is ready (prints fixes for anything missing)
make up                 # create cluster + registry + LB, apply infra, install core + chart
# ... test ...
make down               # tear it all down
```

The cluster is named **`devicechain`**, so its kube-context is
**`kind-devicechain`** — the value the OpenTofu
[`terraform.tfvars.example`](../opentofu/terraform.tfvars.example) already
defaults to.

---

## Prerequisites

Tooling (the [preflight](#preflight) script checks all of these):

| Tool | Why | Install |
|---|---|---|
| Docker engine | kind runs nodes as containers | native in WSL2 / Linux (not Docker Desktop — see below) |
| `kind` | the cluster | `go install sigs.k8s.io/kind@latest` |
| `kubectl` | talk to the cluster | distro pkg / official binary |
| `helm` | render the app chart | `helm` v3 |
| `tofu` (or `terraform`) | infra (NATS/PG/Timescale/ingress/cert-manager) | OpenTofu |
| `cloud-provider-kind` | gives `type: LoadBalancer` services real IPs (ingress-nginx, NATS MQTT) | `go install sigs.k8s.io/cloud-provider-kind@latest` |
| `go` | build `dcctl` + images | 1.22+ |

---

## Host baseline (WSL2 and Linux)

These are the environment tweaks the stack needs. `preflight.sh` verifies each
one and prints the fix if it's missing. **When we discover new requirements, add
them here and as a check in `preflight.sh`** — that's the whole point of this
directory.

### 1. Run Docker natively, not via Docker Desktop

Use a native `dockerd` inside your distro. kind then puts the cluster on the
docker bridge *inside* your dev environment, so `kubectl`/`dcctl`/your services
reach it with zero networking config. Confirm your active context is `default`,
not `desktop-linux`:

```bash
docker context ls          # the '*' should be on 'default'
docker context use default # if it isn't
```

### 2. inotify limits

Operators + ~a dozen services + databases watch a lot of files. Raise the
per-user instance cap (watches is usually already high):

```bash
echo 'fs.inotify.max_user_instances=512' | sudo tee /etc/sysctl.d/99-kind.conf
sudo sysctl -p /etc/sysctl.d/99-kind.conf
```

### 3. cgroup v2

kind expects cgroup v2 (`stat -fc %T /sys/fs/cgroup` → `cgroup2fs`). Current WSL2
and modern distros default to this; no action normally needed.

### 4. Disk headroom

The stack pulls a stack of images and provisions three PVC-backed databases
(NATS JetStream, Postgres, TimescaleDB). Keep **~40 GB+ free** on the filesystem
backing `/var/lib/docker`. Keep all PV data on **native ext4** — never on
`/mnt/c` / `/mnt/<drive>` (the 9p translation makes fsync-heavy databases slow and
unsafe). local-path-provisioner already defaults to native storage.

### 5. WSL2-specific (`%USERPROFILE%\.wslconfig`, Windows side)

WSL2 is a VM that only gets the CPU/RAM/disk you grant it. Recommended:

```ini
[wsl2]
# networking that shares localhost between Windows and WSL2 — lets your Windows
# browser hit the cluster's ingress on localhost with no port-forward.
networkingMode=mirrored
# reclaim freed disk back to the host drive (the vhdx otherwise only grows).
sparseVhd=true
# memory/processors default to 50%/all of the host — set explicitly only if you
# want to cap them (leave Windows headroom):
# memory=48GB
# processors=12
```

Apply with `wsl --shutdown` (then restart the distro). The vhdx physically lives
on a Windows drive; if you need more than its current cap, relocate the distro to
a larger drive or `wsl --manage <distro> --resize <size>`.

> **Reference box this was validated on:** i9-10900K (20 vCPU), 62 GiB RAM to
> WSL2, 16 GiB swap, kernel 6.18, cgroup v2, native dockerd, `networkingMode=mirrored`.

---

## What `make up` does

`up.sh` runs the end-to-end bring-up. Today it performs the steps directly; the
`dcctl bootstrap local` pipeline is being implemented to automate exactly this
sequence (ADR-032), at which point the middle of the script collapses to a single
`dcctl bootstrap local <instance>` call.

1. **Preflight** — fail fast if the host baseline isn't met.
2. **Create the kind cluster** from [`kind-cluster.yaml`](kind-cluster.yaml)
   (single control-plane node by default).
3. **`cloud-provider-kind`** in the background, so `type: LoadBalancer` services
   (ingress-nginx, NATS MQTT device ingress) get real IPs.
4. **(developer path only)** With `BUILD_IMAGES=1`: start a local registry at
   `localhost:5000`, wire it into the cluster's containerd, and build+push images.
   End users skip this and pull published images.
5. **OpenTofu apply** of [`deploy/opentofu`](../opentofu) — NATS, Postgres,
   TimescaleDB, ingress-nginx, cert-manager — targeting `kind-devicechain`.
6. **Install core** — CRDs + operator (`backend/k8s` `make install deploy`).
7. **Install the instance chart** — `helm install` of
   [`deploy/helm/devicechain`](../helm/devicechain) at the resolved registry/version.
8. **Seed** an admin credential / example data via `dcctl`.

`make down` deletes the cluster and the background `cloud-provider-kind` process
(and the registry with `make purge`).

### Images — published by default, build is a developer opt-in

**By default `make up` deploys published images** from
`ghcr.io/devicechain-io/<area>:<VERSION>` — no source build, no local registry.
This mirrors what an end user gets (most users won't even have the source). Pin a
version with `VERSION`:

```bash
make up                 # published images at the default version
VERSION=1.4.0 make up   # published images at a specific version
```

**Developers** who are changing service code build from source instead:

```bash
BUILD_IMAGES=1 make up  # ko-build all images → local registry → deploy those
make images             # just build & push (no cluster changes)
```

`BUILD_IMAGES=1` flips the registry to `localhost:5000` (tag `dev`), starts the
local registry, and runs [`build-images.sh`](build-images.sh). That script uses
**`ko`** (the repo's image tool — services use local `replace` directives that
Dockerfiles can't resolve, so CI builds with ko too) with `--bare`, so each image
is named exactly what the Helm chart pulls: `{REGISTRY}/{area}:{TAG}` for services
and `{REGISTRY}/devicechain-operator:{TAG}` for the operator.

The `dcctl` CLI follows the same model: `dcctl bootstrap local <inst>` deploys
published images at the default version; `--version x.y.z` overrides it, and
`--build` is the developer build-from-source path.

---

## Reaching the UI / API

- **From WSL2:** hit the ingress-nginx LoadBalancer IP that `cloud-provider-kind`
  assigns (printed at the end of `up.sh`), or `localhost` via the node port
  mappings in `kind-cluster.yaml`.
- **From the Windows browser:** with `networkingMode=mirrored`, `localhost` is
  shared — the node port mappings (80/443) are reachable directly.

---

## Multi-node

Single control-plane is the default (least overhead — every node is a full
kubelet/containerd container). To exercise PodDisruptionBudgets / anti-affinity,
uncomment the `worker` nodes in [`kind-cluster.yaml`](kind-cluster.yaml) and
re-run `make up`.

---

## Troubleshooting / discovered tweaks

Append new findings here (and as checks in `preflight.sh`) so the baseline stays
current.

- **`LoadBalancer` service stuck `<pending>`** — `cloud-provider-kind` isn't
  running. `make up` starts it; check `pgrep -a cloud-provider-kind`.
- **`too many open files` / controllers crashlooping** — inotify limits (step 2).
- **Image `ErrImagePull` from `localhost:5000`** — the registry container isn't
  connected to the kind network, or images weren't pushed. Re-run `make up`
  (idempotent) and confirm `docker ps | grep kind-registry`.
- **DB pod `Pending` on PVC** — disk headroom (step 4), or PV data accidentally
  pointed at a 9p mount.
