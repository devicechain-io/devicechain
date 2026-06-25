# DeviceChain Helm chart

Renders the DeviceChain shared-microservice workloads — one Deployment + Service
per **enabled functional area**, plus the instance and per-service config
ConfigMaps — under the shared-microservice model (ADR-001 / ADR-022).

## Install

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain
```

Infrastructure (NATS, TimescaleDB, ingress, TLS) is provisioned separately by
OpenTofu (ADR-002); this chart assumes it exists and points at it via
`instance.config.infrastructure` / `.persistence`.

## Choosing what to deploy (ADR-022 decision 2)

Set **either** a named `profile` **or** an explicit `enabledFunctionalAreas`
list (not both). An empty selection defaults to `full`.

| Profile | Functional areas |
|---|---|
| `full` | user-management, device-management, event-sources, event-management, device-state, command-delivery |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state |
| `ingest-only` | user-management, device-management, event-sources |

```bash
helm install dc deploy/helm/devicechain --set profile=telemetry
# or an explicit set:
helm install dc deploy/helm/devicechain \
  --set profile= \
  --set 'enabledFunctionalAreas={user-management,device-management,event-sources}'
```

The chart **fails the render** if the selection omits a required core area
(`user-management`, `device-management`) or an enabled area's hard dependency —
the install-time enforcement of the decision-2 dependency gate. `user-management`
and `device-management` are the required core; the other four are independently
optional. (The dependency catalog mirrors `backend/k8s/functionalarea`, the Go
source of truth.)

## Per-service configuration

Each service loads its typed config fail-closed (ADR-022 decision 1). Override it
per area under `functionalAreas.<area>.config`; an unset config renders `{}` and
the service applies its own defaults:

```yaml
functionalAreas:
  device-management:
    config:
      deviceAuthMode: required   # disabled | optional | required
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
        port: 8081   # POST /dc/{tenant}/events
```

`values.schema.json` validates the deployment-selection envelope (profile enum,
area names, image/instance shape) at `helm install`/`upgrade` time.

## What it renders

- A `Namespace` named `instance.id` (toggle with `instance.createNamespace`).
- `dci-<id>-config` — instance config mounted at `/etc/dci-config/instance`.
- `dct-<id>-config` — per-area config mounted at `/etc/dct-config/<area>`.
- Per enabled area: a `Deployment` (with `/readyz` readiness + `/healthz`
  liveness probes, ADR-022 decision 3) and a `Service` on the GraphQL port (plus
  any `extraPorts` for the area, e.g. event-sources' HTTP ingest on 8081).
- Optional (`ingress.enabled=true`): an `Ingress` exposing each enabled area at
  `https://<host>/<area>/graphql`, plus a cert-manager TLS `Issuer` (self-signed
  by default). Requires the ingress-nginx controller + cert-manager from
  [`deploy/opentofu`](../../opentofu).
