---
sidebar_position: 5
title: Observability & Metrics
---

# Observability & Metrics

DeviceChain ships with observability built in, not bolted on: every service is
instrumented with **Prometheus metrics** and standard Kubernetes health probes, and
`dcctl install` deploys a complete **Prometheus + Grafana + Alertmanager** stack
on the cluster, for every instance on it — so a fresh install is watchable from its first minute,
with no separate monitoring project to assemble.

:::note Status
The monitoring stack (kube-prometheus-stack via `dcctl install`) and the
event-processing operations dashboard are implemented and validated end-to-end. A
command-delivery dashboard and its alert rules ship alongside them. Dashboards for
the remaining functional areas and OTLP distributed tracing are planned follow-ups.
:::

## What every service exposes

Each functional-area service instruments itself with Prometheus client metrics and
serves the two standard Kubernetes probes:

- **`/healthz`** — liveness: is the process alive?
- **`/readyz`** — readiness: is it ready to take traffic? A service that isn't
  ready is held out of rotation by its Kubernetes Service (see
  [Deployment & Operator](./kubernetes-operator.md)).

Because every pod speaks the same conventions, the monitoring stack scrapes the
whole instance uniformly — there is no per-service integration work.

## The monitoring stack

[`dcctl install`](./bootstrap.md#install) provisions monitoring as one of its embedded
OpenTofu modules, **on by default** (`--no-monitoring` leaves it out, and so does
`--compact`) — the same layer that provisions the relational database, cert-manager and
ingress also stands up
[kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
(Prometheus, Grafana, and Alertmanager). It is installed once per cluster, and every
instance bootstrapped on that cluster is watched by it:

- **Cross-namespace scrape** — Prometheus runs in its own namespace and scrapes
  each instance's services across namespaces, so one stack watches the whole
  deployment.
- **Dashboards ship with the platform** — Grafana boards live in the Helm chart
  (`deploy/helm/devicechain/dashboards/`) and are auto-imported by Grafana's
  dashboard sidecar. A new dashboard is a chart change, not a manual import.
- **One folder per instance** — each instance gets its own Grafana folder,
  `devicechain-<instance>`, holding its own copy of every board. Each copy is scoped to
  that instance and titled with its id; there is no instance picker to set. The files
  under `dashboards/` are templates the chart renders per instance, not boards to
  import by hand. The folder comes from a `grafana_folder: devicechain-<instance>`
  annotation on each dashboard ConfigMap, and only a sidecar configured to read it
  files boards by folder: the sidecar must run with `FOLDER_ANNOTATION=grafana_folder`
  and its dashboard provider must have `foldersFromFilesStructure: true`. The stack
  `dcctl install` deploys sets both. If you installed with `--no-monitoring` and point
  your own Grafana sidecar at these ConfigMaps without them, the annotation is ignored
  and every instance's boards land flat in one place — still separate boards, each
  still scoped to its own instance, just not in folders.
- **Instances on a cluster keep their boards apart** — once every instance on a cluster
  runs a chart with per-instance folders, removing or upgrading one leaves the others'
  boards untouched. Until then, instances still on an older chart share one board per
  dashboard outside any instance folder, and upgrading any instance removes that shared
  board file: an older instance's board is missing until Grafana's dashboard sidecar next
  rescans. A cluster whose monitoring stack predates this layout shows every instance's
  boards outside their folders until `dcctl install` is re-run.
- **Old board links retire** — the boards' former shared ids (`dc-event-processing-ops`,
  `dc-command-delivery-ops`) are not used by any upgraded instance, so bookmarks to them
  stop working once every instance is upgraded.
- **Destroying an instance leaves an empty folder** — its boards are removed, but its
  now-empty `devicechain-<instance>` folder stays in Grafana; delete it by hand.

## Signing in to Grafana

Grafana uses its own **admin login**. Signing in to Grafana through DeviceChain's
single sign-on is not currently available.

Metrics are instance-level and cross-tenant, so Grafana is an *operator* surface, not
something tenant users can reach. Tenants see their own data through the console and
dashboards, never through Grafana.

### Reaching Grafana

The stack `dcctl install` deploys publishes no ingress route for Grafana; its Service
is `ClusterIP`. Port-forward it and open `http://localhost:3000/`:

```bash
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
```

### The admin password

Sign in as `admin`. The password is minted by `dcctl install` and stored in Secret
`dc-grafana-admin` in the `monitoring` namespace, key `admin-password`:

```bash
kubectl -n monitoring get secret dc-grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d
```

The install report prints the same two commands. Grafana runs **once per cluster**, so
this is the *cluster's* login, shared by every instance on it — not one password per
instance. Rotating it locks out every instance's operators on that cluster, not just yours.

### Rotating it

Re-running `dcctl install` **keeps** the password: the Secret is read back and reused,
and a new one is minted only when the Secret is absent. Editing the Secret by hand does
not rotate it either — Grafana reads the password from the Secret as an environment
variable at start-up, a changed Secret restarts nothing, and Grafana keeps accepting the
password it started with while the Secret names one it has never seen. To rotate it on
purpose, do all three steps:

```bash
kubectl -n monitoring delete secret dc-grafana-admin
dcctl install <the flags the cluster was installed with>   # a missing Secret is minted afresh
kubectl -n monitoring rollout restart deployment/kube-prometheus-stack-grafana
```

:::caution
Do not skip the restart. After the first two steps the Secret holds a new password and
Grafana still accepts the old one, so the rotation looks done and is not — and the next
person who reads the Secret cannot log in. The restart is what applies it, and it works
because the deployed Grafana keeps no persistent database.
:::

## The event-processing operations board

The DETECT/REACT engine (see [Event Processing](../concepts/event-processing.md))
is the component an operator most needs to watch, and it ships with a dedicated
Grafana dashboard and alerts. The engine emits operator-facing metrics including
a **consumer-lag gauge** — how far detection has fallen behind the resolved-event
stream — and **rule firing counts**, so "is the alarm engine keeping up, and what
is it doing?" is answerable at a glance.

## Related

- **[Bootstrap an Instance](./bootstrap.md#install)** — `dcctl install`, the command
  that deploys the monitoring stack, and its flags.
- **[Deployment & Operator](./kubernetes-operator.md)** — how the chart renders
  per-service workloads with their health probes.
