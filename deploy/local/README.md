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
dcctl preflight local   # check the host is ready (prints fixes for anything missing)
dcctl bootstrap local <instance>   # cluster, infra, core, chart, credentials, seed
# ... test ...
dcctl destroy <instance>           # deletes the cluster too (--keep-cluster to keep it)
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

These are the environment tweaks the stack needs. `dcctl preflight` verifies each
one and prints the fix if it's missing. **When we discover new requirements, add
them here and as a check in `dcctl preflight`** (the host checks live in
[`backend/cli/cmd/preflight_linux.go`](../../backend/cli/cmd/preflight_linux.go))
— that's the whole point of writing them down.

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

## What the bring-up does

**`dcctl bootstrap local <instance>` is the bring-up, and it is the only one.**
There used to be a second: `up.sh`, which performed the same steps directly as
shell. It was removed once dcctl became the thing that mints an instance's
credentials — a script applying the infrastructure tree on its own could only
produce an instance that comes up healthy and authenticates nothing, because the
databases, the object store, the broker's certificate authority and the dashboard
login are all written by dcctl BEFORE the apply now (ADR-080). Two tools meant two
implementations of those rules, which is how one shared default credential came to
open every instance in the first place.

What remains here is the part dcctl does not own: the **cluster** itself, the
**host diagnosis**, and the **image registry**.

1. **Preflight** — `dcctl preflight local`. Fails fast if the host
   baseline isn't met.
2. **The kind cluster** — `dcctl bootstrap local` creates one from the embedded
   copy of [`kind-cluster.yaml`](kind-cluster.yaml) (single control-plane node by
   default) if there is none, and `dcctl destroy` deletes it again.
3. **`dcctl bootstrap local <instance>`** — everything else, in the order ADR-080
   settled: CRDs and the operator FIRST, so the definition of an instance exists
   before anything declares one; then the credentials, minted and written; then the
   infrastructure apply; then the chart; then the seed.

`cloud-provider-kind` is **optional and nothing here starts it**. The default
bootstrap reaches ingress and MQTT through host-port/NodePort mappings, so no
`type: LoadBalancer` service has to resolve. Run it yourself only if you want real
LoadBalancer IPs — and then stop it yourself (`pkill -x cloud-provider-kind`).

`dcctl destroy <instance>` deletes the cluster along with the instance. Two things
survive it deliberately, both one-liners if you want them gone:

```bash
docker rm -f kind-registry          # the local image registry (kept as a warm cache)
pkill -x cloud-provider-kind        # only if you started it
```

### Images — published by default, build is a developer opt-in

**By default `dcctl bootstrap local` deploys published images** from
`ghcr.io/devicechain-io/<area>:<VERSION>` — no source build, no local registry.
This mirrors what an end user gets (most users won't even have the source).

`VERSION` defaults to the newest release **tag** reachable from `HEAD`,
prereleases excluded. Pin a different one explicitly:

```bash
dcctl bootstrap local dev                    # published images at the newest release tag
dcctl bootstrap local dev --version v1.4.0   # published images at a specific release
```

Note the leading `v`: the release pipeline tags images with the git tag verbatim,
so `ghcr.io/devicechain-io/device-management:v1.4.0` exists and `:1.4.0` does not.
If no release tag is reachable — a tarball export, a shallow clone, a fork with no
releases — `dcctl bootstrap` refuses rather than guessing a version that would
`ImagePullBackOff` several minutes later.

**Developers** who are changing service code build from source instead:

```bash
dcctl bootstrap local dev --build  # ko-build all images → local registry → deploy those
./build-images.sh                  # just build & push (no cluster changes)
```

`--build` flips the registry to `localhost:5000` (tag `dev`), starts the local
registry, and builds the same images [`build-images.sh`](build-images.sh) does.
That script uses
**`ko`** (the repo's image tool — services use local `replace` directives that
Dockerfiles can't resolve, so CI builds with ko too) with `--bare`, so each image
is named exactly what the Helm chart pulls: `{REGISTRY}/{area}:{TAG}` for services
and `{REGISTRY}/operator:{TAG}` for the operator. The web console is a static
nginx SPA (not a Go service), so `build-images.sh` builds it with `docker` as
`{REGISTRY}/frontend:{TAG}`.

The `dcctl` CLI follows the same model: `dcctl bootstrap local <inst>` deploys
published images at the default version; `--version x.y.z` overrides it, and
`--build` is the developer build-from-source path.

### Fast inner loop — bounce one image

To iterate on a single service or the console without a full rebuild/redeploy,
[`bounce.sh`](bounce.sh) rebuilds just that image and rolls it onto the running
instance (it pushes a unique tag and `kubectl set image`s the deployment, so the
new build is always pulled past the chart's `IfNotPresent` policy):

```bash
deploy/local/bounce.sh frontend            # ~6s: rebuild + roll the web console
deploy/local/bounce.sh device-management   # one service
```

For active **UI** work the faster loop is `npm run dev` (Vite HMR) pointed at the
instance ingress — see [`frontend/.env.example`](../../frontend/.env.example).
Use `bounce.sh` when you need to validate the actual served artifact.

---

## Reaching the UI / API

- **From WSL2:** hit the ingress-nginx LoadBalancer IP that `cloud-provider-kind`
  assigns (printed at the end of the bootstrap), or `localhost` via the node port
  mappings in `kind-cluster.yaml`.
- **From the Windows browser:** with `networkingMode=mirrored`, `localhost` is
  shared — the node port mappings (80/443) are reachable directly.

---

## Multi-node

Single control-plane is the default (least overhead — every node is a full
kubelet/containerd container). To exercise PodDisruptionBudgets / anti-affinity,
uncomment the `worker` nodes in [`kind-cluster.yaml`](kind-cluster.yaml) and
re-run `dcctl bootstrap local <instance>`.

---

## Troubleshooting / discovered tweaks

Append new findings here (and as checks in `dcctl preflight`) so the baseline
stays current.

- **`LoadBalancer` service stuck `<pending>`** — `cloud-provider-kind` isn't
  running, and nothing starts it for you. The default bootstrap needs no
  LoadBalancer at all; start it by hand only if you want one.
- **`too many open files` / controllers crashlooping** — inotify limits (step 2).
- **Image `ErrImagePull` from `localhost:5000`** — the registry container isn't
  connected to the kind network, or images weren't pushed. Re-run the bootstrap
  (idempotent) and confirm `docker ps | grep kind-registry`.
- **DB pod `Pending` on PVC** — disk headroom (step 4), or PV data accidentally
  pointed at a 9p mount.
