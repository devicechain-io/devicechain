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
  that instance and titled with its id; there is no instance picker to set. Instances
  sharing a cluster never share a board, so removing or upgrading one leaves the others'
  boards untouched. An instance still on an older chart, or a cluster whose monitoring
  stack predates this layout, shows its boards outside any instance folder until
  both are upgraded (re-running `dcctl install` updates the cluster side).

## Signing in to Grafana

Grafana uses its own **admin login**. Signing in to Grafana through DeviceChain's
single sign-on is not currently available.

Metrics are instance-level and cross-tenant, so Grafana is an *operator* surface, not
something tenant users can reach. Tenants see their own data through the console and
dashboards, never through Grafana.

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
