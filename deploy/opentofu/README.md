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
| Relational Postgres | `cnpg-cluster` module — a CloudNativePG `Cluster` (`dc-rdb`) | `dc-postgresql.dc-system:5432` |
| TimescaleDB (event store) | `cnpg-cluster` module — a CloudNativePG `Cluster` (`dc-tsdb`) | `dc-timescaledb-single.dc-system:5432` |
| NGINX ingress controller | `ingress-nginx` Helm chart | IngressClass `nginx` |
| cert-manager (+ CRDs) | `cert-manager` Helm chart | namespace `cert-manager` |
| Observability (Prometheus/Grafana/Alertmanager) | `kube-prometheus-stack` Helm chart | namespace `monitoring` |
| CloudNativePG operator (+ CRDs) | `cloudnative-pg` Helm chart, pinned | namespace `cnpg-system` |
| Barman Cloud backup plugin | `plugin-barman-cloud` Helm chart, pinned | namespace `cnpg-system` |

**Both CNPG charts require Kubernetes ≥ 1.29** (`kubeVersion: '>=1.29.0-0'`), which is
therefore the floor for this root as a whole.

### Both database stores are CloudNativePG Clusters

They share one module and differ in four values: the image, the instance count, the
durability, and the bootstrap SQL. The relational store runs `required` durability and the
event store runs `preferred`, which is a decision rather than a default — see the module.

Three things about them are worth knowing before changing either:

- **The Cluster is `dc-rdb`; clients still use `dc-postgresql`.** The second is a
  *managed* alias Service whose selector CNPG maintains, so it follows the primary
  across a failover (measured: 3 seconds). The names must differ because CNPG reserves
  `<cluster>-rw`/`-ro`/`-r`. A hand-rolled Service of the same name would look identical
  and would not follow anything — nor would its name appear in the server certificate's
  SANs, which is what keeps `sslmode=verify-full` reachable later.
- **`postgres_instances` is 1 or 3, never 2.** Synchronous replication needs a standby to
  confirm every commit, so at two instances losing either one stalls all writes. CNPG's
  own admission webhook does not refuse that topology — it refuses only `number >=
  instances` — so the guard is ours, in the root variable and again in the chart.
- **CNPG provisions declaratively; it does not *enforce* continuously.** Measured on a
  probe cluster: a deleted alias Service is recreated, but a hand-edited selector is
  never repaired, and a `CREATEDB` grant revoked by hand stays revoked. Treat the
  operator's green status as a record of the last apply, not a live claim — which is why
  `dcctl ha verify-db` reads PostgreSQL and the Kubernetes API rather than the CR status.

The CloudNativePG operator is installed on **every** apply, not only HA ones (ADR-020
A2, decision D4): backup is not a high-availability feature, and one storage shape
means HA becomes an instance count rather than a migration. Both databases above are
`Cluster` resources it owns.

🔴 `enable_database_backups` requires `enable_cert_manager`, and the requirement is a
hard one: the plugin's chart renders an Issuer and two Certificates, so with no
cert-manager CRDs its release fails and takes the apply with it. If you set
`enable_cert_manager = false` because the cluster *already has* cert-manager, leave
backups on — the CRDs are what matter, not who installed them. If you set it false
because there is no cert-manager at all, set `enable_database_backups = false` too.
`dcctl` keeps the pair consistent on the one path where it drops cert-manager
(`--compact --no-tls`); running `tofu` by hand, it is yours to keep consistent.

`enable_cnpg = false` is the escape hatch for a cluster that already runs CNPG — Helm
cannot adopt objects installed by the upstream `kubectl apply` manifest, so without it
the apply fails with an ownership error. `dcctl bootstrap --no-cnpg` sets it.

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
  `Retain`**, so the underlying volume and its data outlive PVC/PV deletion and are
  still there to be recovered FROM.

  🔴 That is where the guarantee ends, and an earlier version of this bullet claimed
  more: it said "a redeploy can re-attach the existing volume". **It cannot.** A
  retained PV whose PVC is deleted goes to `Released` and will not bind a new claim
  until someone clears its `claimRef` by hand, and a new CloudNativePG `Cluster`
  bootstraps through `bootstrap.initdb` — it never adopts a pre-existing PGDATA (the
  cutover guard in main.tf says the same thing about StatefulSet data). A `Retain`
  class buys a volume you can still get data OFF; it does not buy a redeploy that
  comes back up on it. The supported recovery path is `bootstrap.recovery` from a
  backup, which no `Cluster` here declares yet (A2.5).
- 🔴 **The Cluster OWNS its PVCs, and this is SHARPER than the topology it replaced.**
  The old StatefulSets set `persistent_volume_claim_retention_policy { when_deleted =
  "Retain" }`, so deleting one left the data volumes behind. CloudNativePG has no
  equivalent — the PVCs are owned by the `Cluster`, so deleting it garbage-collects
  them. Removing a store takes its data with it rather than orphaning a volume.
- **`prevent_destroy` backstop.** Each database `helm_release` carries
  `lifecycle { prevent_destroy = true }`, so a naive `tofu destroy` *refuses* to
  remove the databases (and therefore refuses to destroy this whole root).

  🔴 It protects a resource that is **in the configuration**. It does NOT protect one
  removed FROM the configuration: a resource whose module block is deleted becomes an
  orphan, and orphans are destroyed without consulting a `lifecycle` block that is no
  longer there to consult. That is what the plan-time cutover guard in `main.tf` is
  for, and it is measured rather than assumed.

**Planned next step:** split this OpenTofu root into a durable **data stack**
(PG/Timescale/NATS JetStream — Retain, prevent_destroy, rarely touched) with its
own state, separate from the disposable **platform stack** (ingress, cert-manager),
so the data tier can be applied/destroyed fully independently (methodology §11).
Scheduled backups (`pg_dump` / volume snapshots) are a belt-and-suspenders
fast-follow.

## Notes & scope boundaries

- **Single-node by default.** `ha = true` provisions the ADR-020 NATS topology —
  3 servers in a RAFT cluster, spread one per node with a hard
  `topologySpreadConstraint`. `nats_cluster_replicas` overrides the count for a
  topology the toggle cannot express (odd, at most 5). It also sets both databases
  to three replicated CloudNativePG instances — the relational store synchronously,
  the event store with `preferred` durability so a lost standby degrades it to
  asynchronous replication rather than stalling ingest. `postgres_instances` and
  `timescale_instances` override each independently.
- **HA is two levers, and this root owns only one of them.** The server count
  lives here; the per-stream replica factor lives in the services' config
  (`instance.config.infrastructure.nats.streamReplicas`), rendered by the
  DeviceChain Helm chart, which this root does not install. Raising only this half
  yields a 3-node broker whose every stream and KV bucket is still single-replica —
  a cluster that survives no node loss while looking like it would. The services
  clamp down to 1 against an unclustered broker rather than crashloop, and export
  `devicechain_*_jetstream_replicas_desired` / `_actual` / `_peers_current` so the
  disagreement alerts. `dcctl bootstrap --ha` sets both from one value; a direct
  tofu user must set the Helm value themselves. `nats_cluster_replicas` is
  published as an output for exactly that check.
- **TimescaleDB extension.** The Timescale image preloads the `timescaledb`
  library; the application creates the extension and hypertables on migrate.
- **Credentials.** Passwords default to `devicechain` for local dev. Override via
  `terraform.tfvars` (gitignored) or a pre-created Secret for any real deploy.
- **Pin versions.** Set `nats_chart_version` and `timescale_image` to tested
  versions for reproducible deploys (the defaults float to latest).
- **JetStream disk ceiling.** `nats_jetstream_max_file_store` is the hard aggregate
  JetStream file-store bound (ADR-023); it defaults to 90% of `nats_jetstream_storage`
  for filesystem headroom, **floored to a whole unit of that size's own magnitude** —
  a 16Gi PV yields a 14Gi ceiling, not 14.4Gi. Lowering it below a stream's current on-disk usage on an
  existing cluster causes immediate `DiscardOld` eviction of the overflow — a
  non-issue on a fresh bring-up, but size it before a running cluster fills. Both
  the volume and the ceiling are **per node**: an HA cluster provisions the volume
  on every server, each holding one replica of the same ~9.5Gi reservation, so
  neither value scales with `nats_cluster_replicas`.
- **Broker metrics.** `nats_prom_exporter` (default on) runs the
  prometheus-nats-exporter sidecar for broker-side cluster health. The PodMonitor
  that scrapes it is rendered by the DeviceChain Helm chart, not here — it is an
  Operator CRD, and this root installs NATS and the monitoring stack in parallel,
  so rendering one here would fail a fresh apply nondeterministically.
