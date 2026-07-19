# DeviceChain infrastructure (OpenTofu)

Provisions the **in-cluster data-plane dependencies** DeviceChain assumes already
exist (ADR-002): **NATS** (core messaging + MQTT ingress + JetStream KV),
**TimescaleDB** (event hypertables), and the relational **Postgres** (control-plane
RDB). The K8s operator and the service workloads (rendered by
[`deploy/helm/devicechain`](../helm/devicechain)) connect to what this stands up.

## Scope

This root is **cluster-agnostic**: it deploys into an **existing** cluster through
the `kubernetes` and `helm` providers (kubeconfig-supplied), so it runs the same on
kind, k3s, EKS, or GKE. Provisioning the cluster itself is intentionally out of
scope — a cloud-specific root (e.g. EKS/GKE) can provision a cluster and then wrap
these modules, passing its cluster credentials to the providers.

| Provisioned | How | Endpoint (defaults) |
|---|---|---|
| Namespace | `kubernetes_namespace_v1` | `dc-system` |
| NATS (JetStream + MQTT) | `nats` Helm chart | `dc-nats.dc-system:4222` / `:1883` |
| Relational Postgres | StatefulSet + headless Service + Secret | `dc-postgresql.dc-system:5432` |
| TimescaleDB | same generic module, Timescale image | `dc-timescaledb-single.dc-system:5432` |
| NGINX ingress controller | `ingress-nginx` Helm chart | IngressClass `nginx` |
| cert-manager (+ CRDs) | `cert-manager` Helm chart | namespace `cert-manager` |
| Observability (Prometheus/Grafana/Alertmanager) | `kube-prometheus-stack` Helm chart | namespace `monitoring` |

The ingress controller and cert-manager are the TLS/ingress *capability*; the
per-instance **Ingress resource + cert Issuer** that route to the app Services are
rendered by the Helm chart (it knows the enabled functional areas) — set
`ingress.enabled=true` there. Both are toggleable (`enable_ingress_nginx`,
`enable_cert_manager`) if the cluster already has them.

The monitoring stack installs the Prometheus Operator CRDs the DeviceChain chart's
ServiceMonitors / PrometheusRule / dashboard ConfigMaps depend on, so it applies
**before** the Helm step and the chart's `metrics.enabled` rendering "just works".
It is default-on (`enable_monitoring`); set `monitoring_slim=true` on a local/kind
cluster (emptyDir TSDB, smaller requests). Grafana auth is native admin for now
(`monitoring_grafana_admin_password`); OIDC via user-management (ADR-047), gated to
the operator/superuser tier, is a follow-up. Reach Grafana with
`kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80`.

These endpoints line up with the Helm chart's `values.yaml` defaults, so the chart
deploys against this infra with no extra wiring.

## Usage

```bash
cd deploy/opentofu
cp terraform.tfvars.example terraform.tfvars   # edit: kubeconfig, credentials, pinned versions
tofu init
tofu plan
tofu apply
```

(`terraform` works identically — the HCL is provider-compatible.)

## Data durability

The databases (relational Postgres + TimescaleDB) are treated as **lifecycle-
independent and durable** — they must survive app upgrades/uninstalls *and*
accidental destroys (methodology §11). Three guards back this up:

- **Retain StorageClass (recommended for production).** The data volume's
  `storage_class_name` is set from `postgres_storage_class` / `timescale_storage_class`.
  Empty (the default) uses the cluster default StorageClass, whose `reclaimPolicy`
  is typically `Delete` — fine for local dev, but PVC/PV deletion then destroys the
  data. **For production, point these at a StorageClass whose `reclaimPolicy` is
  `Retain`**, so the underlying volume (and its data) survives PVC/PV deletion and a
  redeploy can re-attach the existing volume.
- **PVC retention policy.** Each DB StatefulSet sets
  `persistent_volume_claim_retention_policy { when_deleted = "Retain"  when_scaled = "Retain" }`,
  so deleting or scaling the StatefulSet never reaps the data PVCs.
- **`prevent_destroy` backstop.** Each DB StatefulSet carries
  `lifecycle { prevent_destroy = true }`. This is **intentional**: a naive
  `tofu destroy` will *refuse* to remove the databases (and therefore refuses to
  destroy this whole root). Intentional teardown requires removing the guard, or
  removing the DBs from state / targeting around them — the "databases outlive the
  deployment" guarantee.

**Planned next step:** split this OpenTofu root into a durable **data stack**
(PG/Timescale/NATS JetStream — Retain, prevent_destroy, rarely touched) with its
own state, separate from the disposable **platform stack** (ingress, cert-manager),
so the data tier can be applied/destroyed fully independently (methodology §11).
Scheduled backups (`pg_dump` / volume snapshots) are a belt-and-suspenders
fast-follow.

## Notes & scope boundaries

- **Single-node by default.** The `ha` variable is threaded through for the
  ADR-020 HA topology (3-node NATS RAFT, DB replication) but defaults to
  single-node for the launch slice — HA is a later toggle, not a rewrite.
- **TimescaleDB extension.** The Timescale image preloads the `timescaledb`
  library; the application creates the extension and hypertables on migrate.
- **Credentials.** Passwords default to `devicechain` for local dev. Override via
  `terraform.tfvars` (gitignored) or a pre-created Secret for any real deploy.
- **Pin versions.** Set `nats_chart_version` and `timescale_image` to tested
  versions for reproducible deploys (the defaults float to latest).
- **JetStream disk ceiling.** `nats_jetstream_max_file_store` is the hard aggregate
  JetStream file-store bound (ADR-023); it defaults to 90% of `nats_jetstream_storage`
  for filesystem headroom, **floored to a whole unit of that size's own magnitude** —
  a 12Gi PV yields a 10Gi ceiling, not 10.8Gi. Lowering it below a stream's current on-disk usage on an
  existing cluster causes immediate `DiscardOld` eviction of the overflow — a
  non-issue on a fresh bring-up, but size it before a running cluster fills.
- **Not yet here (next slice):** the ADR-020 HA topology + the broker-enforced
  transport security (TLS + NATS auth-callout) it unblocks.
