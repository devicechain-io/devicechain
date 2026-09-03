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
  --version 1.2.0 \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

Infrastructure (NATS, TimescaleDB, ingress, TLS) is provisioned separately by the
OpenTofu modules in
[`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu);
this chart assumes it exists and points at it via
`instance.config.infrastructure` / `.persistence`.

Every release is one semver tag (`vX.Y.Z`) covering all images, the operator, the
chart, and the `dcctl` CLI together. Images are public on `ghcr.io/devicechain-io`
(multi-arch, distroless nonroot), so nothing is built locally. The chart's OCI tag
is that same version without the leading `v`; `image.tag` keeps it.

### Installing from a checkout

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
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
helm install dc deploy/helm/devicechain --set profile=telemetry
# or an explicit set:
helm install dc deploy/helm/devicechain \
  --set profile= \
  --set 'enabledFunctionalAreas={user-management,device-management,event-sources}'
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
> 256-bit key (`openssl rand -base64 32`) — is required for any profile carrying an
> area that owns an envelope-encrypted secret store:
> `notification-management` (in `default`), `outbound-connectors`, and `ai-inference`.
> Such a service cannot form its KEK and refuses to start without it, so the chart
> **fails the render** rather than shipping a crash-loop. `dcctl bootstrap` mints one
> automatically. The chart deliberately does NOT generate one: Helm's random functions
> re-run on every upgrade, which would rotate the KEK and orphan every stored secret.

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
503 first, waits `shutdownDrainSeconds` (default 5, under the 30s
`terminationGracePeriodSeconds`) for endpoint removal to propagate, then drains
in-flight requests — an app-side drain, since the `FROM scratch` images have no shell
for a `preStop` hook. Database migrations run under a Postgres advisory lock so
concurrently-rolling replicas don't race on DDL.

For true zero-downtime run `replicas: 2`+ per area (`--set replicas=2` or
`functionalAreas.<area>.replicas`); a `PodDisruptionBudget` is rendered for any area
with more than one replica. Tune the strategy via `rollingUpdate.maxUnavailable` /
`rollingUpdate.maxSurge`.

## Bounding tenant egress (optional)

A tenant configures its own delivery destinations — a webhook URL, an SMTP relay, an MQTT
or Kafka broker, an SNS topic. DeviceChain refuses a destination that resolves to a
private, loopback, link-local, carrier-NAT or cloud-metadata address, and it does so at
the moment the connection is dialled rather than when the URL is saved, because a hostname
can resolve differently between the two.

That covers the webhook, HTTP-call and SMTP paths. It cannot cover MQTT, Kafka or AWS
SNS/SQS: those are built inside an embedded stream engine that exposes no place to hook a
dialer. For Kafka an address check would not be enough even with one — the client dials
the bootstrap broker and then registers whatever addresses that broker returns in its
metadata, so the tenant's own broker chooses the second hop. The network layer is the only
boundary those three have.

`networkPolicy.enabled=true` renders an egress `NetworkPolicy` for `outbound-connectors`
that permits DNS, the platform's own datastores and services, and the public internet —
and nothing else.

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

- The datastore rule permits the whole namespace on the NATS and PostgreSQL ports, not
  the two workloads by name. So a second datastore living in that namespace on one of
  those ports is reachable even though only NATS and PostgreSQL are intended. Narrowing
  it needs a pod selector per datastore, and a way to express the labels of
  bring-your-own infrastructure; until then, treat "the platform's datastores" as "that
  namespace, on those two ports".

- It compares address prefixes, so it cannot look inside an IPv6 address that carries an
  IPv4 one. On a NAT64 or dual-stack cluster a tenant broker at a translated address
  reaches what the translated address points at, including a metadata service, on exactly
  the paths this policy exists to bound. Closing that needs the policy generated from your
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

- A `Namespace` named `instance.id` (toggle with `instance.createNamespace`).
- `dci-<id>-config` — instance config mounted at `/etc/dci-config/instance`.
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
