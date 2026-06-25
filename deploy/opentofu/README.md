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

The ingress controller and cert-manager are the TLS/ingress *capability*; the
per-instance **Ingress resource + cert Issuer** that route to the app Services are
rendered by the Helm chart (it knows the enabled functional areas) — set
`ingress.enabled=true` there. Both are toggleable (`enable_ingress_nginx`,
`enable_cert_manager`) if the cluster already has them.

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
- **Not yet here (next slice):** the ADR-020 HA topology + the broker-enforced
  transport security (TLS + NATS auth-callout) it unblocks.
