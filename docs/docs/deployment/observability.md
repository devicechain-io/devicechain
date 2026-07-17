---
sidebar_position: 4
title: Observability & Metrics
---

# Observability & Metrics

DeviceChain ships with observability built in, not bolted on: every service is
instrumented with **Prometheus metrics** and standard Kubernetes health probes, and
`dcctl bootstrap` deploys a complete **Prometheus + Grafana + Alertmanager** stack
alongside the instance — so a fresh install is watchable from its first minute,
with no separate monitoring project to assemble.

:::note Status
The monitoring stack (kube-prometheus-stack via `dcctl bootstrap`), Grafana SSO
through the platform's own OAuth 2.1 authorization server, and the
event-processing operations dashboard are implemented and validated end-to-end.
Per-service dashboard breadth (a curated Grafana board for every functional area)
and OTLP distributed tracing are planned follow-ups.
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

[`dcctl bootstrap`](./bootstrap.md) provisions monitoring as one of its embedded
OpenTofu modules, **on by default** — the same layer that provisions NATS,
PostgreSQL, and ingress also stands up
[kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
(Prometheus, Grafana, and Alertmanager):

- **Cross-namespace scrape** — Prometheus runs in its own namespace and scrapes
  the instance's services across namespaces, so one stack watches the whole
  deployment.
- **Dashboards ship with the platform** — Grafana boards live in the Helm chart
  (`deploy/helm/devicechain/dashboards/`) and are auto-imported by Grafana's
  dashboard sidecar. A new dashboard is a chart change, not a manual import.
- **A break-glass admin login** — Grafana keeps a native admin credential
  available alongside SSO, so an operator is never locked out of metrics by an
  auth outage.

## Grafana SSO

Grafana can log operators in through DeviceChain's own identity system instead of
a separate Grafana account. Enable it at bootstrap:

```bash
dcctl bootstrap local my-instance --grafana-sso
```

This registers Grafana as a **confidential OAuth client** of `user-management`'s
OAuth 2.1 authorization server — the same server that secures
[AI access over MCP](../concepts/mcp.md) — and configures Grafana to send users
through it. Signing in to Grafana is signing in to DeviceChain.

Two things to know about the model:

- **Operator-tier only.** Metrics are instance-level and cross-tenant, so Grafana
  is an *operator* surface, not something tenant users can reach. Tenants see
  their own data through the console and dashboards, never through Grafana.
- **Linked from the console.** The superuser console shows a **Metrics** link
  that takes an operator straight to Grafana.

## The event-processing operations board

The DETECT/REACT engine (see [Event Processing](../concepts/event-processing.md))
is the component an operator most needs to watch, and it ships with a dedicated
Grafana dashboard and alerts. The engine emits operator-facing metrics including
a **consumer-lag gauge** — how far detection has fallen behind the resolved-event
stream — and **rule firing counts**, so "is the alarm engine keeping up, and what
is it doing?" is answerable at a glance.

## Related

- **[Bootstrap an Instance](./bootstrap.md)** — the command that deploys the
  monitoring stack, and its flags.
- **[Deployment & Operator](./kubernetes-operator.md)** — how the chart renders
  per-service workloads with their health probes.
- **[AI Access (MCP)](../concepts/mcp.md)** — the OAuth 2.1 authorization server
  that Grafana SSO rides on.
