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

- **`/healthz`** — liveness: can the process still do its job, or does it need a
  restart? It fails once the service's message-broker connection has closed for
  good, so Kubernetes restarts the pod.
- **`/readyz`** — readiness: is it ready to take traffic? A service that isn't
  ready is held out of rotation by its Kubernetes Service (see
  [Deployment & Operator](./kubernetes-operator.md)).

Because every pod speaks the same conventions, the monitoring stack scrapes the
whole instance uniformly — there is no per-service integration work.

## Logs {#logs}

Every service writes structured JSON logs to stderr, one object per line. Each line
carries the `instance` and `area` it came from, and `tenant` when the pod serves a
single tenant, so a log pipeline can filter on them without parsing messages.

### The log level

How much a service logs is set once for the whole instance, by
`infrastructure.logging.level` in the instance configuration. It accepts exactly one
of these values, in lowercase:

| Level | What you get |
| --- | --- |
| `trace` | Everything, including the most detailed diagnostics. |
| `debug` | Diagnostic lines, some of them written once per message on the ingest path. |
| `info` | **The default.** Startup, shutdown, configuration and notable events. |
| `warn` | Only warnings and errors. |
| `error` | Only errors. |

Anything else, including `INFO`, a number, or a value with a space in it, is refused: the
service does not start, and its log names the key and the accepted values. There is no
level that turns error logging off.

`debug` and `trace` are for diagnosing a problem, not for running. On the ingest path
they write a line for every device message, so at production rates they multiply the
volume your log pipeline has to carry.

Each service starts at `info` and switches to the configured level as soon as it has read
the instance configuration, early in startup. It logs one line saying
which level it switched to, written before the switch so that it appears even under
`warn` or `error`.

The level is part of the mounted configuration, not something a running service
reloads. Changing it changes the configuration checksum, and the pods restart onto the
new value.

**Who can change it depends on how the instance was installed:**

- **Installed with `dcctl bootstrap`:** the instance runs at `info`, and `dcctl` has no
  option to change the level yet. Do not edit the configuration Secret by hand to get
  around that: `dcctl` writes that Secret itself and replaces it on its next run, and an
  edit made outside `dcctl` does not restart the pods in any case.
- **Installed from the Helm chart directly:** set it with your other values, for
  example `--set instance.config.infrastructure.logging.level=debug`.
- **Chart install with `instance.existingSecret`:** add `logging.level` under
  `infrastructure` in the document you supply, and update
  `instance.existingSecretChecksum` to the new document's checksum. The checksum is what
  restarts the pods; without it the new level is not picked up.

:::note Debug output used to be on by default
Before the log level was configurable, every service logged at `debug` whether or not
anyone asked it to. If you are comparing logs from an instance upgraded across that
change, the per-message lines on the ingest path, the broker read and write
confirmations, and similar diagnostics are no longer there at the default level. Nothing
was lost: set `debug` to see them again.
:::

### What is never logged

A service's own configuration document is never written to the log, at any level. The
service logs a short hash of it instead (`config_sha256`, the first 16 hexadecimal
characters of its SHA-256). To check which configuration a pod is running, hash that
service's entry in the rendered configuration ConfigMap and compare the two.

SQL statement logging is a separate, per-service switch: `sqlDebug` in a service's
datastore configuration. The database layer writes it through its own logger, so it is
neither enabled nor suppressed by `infrastructure.logging.level`.

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

## Messages a consumer never read {#unread-loss}

Every JetStream stream has a ceiling. When a stream is full it discards its **oldest** messages to
make room, so ingest keeps running. A consumer that had not read a message yet when it was
discarded will never read it. The broker does not report this, so every service measures it for
each durable consumer it reads, and three alerts watch the result:

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `JetStreamStreamNearFull` | warning | A stream has been over 80% of its byte ceiling for 10 minutes. Nothing has been lost yet. It covers the streams of every service. | Look for a consumer that is falling behind. If the traffic has simply outgrown the stream, raise its ceiling. |
| `JetStreamDurableLostUnread` | critical | A consumer moved past messages that were removed before it read them. They were never processed. | If a tenant was being deleted at the time, this is expected: the deletion removed messages the consumer had not reached yet. Otherwise the stream was full while this consumer was behind. Either the ceiling is too small for the traffic, or the consumer is slower than its producer. |
| `JetStreamDurableStalledBehindStream` | critical | A consumer has been handed no messages for at least two minutes, and the stream has already discarded messages ahead of it. A consumer that is reading, however slowly, does not fire this one; its losses fire `JetStreamDurableLostUnread`. | The service is running, since it reports this, but its consumer is not reading. Look for message handling stuck on a dependency such as the database, or pods waiting to become ready. If that cannot be fixed quickly, raise the stream's ceiling so it stops discarding. |

The ceilings are `streamMaxBytes` (the high-volume streams), `streamMaxBytesCold` (the others) and
`streamMaxMsgs` under `instance.config.infrastructure.nats`. The JetStream volume is sized from
their sum, so raise the volume with them (see [Bootstrap an Instance](./bootstrap.md)).

The alerts read two series, which every service exports for each durable consumer it reads:

- **`devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`** counts the
  messages the consumer moved past without reading them. It is a lower bound: a redelivery, or a
  message deleted behind the consumer, makes it count less, never more.
- **`devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`** is how many
  messages have been discarded ahead of a consumer that was handed no messages since the previous
  sample (every 30 seconds). It is 0 while the consumer is reading, even when it is behind: those
  losses are the counter's. It drops back to 0 when the consumer reads again, and the counter above
  takes over.

Both exist at 0 from the moment the service creates the consumer's reader. Every replica of a service reports the same
consumer and counts the same loss, so combine them with `max`, not `sum`. A pod restart resets the
counter, so read it with `increase()` or `rate()`. Each pod measures from its own first sample, so a
loss the consumer moves past while every pod of the reading service is restarting at once can go
uncounted. A service with no running pods reports neither series, so neither alert can fire for
it; the near-full warning and your pod-health alerts cover that case.

## Messages held past their acknowledgement window {#held-past-ack-wait}

The broker gives a service a fixed window to acknowledge each message it hands out. A
message still unacknowledged when the window closes is handed out again, and the service
handles it as though it were new. For the two services whose work is a slow send to
somewhere outside the platform — alarm notifications and outbound connectors — that can mean
a second page or a second webhook call. Both read only as many messages as they have workers
free to start, so nothing waits in a queue while the window runs, and each send is cut off
with time to spare before the window closes. The alert below reports the cases that still
get through.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `ReaderHeldMessagePastAckWait` | warning | A handler held a message past its acknowledgement window, so it was redelivered. `stage=worker`: a send ran long, so the message may have been sent twice. `stage=buffer`: a message was dropped before it was handed out, and its redelivery was handled instead. | For `stage=worker`, look for a slow or unresponsive destination behind the service named by the `durable` label. For `stage=buffer`, the service is not keeping up with its stream. |

## Messages that ran out of delivery attempts {#max-delivery-records}

After five unacknowledged deliveries the broker stops handing a message out. It publishes a
notice the next time the consumer is pulled after the last delivery's acknowledgement window has
passed, so for a service that is down the notice waits until the service runs again. A platform stream, `max-deliveries`, captures those notices, and each service turns
its own into dead letters with reason `no-outcome` (read them with `dcctl dead-letters list`).
The stream is a work queue: a recorded notice is deleted, so on a healthy instance it is empty.
The counter `devicechain_<area>_max_delivery_records_total{stream,outcome}` says what was done
with each notice.

One consumer is an exception, and it is declared as one: the detection engine in
`event-processing` reads `resolved-events` from its own saved checkpoint. It acknowledges an
event only once a checkpoint covers it, and after a restart it reads the stream again from the
last checkpoint, so an event whose delivery attempts ran out has not been lost. When its
checkpoint cannot be saved (usually because its database is unreachable) for longer than the
broker keeps redelivering, every event in that window runs out of attempts. Those notices are
not turned into dead letters, which would report hundreds of losses that did not happen. They
are counted with `outcome="replay-covered"`, and the alert below reports them. The other services
that read `resolved-events` have no such checkpoint, and their notices are dead-lettered as usual.

| Alert | What it means | What to do |
| --- | --- | --- |
| `MaxDeliveryRecordsWaiting` | Notices of messages that ran out of delivery attempts have waited 15 minutes without being recorded. | Check that every service is running: one that is down records late. If a notice stays once everything is healthy, it names a consumer no running service reads any more (a reader removed by an upgrade); it will not be recorded, and can be deleted from the stream. |
| `ReplayCoveredDeliveriesExhausted` | A consumer that reads its stream from its own checkpoint ran out of delivery attempts in the last 15 minutes, because the checkpoint has not been saved for longer than the broker keeps redelivering. Nothing has been lost yet. | Fix whatever stops the service named by the `job` label from saving its checkpoint, usually its database connection. While the service runs, it saves what it has read once the checkpoint succeeds. If it restarts first, it reads the stream again from the last saved checkpoint, and events the stream has already discarded cannot be read again, so also watch `JetStreamStreamNearFull`. |

## Related

- **[Bootstrap an Instance](./bootstrap.md#install)** — `dcctl install`, the command
  that deploys the monitoring stack, and its flags.
- **[Deployment & Operator](./kubernetes-operator.md)** — how the chart renders
  per-service workloads with their health probes.
