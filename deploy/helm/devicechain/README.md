# DeviceChain Helm chart

[DeviceChain](https://devicechain.io) is a cloud-native, self-hosted IoT platform:
device management, MQTT / Sparkplug / LwM2M ingest, TimescaleDB telemetry, a CEL
rule engine with alarms and outbound connectors, versioned dashboards, and a web
console. It is Apache-2.0 and multi-tenant — one shared set of services serves
every tenant, with isolation enforced in storage and messaging rather than by
per-tenant pods. Full documentation: [docs.devicechain.io](https://docs.devicechain.io).

This chart renders those workloads — one Deployment + Service per **enabled
functional area**, plus the instance and per-service config ConfigMaps.

## Install

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version <X.Y.Z> \
  --set instance.id=devicechain \
  --set image.tag=v<X.Y.Z> \
  --set instance.config.infrastructure.secrets.rootKey=$(openssl rand -base64 32)
```

**Keep that root key.** It is required in every profile — `user-management` seals the
instance's token-signing key under it, so the chart fails the render without one rather
than shipping a pod that cannot form its key. The **same** value has to be passed on
every later install and upgrade of this instance: a new key orphans every secret
already stored, and stops everyone signing in. Generating it on the command line as above is fine for a first look;
for anything you intend to keep, let `dcctl bootstrap` mint and store it for you. It
owns the instance config Secret from then on, which is what keeps the key, the database
and broker credentials and every later run consistent with each other.

If you are driving Helm yourself rather than using `dcctl`, supply the document from a
Secret you manage with `instance.existingSecret` (see below) and keep the key in it.

**Create the superuser's password Secret too.** There is no default superuser password.
user-management creates the instance's superuser (`superuser@devicechain.local`) the first
time it starts with an empty identity table, and takes the password from key `password` of
the Secret `dci-<instance.id>-superuser` in the instance's namespace. Set
`instance.superuserSecret` to use a Secret with a different name. `dcctl bootstrap` generates
that Secret. With plain Helm you create it yourself, once the chart has created the namespace
(or beforehand, in a namespace you manage with `instance.createNamespace=false`):

```bash
kubectl -n dci-devicechain create secret generic dci-devicechain-superuser \
  --from-literal=password="$(openssl rand -base64 32)"
```

Until the Secret exists, user-management refuses to create the superuser and restarts, and it
picks the Secret up on its next start. The password is read only for that first creation.
After that, change it in the console; changing the Secret does nothing.

Take `<X.Y.Z>` from the [Releases
page](https://github.com/devicechain-io/devicechain/releases) — the chart's OCI tag is
the release version without the leading `v`, and `image.tag` keeps the `v`.

**`instance.id` picks the namespace, but is not the namespace.** The instance is
deployed into `dci-` plus the id — the install above lands entirely in
`dci-devicechain` — and the chart creates that namespace itself, so no `--namespace`
flag is needed. The prefix keeps instance namespaces disjoint from the cluster's own
(`monitoring`, `dc-system`, `cert-manager`, `cnpg-system`, `ingress-nginx`), so an
instance can never be installed into a namespace a cluster component owns: without it,
an instance named `monitoring` would write its root key, its broker TLS private key and
every database credential into the monitoring stack's namespace. The id keeps all its
other jobs — it still names the release, the database and its login, the config objects,
the ServiceAccount, the `devicechain.io/instance` label and every messaging subject.

Infrastructure (NATS, TimescaleDB, ingress, TLS) is provisioned separately by the
OpenTofu modules in
[`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu);
this chart assumes it exists and points at it via
`instance.config.infrastructure` / `.persistence`. The instance's own broker and event
store run in the instance's namespace, beside the services this chart deploys; the
relational database, ingress controller and cert-manager are shared by every instance on
the cluster.

Every release is one semver tag (`vX.Y.Z`) covering all images, the operator, the
chart, and the `dcctl` CLI together. Images are public on `ghcr.io/devicechain-io`
(multi-arch, distroless nonroot), so nothing is built locally. The chart's OCI tag
is that same version without the leading `v`; `image.tag` keeps it.

### Installing from a checkout

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v<X.Y.Z>
```

## Choosing what to deploy

Set **either** a named `profile` **or** an explicit `enabledFunctionalAreas`
list (not both). An empty selection resolves to `default`.

| Profile | Functional areas |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing |
| `full` | everything in `default`, plus `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest`, `lwm2m-ingest` |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

```bash
# Every profile needs the instance root key (see above), the smallest ones included.
helm install dc deploy/helm/devicechain --set profile=telemetry \
  --set instance.config.infrastructure.secrets.rootKey=$ROOT_KEY
# or an explicit set:
helm install dc deploy/helm/devicechain \
  --set profile= \
  --set 'enabledFunctionalAreas={user-management,device-management,event-sources}' \
  --set instance.config.infrastructure.secrets.rootKey=$ROOT_KEY
```

The chart **fails the render** if the selection omits a required core area
(`user-management`, `device-management`) or an enabled area's hard dependency.
`user-management` and `device-management` are the required core; every other area in
the table above is optional — the seven `default` adds on top of the core, plus the five
only `full` ships. The one dependency between optional areas is `outbound-connectors`,
which requires `event-processing`; the rest can be enabled or left out independently.
(`values.schema.json` carries the authoritative area names; the dependency catalog
mirrors `backend/k8s/functionalarea`, the Go source of truth.)

> **Required value.** `instance.config.infrastructure.secrets.rootKey` — a base64
> 256-bit key (`openssl rand -base64 32`) — is required in **every** profile.
> `user-management`, which every profile runs, seals the private half of the
> instance's token-signing key under it; `notification-management`,
> `outbound-connectors` and `ai-inference` seal their integration credentials under
> it too. A service that cannot form its key refuses to start, so the chart
> **fails the render** rather than shipping a crash-loop. `dcctl bootstrap` mints one
> automatically. The chart deliberately does NOT generate one: Helm's random functions
> re-run on every upgrade, which would rotate the KEK and orphan every stored secret.
>
> The render check applies to config supplied **inline**. With
> `instance.existingSecret` the chart cannot read the document at all, so it cannot
> check the key — the responsibility moves to whoever writes that Secret, and the
> chart asks them to say so by requiring `instance.existingSecretChecksum`. See
> below.

### Supplying the instance config from a Secret the chart does not write

`instance.existingSecret` mounts a Secret you manage (External Secrets, a sealed
secret) instead of the one the chart renders. It is for driving the chart **without**
`dcctl`: on a `dcctl`-managed instance this is already how the chart runs, because
`dcctl` writes that Secret itself and sets these values for you — so on such an
instance, change the config through `dcctl` and not through the Secret.

That is not a style preference. The annotation that rolls the workloads hashes the
document Helm was given, not the one in the cluster, so a Secret edited by any other
route updates the file the pods have mounted and restarts nothing: every process goes
on serving the config it started with while `kubectl get secret` shows the new one.

Four constraints come with this path, and each exists because the chart stops being the
document's author:

| Value | Why |
| --- | --- |
| `instance.existingSecret` must be `dci-<instance.id>-config` | `dcctl` reads the config back by that name to decide whether the instance already exists, and reads a missing Secret as a **fresh install**. Any other name makes the next bootstrap re-run mint a new root key and new database and broker credentials over a live instance. |
| `instance.existingSecretChecksum` is **required** — the sha256 of the `instance` document | The pod annotation that rolls workloads on a config change normally hashes the document the chart writes. With an external Secret there is none, so the annotation would be a constant and a rotated credential would apply cleanly, restart nothing, and report success. |
| `metrics.natsBrokerHost` is required when `metrics.natsPodMonitor` is on | The PodMonitor's target namespace is derived from the broker hostname in the config. The chart would otherwise use its own default and monitor a namespace this instance may not use — collecting nothing, silently. |
| `networkPolicy.externalConfigPorts` is required when `networkPolicy.enabled` is on | The egress rule's ports come from the config for the same reason. A port that does not match silently blocks the services' own egress, which presents as a broker or database outage. |

The chart also stops applying two transforms it normally performs while writing the
document: it injects `infrastructure.shutdown` from the top-level
`shutdownDrainSeconds` and `terminationGracePeriodSeconds`, and it removes
`infrastructure.aiInference` when that area is not deployed. **Both are yours to
reproduce.** Neither omission is invalid — a service will start on a document with a
grace period that disagrees with its pod, and then be SIGKILLed mid-drain — so the
document being accepted is not evidence that it is right. `dcctl` checks both before
installing.

`default` is the standard system. `full` is exhaustive — it ships **every** area this
build has, and a test enforces that, so "full" cannot drift back into meaning "most of
it".

The difference is the five areas `default` holds back. Each carries a decision an operator
should make deliberately rather than inherit — a paid provider key, an egress surface, an
agent-facing API, a customer broker topology, a device-facing DTLS port — not because they
are second-class. Get them with `--set profile=full`, or name them in an explicit
`enabledFunctionalAreas` set:

| Area | Purpose | Notes |
|---|---|---|
| `outbound-connectors` | outbound rule-action sink (webhooks, MQTT, Kafka, SNS/SQS) | hard-depends on `event-processing`; needs `infrastructure.secrets.rootKey` for its credential store |
| `mcp` | read-only MCP resource server | needs `resourceUrl` + `issuerUrl`, **derived from the ingress** when one is configured (override under `functionalAreas.mcp.config`). Starts and serves metadata regardless, but a client can only obtain a token once the OAuth AS is on — set `user-management`'s `auth.issuerUrl`, a separate deliberate switch |
| `ai-inference` | natural-language→rule authoring proxy | no hard dep (fails paths closed); needs `infrastructure.secrets.rootKey` for provider keys; external routing needs `serviceAuth.secret` + `userManagement` and is per-tenant opt-in / fail-closed |
| `sparkplug-ingest` | Sparkplug B host application ingesting from customer MQTT brokers | hard-depends on `device-management`; each source binds one broker connection to one tenant; single-owner — `replicas: 1` + `Recreate`, the render fails above one |
| `lwm2m-ingest` | OMA LwM2M over CoAP/UDP + DTLS for constrained devices | hard-depends on `device-management`; serves CoAPS on UDP 5684; single-owner — `replicas: 1` + `Recreate`, the render fails above one |

## Per-service configuration

Each service loads its typed config fail-closed — an unknown or invalid key is a
startup failure, not a warning. Override it
per area under `functionalAreas.<area>.config`; an unset config renders `{}` and
the service applies its own defaults:

```yaml
functionalAreas:
  device-management:
    config:
      deviceAuthMode: required   # disabled | optional | required
      maxEventFutureSkewSeconds: 300   # ceiling on device clock skew
  event-sources:
    replicas: 2
```

The skew ceiling bounds only how far a timestamp may run ahead. There is no past
setting: a reading dated more than 366 days before it arrives is refused, on every
transport. That limit is fixed.

Per-tenant rate ceilings are enforced by each replica separately: at `replicas: 2`,
event-sources, outbound-connectors and ai-inference can admit up to twice a tenant's
ceiling.

The five services on the event path request CPU sized from measurement, from
`functionalAreas.<area>.measuredRequests`: `device-management` 800m,
`event-management` 900m, `device-state` 950m and `event-sources` 1 core, what each
used at 6,000 events a second in an earlier GKE run, and `event-processing` 400m, a floor taken from a
heavier run rather than a measure of what keeping up needs. While
`useMeasuredRequests` is `true`, the default, a top-level `resources.requests.cpu`
does not reach these five; set the area's own
`functionalAreas.<area>.resources.requests`, which wins over both.
`dcctl install --compact` turns `useMeasuredRequests` off. These five also prefer
nodes running fewer of each other (`eventPathSpread`), and `device-management`,
`event-sources` and `event-management` prefer a node without the instance's
event-store primary (`avoidEventStorePrimary`). Both are preferences: a pod still
schedules when they cannot be met.

Areas can also expose extra ports beyond the shared 8080 graphql port via
`functionalAreas.<area>.extraPorts` (name ≤15 chars). event-sources ships with
its HTTP device-ingest port by default:

```yaml
functionalAreas:
  event-sources:
    extraPorts:
      - name: http-ingest
        port: 8081   # POST /{instanceId}/{tenant}/events
```

An area can serve Go runtime profiles (CPU, heap, goroutines, execution trace) for
measuring where it spends its time. This is off by default. When it is on, the
profiles are served on a separate listener, which is never a container port, a
Service port or an ingress route. The default address is the pod's loopback, so you
reach it with `kubectl port-forward`. Turning it on restarts that area's pods only:

```yaml
functionalAreas:
  device-management:
    profiler:
      enabled: true
      # address: "127.0.0.1:6060"   # default; an IP address and a port
```

The documentation's observability page, under "Profiling a service", explains how to
capture a profile.

`values.schema.json` validates the deployment-selection envelope (profile enum,
area names, image/instance shape) at `helm install`/`upgrade` time.

## Object store

Opaque binaries that don't belong in Postgres (branding logos today; firmware/OTA +
exports later) go to a pluggable object store, configured under
`instance.config.infrastructure.blob`. Two backends:

- **`s3`** (recommended for production / multi-replica) — AWS S3 or any
  S3-compatible service (MinIO). No volume is needed. Credentials come from the
  standard AWS chain (env, IRSA, instance profile), **never** from values;
  set only `bucket` (+ `region`, or `endpoint`/`usePathStyle` for MinIO).

  ```yaml
  instance:
    config:
      infrastructure:
        blob:
          backend: s3
          bucket: dc-prod-blobs
          region: us-east-1
  ```

- **`filesystem`** (default) — writes under `blob.directory`. Because the pods run
  with a read-only root filesystem, that directory **must** be a writable mounted
  volume, wired via the top-level `blobStorage` block. The chart renders (and mounts)
  a `PersistentVolumeClaim` for it:

  ```yaml
  instance:
    config:
      infrastructure:
        blob:
          backend: filesystem
          directory: /var/lib/devicechain/blob
  blobStorage:
    persistence:
      enabled: true
      size: 10Gi
      # RWX + an RWX-capable class are REQUIRED when a consumer runs replicas > 1
      # (a filesystem store is per-pod otherwise); prefer the s3 backend at scale.
      accessModes: [ReadWriteMany]
      storageClass: efs-sc
  ```

  Use `blobStorage.persistence.existingClaim` to mount a PVC you provisioned
  out-of-band instead. Only the areas in `blobStorage.mountAreas` (default
  `user-management`) mount the volume. Leaving `blob.directory` empty and
  persistence disabled keeps the store "not configured" — logo upload/read return
  503 while inline/URL logos still work. The render **fails** on a mismatch
  (persistence enabled for the `s3` backend, a directory with no volume, or a
  `mountPath` that disagrees with `blob.directory`).

  Switching a live instance from `filesystem` to `s3` sets `persistence.enabled=false`,
  and Helm then deletes the chart-created PVC (and its stored objects). Migrate the data
  first, or set `blobStorage.persistence.annotations: {helm.sh/resource-policy: keep}` to
  retain the claim.

## Zero-downtime upgrades

`helm upgrade` rolls forward without dropping traffic. Each Deployment uses a
`RollingUpdate` strategy with `maxUnavailable: 0` / `maxSurge: 1`, so a new pod must
pass `/readyz` before an old one is removed. On termination a pod flips `/readyz` to
503 first, waits `shutdownDrainSeconds` (default 5) for endpoint removal to
propagate, then drains in-flight requests — an app-side drain, since the
distroless images have no shell for a `preStop` hook. That window and
`terminationGracePeriodSeconds` (default 30) are one budget: both are rendered into
the instance configuration, and a service refuses to start if the drain would take
more than half the grace period, since the teardown after the drain is what actually
finishes in-flight work before the kubelet's SIGKILL. Database migrations run under a
Postgres advisory lock so concurrently-rolling replicas don't race on DDL.

For true zero-downtime run `replicas: 2`+ per area (`--set replicas=2` or
`functionalAreas.<area>.replicas`); a `PodDisruptionBudget` is rendered for any area
with more than one replica. Tune the strategy via `rollingUpdate.maxUnavailable` /
`rollingUpdate.maxSurge`.

Each `event-management` pod holds its own pool of 20 connections to the event store.
On an event store built from this repository's OpenTofu, set the instance root's
`event_management_replicas` to the same count, so the store keeps room for every pod's
pool during a rollout. `dcctl install --ha` (without `--compact`) runs two and sets both.

Per-tenant rate ceilings are enforced by each replica separately: at `replicas: 2`,
event-sources, outbound-connectors and ai-inference can admit up to twice a tenant's
ceiling.

## Bounding tenant egress (optional)

A tenant configures its own delivery destinations — a webhook URL, an SMTP relay, an MQTT
or Kafka broker, an SNS topic or SQS queue. DeviceChain refuses a destination that resolves
to a private, loopback, link-local, carrier-NAT or cloud-metadata address, and it does so
at the moment the connection is dialled rather than when the URL is saved, because a
hostname can resolve differently between the two.

That check covers every tenant path, on any cluster, whether or not this section is
enabled: webhooks, HTTP calls and SMTP, and the MQTT (including `ws://` and `wss://`),
Kafka and SNS/SQS connectors. For Kafka it covers every broker the cluster advertises in
its metadata, not only the addresses the connector names. For SNS and SQS it covers an
endpoint override. Proxy environment variables are not used on any of these paths. A
refused destination is final: the dispatch is dead-lettered as `blocked` and not retried.

Permitting a private destination is `instance.config.infrastructure.egress.allowedDestinations`,
one `/32` per address. An allowance applies to every tenant and every path. Destinations
that are private by construction — Amazon MSK brokers, Amazon MQ, SNS or SQS through an
interface VPC endpoint with private DNS — need an entry for each address, and an interface
endpoint has one per availability zone.

`networkPolicy.enabled=true` adds a second, independent layer: an egress `NetworkPolicy`
for `outbound-connectors` that permits DNS, the platform's own datastores and services, and
the public internet — and nothing else. It is defence in depth against a defect in the
in-process check, not what makes the boundary hold.

**It only does anything if your CNI enforces NetworkPolicy.** The object is ordinary
Kubernetes API and every cluster accepts and stores it, so `kubectl get netpol` shows it
whether or not anything acts on it. Most production CNIs enforce policy; so does the one
`kind` installs by default, which means a development cluster is not a safe place to find
out you got a rule wrong.

**A denied connection hangs; it does not fail.** Traffic a policy drops produces a timeout
rather than a refusal, so a peer missing from the rules looks like an outage somewhere
else. The rules enumerate everything the service needs. Three ways to lose one:

1. **Check where your CNI evaluates egress.** The rules reach the datastores and
   `user-management` with namespace and pod selectors, which match pod addresses. A CNI
   that evaluates egress *before* kube-proxy's address translation sees a Service
   ClusterIP instead, matches nothing, and drops it — and the service then never becomes
   ready. Add your Service CIDR to `networkPolicy.additionalAllowedCidrs` if so.
2. **Check how DNS reaches your pods.** The rules permit DNS to pods in the namespace
   named by `networkPolicy.dnsNamespaceSelector`. If you run NodeLocal DNSCache — common
   on managed clusters — that selector may match nothing, every lookup is dropped, and
   nothing starts. Which address to permit depends on how the cache is deployed, and the
   two cases need different answers:

   - **kube-proxy in iptables mode (the usual deployment).** Pods keep the kube-dns
     Service ClusterIP in `/etc/resolv.conf`; the cache binds that address locally and
     intercepts the query without translating it, so what your CNI sees is the kube-dns
     ClusterIP — inside the blocked private ranges, matching no pod. Permit the kube-dns
     Service ClusterIP as a `/32`.
   - **IPVS mode, or any cluster where `--cluster-dns` was pointed at the cache.** The
     resolver really is the node-local link-local address. Permit that.

   To tell which you have, read `/etc/resolv.conf` in any running pod: the `nameserver`
   there is the address to permit.
3. **Replacing a namespace selector means clearing the default key, not just setting
   yours.** The keys hold a label map, and Helm MERGES a map rather than replacing it, so
   adding your label leaves the shipped one in place and the resulting two-label selector
   matches neither namespace. That is true of `--set`, `--set-json` and a values file
   alike — a values file is not the workaround, and an earlier version of this page said
   it was. Set the default key to `null` in the same values file as your own label:

   ```yaml
   networkPolicy:
     infrastructureNamespaceSelector:
       devicechain.io/component: null    # drop the shipped label
       my.org/role: datastores           # and select on yours
   ```

The address space the policy refuses is part of the chart, not a value you set. It has to
match what the platform refuses in code — a build check compares the rendered policy
against the compiled deny table and fails if they drift apart — so it is not a per-
deployment choice. To permit a specific destination use `networkPolicy.additionalAllowedCidrs`
below; there is deliberately no way to shrink the refused set from values, because doing so
would silently leave the network permitting what the code refuses.

**Three things the policy does not do**, all worth knowing before relying on it:

- The relational database rule permits the whole infrastructure namespace on the
  PostgreSQL port, not the database by name. So another datastore living in that namespace
  on that port is reachable even though only PostgreSQL is intended. Narrowing it needs a
  pod selector for the database, and a way to express the labels of bring-your-own
  infrastructure; until then, treat "the platform's database" as "that namespace, on that
  port". The broker is the instance's own and is selected by its pod labels in the
  instance's namespace, so it is not affected.

- It compares address prefixes, so it cannot look inside an IPv6 address that carries an
  IPv4 one. On a NAT64 or dual-stack cluster it does not refuse a tenant broker at a
  translated address that points at a private or metadata address. The in-process check
  does, on every path; closing it in this layer too needs the policy generated from your
  cluster's own translation prefix.
- It is a **ceiling** over `instance.config.infrastructure.egress.allowedDestinations`
  for the paths it covers — two controls in series rather than one. That setting permits
  specific addresses for the destinations checked in code, and an address permitted there
  is still dropped by the network unless it is also in `additionalAllowedCidrs`. A dropped
  connection hangs rather than reporting a refusal, so the delivery retries to its cap
  instead of failing usefully. Where both apply, put the address in both places.

  🔴 **But note WHICH paths, because an earlier version of this page got it wrong and told
  you to do this for a mail relay.** The policy selects `outbound-connectors` only, and
  mail is `notification-management`'s path — so nothing here affects SMTP, and adding a
  relay address to `additionalAllowedCidrs` does nothing for mail while needlessly opening
  the tenant connector paths toward it. For a relay the Go guard refuses, the knob is
  `egress.allowedDestinations` alone.

Prefer single addresses when you widen either boundary: in Kubernetes the smallest CIDR
that reaches one in-cluster service is often the whole Service range, and granting that
re-opens every private address for every tenant.

## What it renders

- A `Namespace` named `dci-<instance.id>` (toggle with `instance.createNamespace`), and
  every object below is written into it. **The instance's broker and event store live in
  this namespace too**, so uninstalling the release deletes them — and the event store's
  data — along with the services.
- `dci-<id>-config` — instance config mounted at `/etc/dci-config/instance`. (The
  `dci-` here is the object-name prefix these objects have always carried, not the
  namespace prefix; this Secret lives *in* `dci-<id>`, it is not named after it.)
- `dct-<id>-config` — per-area config mounted at `/etc/dct-config/<area>`.
- Per enabled area: a `Deployment` (with `/readyz` readiness + `/healthz`
  liveness probes) and a `Service` on the GraphQL port (plus
  any `extraPorts` for the area, e.g. event-sources' HTTP ingest on 8081).
- Optional (`blobStorage.persistence.enabled=true`, filesystem backend): a
  `PersistentVolumeClaim` (`dci-<id>-blob`) mounted into `blobStorage.mountAreas`
  at the store directory.
- The web console (`frontend.enabled=true`, on by default): a static nginx
  `Deployment` + `Service` serving the Vite/React SPA. Disable for
  headless/ingest-only instances.
- Optional (`networkPolicy.enabled=true`): an egress `NetworkPolicy` for
  `outbound-connectors`. Requires a policy-enforcing CNI to do anything; see
  "Bounding tenant egress" above.
- Optional (`ingress.enabled=true`): two `Ingress` objects on one host — the API
  ingress routes `https://<host>/api/<area>/graphql` to each enabled area
  (stripping the `/api/<area>` prefix), and the web ingress serves the console at
  `https://<host>/`. Plus a cert-manager TLS `Issuer` (self-signed by default).
  Requires the ingress-nginx controller + cert-manager from
  [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu).

## Values reference

Every value the chart's schema (`values.schema.json`) accepts, with its type, its default in
`values.yaml` and what it does. A `-` default means the key has no entry in `values.yaml`
(or, for a group, that its defaults are listed on the keys beneath it). The table is
generated from the schema; do not edit it by hand.

<!-- values:begin -->
| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `profile` | string | `""` | A named profile, or empty to use enabledFunctionalAreas (or fall back to the default profile). 'default' is the standard system; 'full' ships every area. |
| `enabledFunctionalAreas` | array | `[]` | Explicit set of functional areas; mutually exclusive with profile. |
| `extraSecrets` | array | `[]` | Extra Opaque Secrets to render into the instance namespace (templates/extra-secrets.yaml), projected into a functional area via its extraEnv secretKeyRef. |
| `extraSecrets[].name` | string | - | Secret name, rendered in the instance namespace. |
| `extraSecrets[].stringData` | object | - | Verbatim key→value entries (e.g. a base64 PSK string a service decodes at load). |
| `instance` | object | - | Identity and configuration of this DeviceChain instance. |
| `instance.id` | string | `"devicechain"` | Instance id, 1 to 50 characters, a lowercase DNS-1123 label. It names the Helm release (dc-&lt;id&gt;), the instance's database and login on the shared relational store, the config objects (dci-&lt;id&gt;-config, dct-&lt;id&gt;-config), the blob claim (dci-&lt;id&gt;-blob) and the ServiceAccount (dc-&lt;id&gt;), and it is the first segment of every messaging subject, the device-plane MQTT topic and the HTTP ingest route. The instance's namespace is not the id: it is dci-&lt;id&gt;, so instance foo is deployed into namespace dci-foo, which keeps instance namespaces disjoint from the cluster's own (monitoring, cert-manager and so on). The 50-character cap comes from Helm's 53-character release-name limit applied to dc-&lt;id&gt;. |
| `instance.createNamespace` | boolean | `true` | Whether the chart renders the instance's Namespace object, i.e. dci-&lt;instance.id&gt;. Disable when that namespace is created and managed elsewhere; either way every object this chart renders is written into it. |
| `instance.existingSecret` | string | `""` | Name of a pre-created Secret (for example one managed by External Secrets) holding the instance-config JSON under an `instance` key. When set, the chart mounts it and renders no Secret of its own, and existingSecretChecksum becomes required. |
| `instance.existingSecretChecksum` | string | `""` | Required whenever existingSecret is set: the sha256 of the `instance` document inside that Secret, as 64 lowercase hex characters. The chart cannot read the Secret, so this is what the pod annotation carries; without it a config change would apply cleanly, roll no pods and report success. The shape is constrained because the value lands in an annotation: a blank or YAML-null string would render as a constant, and one carrying a colon or newline would corrupt the pod template. |
| `instance.superuserSecret` | string | `""` | Name of the Secret holding the superuser's seed password under key `password`, projected into user-management as DC_SUPERUSER_PASSWORD and read only to seed an empty identity table. Empty means dci-&lt;id&gt;-superuser, the Secret dcctl bootstrap generates per instance. There is no default password: an install made without dcctl must create this Secret before user-management first starts, or it refuses to seed the superuser. |
| `instance.config` | object | see values.yaml | The instance configuration document (infrastructure, persistence, per-service settings), written into the dci-&lt;id&gt;-config Secret. The chart validates only the envelope; each service validates its own part at startup. Ignored when existingSecret is set. |
| `image` | object | - | Container image selection for the platform's services. |
| `image.registry` | string | `"ghcr.io/devicechain-io"` | Registry and organisation the service images are pulled from; each area image is &lt;registry&gt;/&lt;area&gt;:&lt;tag&gt; unless the area sets its own image. |
| `image.tag` | string | `""` | Image tag for every service. Empty uses the chart's appVersion, which a release sets to the release tag, so a packaged chart deploys the matching images with no override. |
| `image.pullPolicy` | string | `"IfNotPresent"` | Kubernetes imagePullPolicy applied to every container. |
| `service` | object | - | The Service exposed for each functional area. |
| `service.port` | integer | `8080` | HTTP port every service listens on. It serves the GraphQL API, /healthz, /readyz and /metrics. |
| `metrics` | object | - | Prometheus integration. Metrics are served on the same HTTP port as GraphQL, at /metrics. `enabled` renders a ServiceMonitor per area and requires the Prometheus Operator CRDs; the other keys here are ignored while it is false. See the individual keys for the dashboards, alert rules and PodMonitors it gates. |
| `metrics.enabled` | boolean | `true` | Render one ServiceMonitor per enabled functional area, scraping /metrics on the named graphql port. Requires the Prometheus Operator CRDs; set false on a cluster without them. |
| `metrics.grafanaDashboards` | boolean | `true` | Render one ConfigMap per bundled operations dashboard (event-processing and command-delivery today), labelled for the Grafana sidecar to import. Each board is this instance's own copy with its own uid and title. Set false on clusters that have the Prometheus Operator but no Grafana sidecar. Ignored when metrics.enabled is false. |
| `metrics.alerts` | boolean | `true` | Render the PrometheusRules for the platform's own services: event-processing, dead-letter and command-delivery rules, among others. Requires the Prometheus Operator CRDs. The database and replication rules are gated separately because they describe infrastructure this chart does not install. Ignored when metrics.enabled is false. |
| `metrics.natsPodMonitor` | boolean | `true` | Render a PodMonitor scraping the NATS broker's own exporter sidecar, for broker-side cluster health (routes, JetStream RAFT). It targets the namespace in instance.config.infrastructure.nats.hostname. Safe to leave on when the sidecar is absent (no targets are generated); set false for a broker this chart should not monitor. Ignored when metrics.enabled is false. |
| `metrics.natsBrokerHost` | string | `""` | The broker's &lt;service&gt;.&lt;namespace&gt; hostname, restated for the PodMonitor. Needed only with instance.existingSecret, where the chart cannot read instance.config and would otherwise derive the target namespace from its own default. |
| `metrics.databaseBackups` | boolean | `true` | Render the WAL-archiving and base-backup alerting rules. It must track whether the infrastructure actually archives, which is decided outside this chart: dcctl sets it from the database_backups_enabled OpenTofu output, and a hand-run install against an instance with backups off should set false. Left true with archiving off the rules load but can never fire. Ignored when metrics.enabled or metrics.alerts is false. |
| `metrics.databaseBackupSnapshots` | boolean | `false` | Declare that the databases take their daily base backup as a CSI volume snapshot, with a weekly base backup still going to the backup store. Renders the snapshot rules and moves the base-backup alert from 36 hours to 8.5 days. Like databaseBackups it must match what the infrastructure does; dcctl sets it from the database_backup_snapshot_class output. Ignored unless databaseBackups is on. |
| `metrics.databaseNamespace` | string | `"dc-system"` | Namespace the CloudNativePG database Clusters run in, where their archiver and backup metrics are exported and where their volumes are reported. It is not the instance's own namespace (dci-&lt;id&gt;): a rule scoped to that would select no series and never fire. It also gates the database volume-fullness rules, which are not behind databaseBackups. |
| `metrics.cnpgNamespace` | string | `"cnpg-system"` | Namespace the CloudNativePG operator and the Barman Cloud plugin run in (the database control plane, distinct from the databases). Gates both the control-plane alert rules and a diagnostic PodMonitor over the operator. Empty disables both; dcctl sets it from the cnpg_namespace OpenTofu output, which is null when the operator was not installed. Ignored when metrics.enabled is false; the rules also need metrics.alerts. |
| `replicas` | integer | `1` | Default replica count for each functional area's Deployment; an area overrides it with functionalAreas.&lt;area&gt;.replicas. |
| `rollingUpdate` | object | - | Rolling-update strategy for the area Deployments. The defaults (maxUnavailable 0, maxSurge 1) bring a Ready replacement up before an old pod is removed, so an upgrade keeps full capacity. |
| `rollingUpdate.maxUnavailable` | integer or string | `0` | Maximum pods that may be unavailable during a rollout, as a count or a percentage. Default 0. |
| `rollingUpdate.maxSurge` | integer or string | `1` | Maximum pods above the desired count during a rollout, as a count or a percentage. Default 1. |
| `terminationGracePeriodSeconds` | integer | `30` | Graceful-shutdown window in seconds. Rendered into the pod spec and into the instance config, where the services validate it against shutdownDrainSeconds and refuse to start if the drain leaves no room for the teardown that follows it. |
| `nodeLossTolerationSeconds` | integer or null | `30` | Seconds a pod tolerates its node being NotReady/unreachable before eviction. null leaves Kubernetes' 300s default. MUST be a bare integer, not a duration: `30s` would coerce to 0, which evicts every pod the instant a node is tainted. |
| `shutdownDrainSeconds` | integer | `5` | Readiness-drain window in seconds before a service tears down, written into the instance config as infrastructure.shutdown.drainSeconds. It may take at most half of terminationGracePeriodSeconds, because the drain only waits for endpoint removal to propagate and the teardown after it finishes in-flight work; a larger value is refused at startup. 0 skips the drain, which suits a single-instance run with no Service to be pulled out of. |
| `goMemLimitPercent` | integer | `0` | GOMEMLIMIT as a percentage of each container's own memory LIMIT. Go derives GOMAXPROCS from the cgroup CPU limit but does NOT derive GOMEMLIMIT from the memory limit. This is a CEILING against an OOMKill during a live-heap spike, not a footprint reduction — measurement found no heap reduction and a GC CPU cost — so it defaults to 0 (off). Capped below 100 deliberately: the cgroup limit also covers goroutine stacks and runtime bookkeeping outside the Go heap, so aiming at the full limit OOMKills instead of collecting. |
| `goMemLimit` | string | `""` | An explicit GOMEMLIMIT for every area, bypassing goMemLimitPercent. Go's own syntax (e.g. "192MiB"). Prefer the percentage — a pinned value keeps throttling a service whose memory limit was later raised. |
| `resources` | object | - | Default container resources for every area; an area's own functionalAreas.&lt;area&gt;.resources is merged over it key by key. Requests are mandatory and must not be empty, so pods are Burstable or Guaranteed rather than BestEffort QoS. |
| `resources.requests` | object | `{"cpu":"100m","memory":"128Mi"}` | Default resource requests (cpu, memory); must not be empty. |
| `resources.limits` | object | `{"cpu":"500m","memory":"256Mi"}` | Default resource limits (cpu, memory). |
| `useMeasuredRequests` | boolean | `true` | Apply each area's functionalAreas.&lt;area&gt;.measuredRequests (its CPU use at 6,000 events/s; event-processing's at 6,800) over the top-level resources.requests. dcctl install --compact sets false, so the top-level requests reach every area. |
| `serviceAccount` | object | - | The ServiceAccount the area Deployments run as, in place of the namespace's default one. |
| `serviceAccount.create` | boolean | `true` | Render a dedicated ServiceAccount for the release. The data-plane services never call the Kubernetes API. |
| `serviceAccount.name` | string | `""` | ServiceAccount name. Empty uses dc-&lt;instance.id&gt;. |
| `serviceAccount.automountToken` | boolean | `false` | Mount the ServiceAccount API token into the pods. Default false, since the services do not use it. |
| `networkPolicy` | object | - | Optional egress NetworkPolicy for outbound-connectors, a network-level second layer over the egress guard every tenant connector already dials through. Off by default. It only has effect on a CNI that enforces NetworkPolicy, and traffic it denies is dropped rather than refused, so a missing peer shows up as timeouts. The blocked address space is fixed in the template; the old blocked-range keys are rejected, and additionalAllowedCidrs is the way to permit a destination. |
| `networkPolicy.enabled` | boolean | `false` | Render the egress NetworkPolicy for outbound-connectors. Check that your CNI enforces policy before treating it as a control. |
| `networkPolicy.infrastructureNamespaceSelector` | object | `{"devicechain.io/component":"infrastructure"}` | Labels selecting the namespace that holds NATS and PostgreSQL. The OpenTofu-created infrastructure namespace carries the default pair. If you brought your own, set the labels it actually has; an empty selector is refused at render time. |
| `networkPolicy.dnsNamespaceSelector` | object | `{"kubernetes.io/metadata.name":"kube-system"}` | Labels selecting the namespace that serves cluster DNS. The default matches kube-system on any cluster that labels namespaces with kubernetes.io/metadata.name (Kubernetes 1.21 and later). |
| `networkPolicy.additionalAllowedCidrs` | array | `[]` | Extra destinations to permit, as CIDRs. Two reasons to need it: a CNI that evaluates egress before service load-balancing, so Service ClusterIPs match no pod selector and must be allowed by their Service CIDR (kubeadm default 10.96.0.0/12), and a tenant's broker or webhook on a private address such as a peered VPC. Each entry opens that range for every tenant connector, so keep it as narrow as you can. It does not affect mail, which is notification-management's path. |
| `networkPolicy.externalConfigPorts` | object | - | Only for instance.existingSecret deployments: restates the NATS and relational-database ports the egress rule would otherwise read from instance.config, which the chart cannot see there. Both keys required when set. |
| `networkPolicy.externalConfigPorts.nats` | integer | - | NATS port for the egress rule. |
| `networkPolicy.externalConfigPorts.rdb` | integer | - | Relational-database port for the egress rule. |
| `podDisruptionBudget` | object | - | PodDisruptionBudget per area, so a node drain cannot evict every replica at once. Emitted only for areas with more than one replica, since a budget on a single replica would block voluntary drains. Set exactly one of minAvailable and maxUnavailable. |
| `podDisruptionBudget.enabled` | boolean | `true` | Render the PodDisruptionBudgets. |
| `podDisruptionBudget.minAvailable` | integer or string | `""` | Minimum pods that must stay available, as a count or percentage. Leave empty when using maxUnavailable. |
| `podDisruptionBudget.maxUnavailable` | integer or string | `1` | Maximum pods that may be unavailable, as a count or percentage. Leave empty when using minAvailable. |
| `startupProbe` | object | - | Startup probe on /readyz. It gives a slow cold start (identity keys, database) up to periodSeconds times failureThreshold to come up before the liveness probe can act. |
| `startupProbe.periodSeconds` | integer | `5` | Seconds between startup probe attempts. |
| `startupProbe.failureThreshold` | integer | `30` | Consecutive startup probe failures before the container is restarted. |
| `livenessProbe` | object | - | Liveness probe on /healthz. Readiness gates Service endpoints on /readyz separately. |
| `livenessProbe.failureThreshold` | integer | `6` | Consecutive liveness failures before the container is restarted; kept high so a transient blip does not kill a healthy pod. |
| `podSecurityContext` | object | see values.yaml | Pod-level securityContext. The hardened defaults run the workload as the non-root uid 65532 with the RuntimeDefault seccomp profile; relax them only if you build images that need to run as root. |
| `securityContext` | object | see values.yaml | Container-level securityContext. The hardened defaults drop all Linux capabilities, forbid privilege escalation and use a read-only root filesystem; the service images are static binaries that write nothing to the root filesystem. |
| `ingress` | object | - | Optional Ingress + cert-manager TLS exposing the GraphQL/HTTP surface (requires the OpenTofu ingress-nginx + cert-manager). |
| `ingress.enabled` | boolean | `false` | Render the API and web Ingress objects. Each enabled area is reachable at https://&lt;host&gt;/api/&lt;area&gt;/graphql and the console at https://&lt;host&gt;/. |
| `ingress.className` | string | `"nginx"` | IngressClass the controller registered (the OpenTofu ingress_class). |
| `ingress.host` | string | `"devicechain.local"` | Host name both Ingress objects route. |
| `ingress.annotations` | object | `{}` | Extra annotations merged onto the Ingress objects (body size, timeouts and so on). |
| `ingress.tls` | object | - | TLS for the Ingress, using cert-manager. |
| `ingress.tls.enabled` | boolean | `true` | Terminate TLS at the Ingress. |
| `ingress.tls.selfSigned` | boolean | `true` | Render a cert-manager self-signed Issuer in the instance namespace: valid TLS that browsers do not trust, for development. For real certificates set false and name a ClusterIssuer in clusterIssuer. |
| `ingress.tls.issuerName` | string | `"devicechain-selfsigned"` | Name of the self-signed Issuer the chart renders when selfSigned is true. |
| `ingress.tls.clusterIssuer` | string | `""` | Name of an existing cert-manager ClusterIssuer (for example a Let's Encrypt ACME issuer) to request certificates from. Used when selfSigned is false. |
| `ingress.tls.secretName` | string | `"devicechain-tls"` | Name of the Secret that holds the TLS certificate. |
| `frontend` | object | - | The web console workload: a static nginx SPA served at the ingress root. Disable for headless/ingest-only instances. |
| `frontend.enabled` | boolean | `true` | Deploy the web console. Disable for headless or ingest-only instances. |
| `frontend.image` | object | - | Console image override. By default the image is &lt;image.registry&gt;/frontend:&lt;image.tag&gt;. |
| `frontend.image.repository` | string | `""` | Full image repository for the console, overriding the registry-derived default. |
| `frontend.image.tag` | string | `""` | Console image tag, overriding image.tag. |
| `frontend.replicas` | integer | `1` | Replica count for the console Deployment. |
| `frontend.resources` | object | see values.yaml | Container resources for the console, a small static nginx image. |
| `blobStorage` | object | - | Kubernetes volume wiring for the filesystem object store (instance.config.infrastructure.blob.backend set to filesystem). Creates and mounts the PVC that backs instance.config.infrastructure.blob.directory, which today holds branding logos. Ignored for the s3 backend; the render fails on a persistence and backend mismatch. |
| `blobStorage.persistence` | object | - | The PersistentVolumeClaim that backs the filesystem object store. |
| `blobStorage.persistence.enabled` | boolean | `false` | Create a PersistentVolumeClaim for the filesystem store and mount it into the consuming areas. Requires the filesystem backend and a resolved mount path. |
| `blobStorage.persistence.existingClaim` | string | `""` | Name of a pre-created PVC to use instead of having the chart create one (for example an RWX claim provisioned elsewhere). When set, size, accessModes and storageClass are ignored and no PVC is rendered, but the volume is still mounted into mountAreas. |
| `blobStorage.persistence.mountPath` | string | `""` | Volume mount path; defaults to blob.directory (must match it). Set only when the instance config is supplied via instance.existingSecret and the chart cannot read blob.directory. |
| `blobStorage.persistence.accessModes` | array | `["ReadWriteOnce"]` | PVC access modes. ReadWriteOnce suits a single replica; for more than one replica of a consuming area use ReadWriteMany with an RWX-capable storage class, or the s3 backend. |
| `blobStorage.persistence.size` | string | `"5Gi"` | Requested PVC size. |
| `blobStorage.persistence.storageClass` | string | `""` | StorageClass for the PVC. Empty uses the cluster default; set an RWX-capable class for multi-replica use; "-" selects no storage class, for a statically provisioned volume. |
| `blobStorage.persistence.annotations` | object | `{}` | Extra annotations merged onto the PVC (for example a backup policy). |
| `blobStorage.mountAreas` | array | `["user-management"]` | Functional areas that mount the blob volume at the store directory (default: user-management). |
| `functionalAreas` | object | see values.yaml | Per-functional-area settings, keyed by area name (user-management, device-management, event-sources and so on). Only enabled areas are rendered. Every key an area may set is listed under functionalAreas.&lt;area&gt;. |
| `functionalAreas.<area>.image` | string | - | Full image reference for this area, overriding &lt;image.registry&gt;/&lt;area&gt;:&lt;image.tag&gt;. |
| `functionalAreas.<area>.replicas` | integer | - | Replica count for this area, overriding the top-level replicas. |
| `functionalAreas.<area>.strategy` | string | - | Deployment rollout strategy for this area: RollingUpdate (the default) or Recreate, which stops every old pod before starting a new one. The template refuses Recreate above one replica. event-processing ships with Recreate and is the one stateful single-owner area that may run more than one replica: the extras are warm standbys arbitrated by a partition lease, and running them means setting RollingUpdate as well. sparkplug-ingest, lwm2m-ingest and mcp are held at one serving pod regardless of strategy, because they own a broker session, a UDP socket or in-memory client sessions respectively. |
| `functionalAreas.<area>.resources` | object | - | Container resources for this area, merged key by key over the top-level resources: set only the keys that differ (e.g. limits.cpu). A request above the merged limit is refused at render. |
| `functionalAreas.<area>.resources.requests` | object | - | Resource requests for this area, merged over the top-level requests. |
| `functionalAreas.<area>.resources.limits` | object | - | Resource limits for this area, merged over the top-level limits. |
| `functionalAreas.<area>.resources.claims` | array | - | Core-v1 resource claims (dynamic resource allocation) for this area's container. |
| `functionalAreas.<area>.goMemLimit` | string | - | GOMEMLIMIT for this area only, overriding the derivation. Use for a service whose working set genuinely differs (e.g. event-processing holds more live state than the CRUD areas). |
| `functionalAreas.<area>.config` | object | - | This area's own configuration, written to the dct-&lt;id&gt;-config object and mounted at /etc/dct-config/&lt;area&gt;. Empty means the service applies its typed defaults; an unknown key stops the service at startup. |
| `functionalAreas.<area>.extraPorts` | array | - | Extra Service/container ports for this area beyond the 8080 graphql port (e.g. event-sources HTTP device ingest on 8081). Port names must be &lt;=15 chars per Kubernetes. |
| `functionalAreas.<area>.extraPorts[].name` | string | - | Port name; at most 15 characters, per Kubernetes. |
| `functionalAreas.<area>.extraPorts[].port` | integer | - | Port number, 1 to 65535. |
| `functionalAreas.<area>.extraPorts[].protocol` | string | - | Port protocol; defaults to TCP. lwm2m-ingest sets UDP for its CoAPS port. |
| `functionalAreas.<area>.extraEnv` | array | - | Extra container environment variables for this area, passed through verbatim as core-v1 EnvVar entries. Used to project a Secret into an env var — e.g. sparkplug-ingest reads each per-tenant broker password from the env var its source's broker.passwordEnv names, so an operator sets an entry here with valueFrom.secretKeyRef. |
| `functionalAreas.<area>.extraEnv[].name` | string | - | Environment variable name. |
| `functionalAreas.<area>.extraEnv[].value` | string | - | Literal value. Use valueFrom instead to project a Secret. |
| `functionalAreas.<area>.extraEnv[].valueFrom` | object | - | Core-v1 EnvVarSource, for example a secretKeyRef. |
| `functionalAreas.<area>.measuredRequests` | object | - | CPU request: the area's measured use at 6,000 events/s (event-processing: at 6,800), applied over the top-level resources.requests while useMeasuredRequests is true. The area's own resources.requests wins over it. |
| `functionalAreas.<area>.measuredRequests.cpu` | string or number | - | CPU request, as a Kubernetes quantity or a number of cores. |
| `functionalAreas.<area>.avoidEventStorePrimary` | boolean | - | Prefer (never require) a node that is not running this instance's event-store primary. |
| `functionalAreas.<area>.eventPathSpread` | boolean | - | Prefer (never require) a node running fewer of this instance's event-path services: every area with this set counts. Shipped on device-management, event-management, device-state, event-sources and event-processing. Above one replica, also prefers a node not running another of this area's own pods; off keeps the cluster's default spread instead. |
| `functionalAreas.<area>.profiler` | object | - | Opt-in profiling listener for this area: Go runtime profiles (CPU, heap, goroutines, execution trace) on a port of its own, never on the Service or the ingress. Off by default. Turning it on restarts this area's pods only. |
| `functionalAreas.<area>.profiler.enabled` | boolean | - | Serve profiles from this area's pods. Default false. |
| `functionalAreas.<area>.profiler.address` | string | - | IP address and port the listener binds. Default 127.0.0.1:6060, the pod's loopback address, reachable only with kubectl port-forward. Any other address is reachable by anything that can reach the pod, without authentication. |
<!-- values:end -->
