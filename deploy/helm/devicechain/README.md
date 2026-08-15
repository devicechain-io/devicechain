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
- Optional (`ingress.enabled=true`): two `Ingress` objects on one host — the API
  ingress routes `https://<host>/api/<area>/graphql` to each enabled area
  (stripping the `/api/<area>` prefix), and the web ingress serves the console at
  `https://<host>/`. Plus a cert-manager TLS `Issuer` (self-signed by default).
  Requires the ingress-nginx controller + cert-manager from
  [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu).
