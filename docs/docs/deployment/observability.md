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
  good, so Kubernetes restarts the pod. A loop that reads messages from a stream
  also ends the process when its reads keep failing for two minutes with no answer
  from the message broker in between, and Kubernetes then restarts it. Until then it
  retries with a growing pause of up to five seconds. A loop on a quiet stream checks
  every few seconds that the broker still answers, and a reconnect to the broker counts
  as an answer too, so two failures minutes apart count as two rather than one long
  one, and a long broker outage does not restart the service when the broker comes
  back. A broker that is simply unreachable does not count as failing reads at all:
  the reads wait for it, the service keeps reconnecting, and it carries on when the
  broker returns. See [A container that restarted](#container-restarts).
- **`/readyz`** — readiness: is it ready to take traffic? A service that isn't
  ready is held out of rotation by its Kubernetes Service (see
  [Deployment & Operator](./kubernetes-operator.md)).

Because every pod speaks the same conventions, the monitoring stack scrapes the
whole instance uniformly — there is no per-service integration work.

### Message results {#message-results}

Most services that consume a stream count each message they finish with in a counter named
`devicechain_<area>_<loop>_messages_total`, for example
`devicechain_eventmanagement_persist_messages_total`. Its `result` label says what became of the
message:

| Result | What became of the message |
| --- | --- |
| `ok` | It was handled. |
| `invalid` | It was discarded on arrival: it could not be read, carried no tenant, or was refused for what it holds. |
| `failed` | It was given up on and recorded, as a dead letter or on the `failed-events` stream. |
| `dropped` | It was given up on after its last delivery and deliberately not recorded, because what it would have updated can be rebuilt. |
| `retry` | Handling it failed for a reason that may clear, so it was left to be delivered again. |
| `erased` | Its tenant has been deleted and the service's database refused the write. It was settled on the delivery it arrived on and recorded nowhere. See [Tenant deletion](./tenant-deletion.md#database-writes). |

Every result is exported at zero from the moment the service starts, so an alert on the increase of
one of them sees the first message counted, not only the second. The one exception is a message
counted before Prometheus has scraped the pod at all: the series is first seen at 1, which is no
increase.

Each service can also serve Go runtime profiles on a listener of its own. This is off
by default; see [Profiling a service](#profiling).

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

### Lines written at every level {#audit-lines}

One kind of line ignores the level: the record the `mcp` service writes for every AI tool call. It carries no `level` field and is written under `warn` and `error` too, because turning verbosity down is not a decision to stop auditing. Select it by its `audit` field; its layout is described in [What is recorded](../concepts/mcp.md#audit).

### What is never logged

A service's own configuration document is never written to the log, at any level. The
service logs a short hash of it instead (`config_sha256`, the first 16 hexadecimal
characters of its SHA-256). To check which configuration a pod is running, hash that
service's entry in the rendered configuration ConfigMap and compare the two.

### Database messages

Database activity is logged through the same logger as everything else: JSON lines carrying
the service's `instance` and `area` fields, filtered by the configured
`infrastructure.logging.level`. A statement that fails is logged at `error` (`database
statement failed`, with the database's message in `error`), and one that takes longer than
200 ms at `warn` (`slow database statement`). Each line carries the statement (`sql`), the
rows it affected (`rows`), its duration in milliseconds (`elapsed_ms`) and the code that
issued it (`caller`). A query that finds no rows has not failed, so it is never logged as a
failure; it is logged only if it is slow, or when `sqlDebug` is on.

Logging every statement is a separate, per-service switch: `sqlDebug` in a service's
datastore configuration. Its lines are written at `info`, so a level of `warn` or `error`
hides them.

The `sql` field shows a statement's placeholders (`$1`, `$2`, …), never the values bound to
them, at every level and with `sqlDebug` on. The database's own error message is logged as
it is, and some of those quote the value they rejected (for example, `invalid input syntax
for type uuid: "…"`).

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

### Upgrading the stack

Re-running `dcctl install` upgrades the monitoring stack to the version the release pins.
Helm never upgrades a chart's custom resource definitions, so the stack runs the chart's own
CRD upgrade job first: a short-lived Job in the `monitoring` namespace applies the Prometheus
Operator CRDs server-side before the new operator starts, and there is nothing to apply by
hand. The Job pulls two images (`busybox` and `registry.k8s.io/kubectl`), so a cluster with no
route to those registries must mirror them first.

Going from the stack of earlier releases to this one crosses a Prometheus major version
(2 to 3) and a Grafana major version (11 to 13). Prometheus, Alertmanager and Grafana restart
once; the Grafana admin login, its `ClusterIP` Service and the per-instance dashboard folders
are unchanged. Prometheus keeps its stored metrics on a cluster with a persistent volume, and
starts empty on a local cluster, which keeps them on the node's disk only.

## Signing in to Grafana

Grafana uses its own **admin login**. Grafana is not integrated with DeviceChain
sign-in: neither a DeviceChain password nor a connected identity provider
(see [Single sign-on](../concepts/single-sign-on.md)) signs you in to it.

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
discarded will never read it. The two ingest streams whose loss would be device data refuse new
events before that happens to the consumer that must not lose them (see
[Backpressure on the ingest path](#ingest-backpressure)). Every other stream, and every other
consumer, is covered here. The broker does not report this loss, so every service measures it for
each durable consumer it reads, and these alerts watch the result:

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `JetStreamDurableUnreadNearFull` | warning | A consumer has not yet read more than 80% of what its stream can hold, for 5 minutes. Nothing has been lost yet. Messages it has already read do not count, so a stream that is full of processed history does not fire this. When the unread backlog reaches the ceiling, the stream discards the oldest messages and this consumer never processes the ones it had not reached. For `event-processing`'s detection consumer this measures the consumer, not the checkpoint detection replays from; the checkpoint is never behind what the consumer has acknowledged, so the number is at least what detection could lose. | Find out why the consumer is slow: its service's logs, its database, `JetStreamDurableFallingBehind`. If the traffic has outgrown the stream, raise its ceiling and the JetStream volume with it. The two consumers that hold ingest back are covered by `JetStreamUnreadBacklogNearFull` instead (see [Backpressure on the ingest path](#ingest-backpressure)). |
| `JetStreamCaptureBacklogWillOverflow` | critical | The capture stream, which holds events from MQTT devices until `event-sources` forwards them, will be full within ten minutes at the rate its unforwarded backlog grew over the last three, and that backlog is at least 10% of the ceiling. The broker has already acknowledged every one of those events to its device, so nothing can refuse them: once the stream is full it discards the oldest unforwarded ones (`JetStreamDurableLostUnread`) and the devices are never told. This fires minutes before `JetStreamDurableUnreadNearFull` would, because a fast backlog can go from empty to discarding in less than its 5 minutes. | Devices are publishing faster than `event-sources` forwards. If `event-sources` is not busy on CPU, its publish window is the limit: `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}` grows by close to a second per second. Add `event-sources` replicas, each of which brings its own window: `capture.publishWindow` already defaults to its maximum, 1024, so raise it only if it was lowered. If `JetStreamIngestBackpressureEngaged` is firing, the slow consumer is further downstream. To buy time, raise `streamMaxBytes` together with the JetStream volume, and `streamMaxMsgs` too if the stream fills by message count first (small payloads); the capture stream takes those ceilings in full, and `event-sources` applies them when it restarts. |
| `JetStreamCaptureBacklogGrowing` | warning | The early warning for `JetStreamCaptureBacklogWillOverflow`. The capture stream, which holds events from MQTT devices until `event-sources` forwards them, will be full within twenty minutes at the rate its unforwarded backlog grew over the last five, and that backlog is at least 10% of the ceiling. The broker has already acknowledged every one of those events to its device, so nothing can refuse them: once the stream is full it discards the oldest unforwarded ones (`JetStreamDurableLostUnread`) and the devices are never told. In a load test the critical alert fired two minutes before loss began; this one leaves time to add capacity first. | Devices are publishing faster than `event-sources` forwards. Add `event-sources` replicas now, each of which brings its own publish window, and check its CPU and `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}`. If `JetStreamIngestBackpressureEngaged` is firing, the slow consumer is further downstream. To buy time, raise `streamMaxBytes` together with the JetStream volume. |
| `JetStreamCaptureLeaderChanged` | warning | The capture stream changed leader within the last 15 minutes while events were arriving (`devicechain_*_jetstream_stream_leader_changes_total`, counted from the broker's stream information every 30 seconds, together with `devicechain_eventsources_total_inbound_messages` having moved in the same period). While the stream has no leader, which lasts several seconds, the broker's MQTT gateway still acknowledges device QoS 1 publishes into it and stores them nowhere, and nothing counts them: **MQTT events published during the change may have been acknowledged and lost**, and `JetStreamDurableLostUnread` does not fire for this loss. In a 12,000 events per second test one such step-down lost about five seconds of events. The default setting has a second way to lose acknowledged events, which this alert does not cover and which needs no leader change: if `event-sources` falls behind under sustained load until the capture stream reaches its bound, the stream discards its oldest unread messages, and `JetStreamDurableLostUnread` reports it (from `devicechain_<area>_jetstream_consumer_unread_skipped_total`). A change shorter than one sample that returns to the same server is not seen. A rolling restart of the broker, including the ones an upgrade makes, moves leaders too and can lose events the same way at any rate of traffic; because it fires on routine restarts, this is a warning. With `infrastructure.nats.mqttFailClosed` on, a QoS 1 event is held by the broker's message store and is not lost this way; only QoS 0 events are. See [MQTT, the platform broker](../reference/transport-matrix.md#mqtt--the-platform-broker) for the limitation. | Look in the broker logs for `Leader falling behind, stepping down`: that is a leader whose node is overloaded. Check the broker nodes' CPU and whether the capture stream's leader shares a node with the inbound-events leader (`nats stream cluster step-down --preferred` moves it). The lost events cannot be recovered; the time of the change bounds the window to look at in your devices' own records. Schedule planned broker restarts at low traffic. |
| `JetStreamReaderForeignDropped` | warning | The device capture reader dropped messages stored under a subject it does not read, within the last 15 minutes (`devicechain_*_jetstream_reader_foreign_dropped_total`). The broker hands that reader the whole capture stream, and the reader keeps only the subjects devices publish telemetry on. A device publish never lands there under another subject, so a dropped message was put into the stream some other way, most likely by a push consumer on another stream whose delivery subject is a device telemetry subject. Nothing was forwarded and no device event is missing. Find the push consumer (`nats consumer ls` on each stream) and the login that created it. event-sources logs the subject and sequence of the first message it drops. |
| `JetStreamStreamNearFull` | info | A stream that holds records for an operator, rather than messages a service processes, has been over 80% of its ceiling (bytes or messages) for 10 minutes. These are the records of messages that failed (`failed-decode`, `failed-events`, `connector-dispatch.dead`), `max-deliveries`, and `dead-letters` while `user-management`, which stores its letters, does not report reading it. Nothing processes what they hold, so once full they discard records nobody has looked at. Streams that services read are not covered: they keep a week of history that is normally near the ceiling, and the alerts in this table watch their consumers instead. | Find what is filling it: a decoder rejecting a device's payloads, events that fail to resolve or store, a connector whose destination refuses every send. On `dead-letters`, check that `user-management` is running. Raise the ceiling only if the records must be kept for longer. The default Alertmanager configuration of kube-prometheus-stack suppresses `info` alerts, so route this one explicitly if you want to receive it. |
| `JetStreamDurableLostUnread` | critical | A consumer moved past messages that were removed before it read them. They were never processed. | If a tenant was being deleted at the time, this is expected: the deletion removed messages the consumer had not reached yet. Otherwise the stream was full while this consumer was behind. Either the ceiling is too small for the traffic, or the consumer is slower than its producer. |
| `JetStreamDurableStalledBehindStream` | critical | A consumer has been handed no messages for at least two minutes, and the stream has already discarded messages ahead of it. A consumer that is reading, however slowly, does not fire this one; its losses fire `JetStreamDurableLostUnread`. | The service is running, since it reports this, but its consumer is not reading. Look for message handling stuck on a dependency such as the database, or pods waiting to become ready. If that cannot be fixed quickly, raise the stream's ceiling so it stops discarding. |

The ceilings are `streamMaxBytes` (the high-volume streams), `streamMaxBytesCold` (the others) and
`streamMaxMsgs` under `instance.config.infrastructure.nats`. The JetStream volume is sized from
their sum, so raise the volume with them (see [Bootstrap an Instance](./bootstrap.md)).

The alerts read these series. Every service exports the first three for each durable consumer it
reads:

- **`devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`** counts the
  messages the consumer moved past without reading them. It is a lower bound: a redelivery, or a
  message deleted behind the consumer, makes it count less, never more.
- **`devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`** is how many
  messages have been discarded ahead of a consumer that was handed no messages since the previous
  sample (every 30 seconds). It is 0 while the consumer is reading, even when it is behind: those
  losses are the counter's. It drops back to 0 when the consumer reads again, and the counter above
  takes over.
- **`devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}`** is the consumer's unread
  backlog (pending plus unacknowledged) divided by what the stream can hold, in messages or bytes,
  whichever limit is tighter. It is not exported for the two consumers that hold ingest back; the
  services writing to their streams export the same number as `jetstream_backpressure_unread_ratio`.
  It is absent until the first sample and while it cannot be measured.

Every service also exports **`devicechain_<area>_jetstream_stream_sink{stream}`** for each stream it
writes or reads: 1 for a stream that holds records for an operator, 0 for any other.
`JetStreamStreamNearFull` reads a stream's fill only where it is 1.

The first two exist at 0 from the moment the service creates the consumer's reader; the ratio
appears at the first sample. Every replica of a service reports the same
consumer and counts the same loss, so combine them with `max`, not `sum`. A pod restart resets the
counter, so read it with `increase()` or `rate()`. Each pod measures from its own first sample, so a
loss the consumer moves past while every pod of the reading service is restarting at once can go
uncounted. A service with no running pods reports none of these series, so none of the
per-consumer alerts can fire for it; your pod-health alerts cover that case.

## A consumer that stays behind {#consumer-backlog}

A consumer can lose nothing and still be far behind: whatever its service derives from the stream
is then that far out of date. Every service reports, for each durable consumer it reads, how many
messages are waiting for it, sampled every 30 seconds:

- **`devicechain_<area>_jetstream_consumer_pending_messages{stream, durable}`**: messages in the
  stream that the consumer has not been handed yet.
- **`devicechain_<area>_jetstream_consumer_ack_pending_messages{stream, durable}`**: messages
  handed to it and not yet acknowledged.

Both appear at the first sample after the service starts, not before, and they disappear again
while the consumer cannot be read. A missing series means "not measured", never "nothing waiting".
Every replica reports the same consumer, so combine them with `max`. A backlog that grows and
shrinks is normal during bursts. One that stays is what the alert watches. The alert's 15 minutes
survive one pod restarting while another replica keeps reporting. With a single replica, a restart
removes the series until the new pod's first sample and the 15 minutes start again, so a pod that
keeps restarting under a backlog may never raise it: watch its restart count as well.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `JetStreamDurableFallingBehind` | warning | A consumer has had more than 10000 messages waiting for it for 15 minutes. Whatever that service derives from the stream is that far behind: for `device-state`, a device's live state lags its stored events. `event-processing`'s detection consumer is not covered, because `DetectConsumerBacklogHigh` watches it and a takeover replays it by design. | Compare the consumer's rate with the stream's. If it is keeping pace but not catching up, give it capacity (for `device-state`, see [its settings](#live-state-projection) and its database). If it has stopped, `JetStreamDurableStalledBehindStream` and the service's logs say why. If the stream fills before it catches up, messages it has not reached will be discarded, except for the two consumers that hold ingest back (see [Backpressure on the ingest path](#ingest-backpressure)), where new events are refused first. |

## Backpressure on the ingest path {#ingest-backpressure}

Two streams carry events the platform must not lose before it has processed them:
`inbound-events` (events waiting to be resolved) and `resolved-events` (events waiting to be
stored). Each has one consumer whose unread events would be lost if the stream discarded them:
`device-management`'s on `inbound-events`, and `event-management`'s on `resolved-events`. On
these two streams the platform refuses new events rather than discard ones that consumer has not
read yet.

- When that consumer's **unread** backlog reaches **90%** of what the stream can hold, the
  services that write to the stream start refusing new events. They accept events again once the
  backlog is below **80%**. For this threshold only the unread backlog counts: the events already
  processed that the stream keeps for a week do not.
- A full stream discards its oldest events first, so the events already processed that it still
  holds ahead of the consumer's unread ones are what runs out before an unread event is lost. The
  services also refuse when that processed history would all be discarded within **30 seconds** at
  the rate the stream is discarding it. How long it lasts does not depend on how large the events
  are, so a burst of large events over a history of small ones is refused here while the unread
  backlog is still far below 90%. They accept events again once that history would last **a
  minute**, or the consumer has read everything. A full stream whose consumer keeps pace refuses
  nothing: what the stream discards is replaced by events the consumer has finished with. A full
  stream that holds less than about a minute of its own traffic is the exception: a consumer that
  falls behind for a few seconds can make it refuse, and it then accepts again only once that
  consumer has read everything. That is most likely with small stream ceilings, such as an
  installation bootstrapped with `--compact`, and large events arriving fast.
- An event the consumer received but has not acknowledged counts as unread, including one waiting
  to be delivered again after a failure. While such an event sits near the front of a full stream,
  the stream can refuse new events until it is acknowledged or given up on, rather than discard
  it.
- Each service measures the stream every 5 seconds, and also as soon as it has itself written
  about a thousandth of the stream's ceiling since its last measurement, at most every 100 ms.
- A service that cannot measure the backlog for 30 seconds treats the stream as full and refuses
  too.
- `device-management` stops reading `inbound-events` while `resolved-events` is refusing, and
  `event-sources` stops reading the MQTT capture stream while `inbound-events` is refusing. The
  backlog waits in the stream before, and no message uses up its delivery attempts.
- The refusal applies to **every tenant**, because the streams are shared. Each tenant is first
  held to its own ingest ceiling, counted in readings, before the shared gate is consulted. At the
  default of 1000 readings per second, a tenant's own devices cannot by themselves send more than
  a default high-availability installation was measured to store, on the cluster described in
  [what the default allows](../concepts/governance.md#ingest-default), with one `event-sources`
  replica. Tenants together still can, and so can a tenant whose tier raises its ceiling, any
  tenant on a smaller cluster, and a client posting over HTTP under many tenant names, since a
  name is metered before its credential is checked. The gate then refuses everyone.
- The broker still discards the oldest message when a stream is full, so an unread event can
  still be lost in three cases. The services writing to the stream can, between two of their
  measurements, use up more processed history than the stream was seen to discard in the
  30 seconds before. A stream can reach its ceiling for the first time holding almost no processed
  history, before any rate of discarding has been measured. And connect and disconnect transitions
  are admitted while the stream refuses (see below). The alerts in
  [Messages a consumer never read](#unread-loss) report it.
- Deleting a tenant removes its events from both streams. That frees room, so it does not by
  itself make the stream refuse, with one exception: a tenant whose events are the oldest a full
  stream holds, and few enough that the stream stays full without them. Removing those looks the
  same as the stream discarding its history quickly, so the stream can refuse until the consumer
  catches up. Nothing is lost.

What each transport does while the stream is refusing:

| Transport | What the device sees |
| --- | --- |
| HTTP | `503` with `Retry-After: 10`, after the request has passed its tenant's own ceiling (a tenant over it gets `429` instead). The event was not stored. Retry it. |
| MQTT (the platform broker) | Nothing. The broker acknowledged the message before the platform could refuse it. The message waits in the capture stream, which discards its oldest messages once it is full (`JetStreamDurableLostUnread`). With `infrastructure.nats.mqttFailClosed` on, a QoS 1 message waits in the broker's message store instead, which refuses new publishes once it is full: the device gets no PUBACK and sends the message again (see [fail-closed MQTT](#mqtt-fail-closed)). |
| External MQTT broker | Nothing. The message was already acknowledged. It is dropped and counted in `devicechain_eventsources_total_msg_backpressured{source}` (a message over its tenant's ceiling is counted in `devicechain_eventsources_total_msg_rate_limited` instead). |
| Sparkplug | Readings are dropped without retrying and counted in `devicechain_sparkplugingest_ingest_failures_total`. |
| LwM2M | Notifications are dropped and counted in `devicechain_lwm2mingest_notify_ingest_dropped_total`. The next notification replaces the lost one. |

Connect and disconnect transitions (from the broker, Sparkplug births and deaths, LwM2M
registrations) are still accepted while the stream is refusing, because nothing would send a
refused transition again. They land in the 10% of the stream kept above the refusal threshold.
The backpressure gate does not limit how many it admits (the tenant ceiling still applies to the
broker tap): devices decide how often they connect and disconnect, so a fleet that reconnects in a
loop can fill that margin, and then the broker discards the oldest
events, unread ones included, as it did before this release. Only these two consumers
hold ingest back. A slow `device-state` or `event-processing` does not. Their unread losses are
still reported by the alerts above. `event-processing`'s real position is its own checkpoint,
which the gate does not see. `ReplayCoveredDeliveriesExhausted` watches that.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `JetStreamUnreadBacklogNearFull` | warning | A gating consumer has been more than 80% of its stream behind for 5 minutes. At 90% the stream refuses new events for every tenant: HTTP devices get `503` with a `Retry-After`, MQTT devices' events wait in the capture stream, and Sparkplug and LwM2M readings, and events from an external MQTT broker, are dropped and counted. | Find out why the consumer is slow: its service's logs, its database, `JetStreamDurableFallingBehind`. If the traffic has outgrown the stream, raise its ceiling and the JetStream volume with it. |
| `JetStreamIngestBackpressureEngaged` | critical | A stream has been refusing new events for a minute, for every tenant: a consumer's unread backlog is near the ceiling, or the full stream is discarding the processed history ahead of it fast enough to run out within 30 seconds, or a service writing to it has not been able to measure that backlog for 30 seconds. | `JetStreamUnreadBacklogNearFull` names the consumer that is behind. The likeliest cause is that the consumer's service is not running: scaled to zero replicas or crash-looping. A deployed service that is not running still holds ingest back, on purpose. If `JetStreamUnreadBacklogNearFull` is quiet, it is the second case: `jetstream_backpressure_history_runway_seconds` shows it, and the writing service's log names the consumer and the rate. The refusal lifts on its own once that consumer's backlog is below 80% and, if the history was the cause, once that history would last a minute or the consumer has read everything. If neither names a consumer, the writing service may not be reaching the broker at all: see [A broker connection that died silently](#broker-connection-dead). |

The services that write to the two streams export these series:

- **`devicechain_<area>_jetstream_backpressure_unread_ratio{stream, durable}`**: the consumer's
  unread backlog (pending plus unacknowledged) divided by what the stream can hold, in messages or
  bytes, whichever limit is tighter. It is absent while it cannot be measured. Combine the pods
  with `max`.
- **`devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`**: how long
  the processed events still ahead of the consumer's unread ones would last at the rate the full
  stream has recently been discarding them. `+Inf` while the stream is not at its ceiling, is not
  discarding any, or the consumer has read everything. The stream starts refusing below 30 and
  accepts again at 60 or more. It is absent while it cannot be measured. Combine the pods with
  `min`.
- **`devicechain_<area>_jetstream_backpressure_engaged{stream}`**: 1 while the service is refusing,
  including while it cannot measure the backlog. It is read when Prometheus scrapes, so it cannot
  show 0 when the service is in fact refusing.
- **`devicechain_<area>_jetstream_publish_refused_total{stream}`**: messages the service did not
  publish because the stream was refusing.

## A broker connection that died silently {#broker-connection-dead}

A broker server whose machine stops or restarts abruptly, or that a network partition cuts off,
closes nothing: its clients' connections stay open and simply stop being answered. Every service
pings its broker server every 10 seconds and gives a connection up after three unanswered
intervals (30 seconds), or once a write to it has made no progress for 10 seconds, so a dead
connection is noticed within 40 seconds. A server that stops reading for 10 seconds while the
service is writing to it is given up the same way. The service then reconnects, usually within a
few seconds. While it is cut off it can neither publish nor receive: for `event-sources`, HTTP
ingest is refused (`503`) or times out, and the
[backpressure gate](#ingest-backpressure) reports itself engaged because it cannot measure the
backlog.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `BrokerConnectionDiedSilently` | warning | A service gave up a broker connection that had stopped answering, within the last 15 minutes, and reconnected. | Check whether a node was lost or restarted: `kubectl get nodes`, and your cloud provider's VM events, because Kubernetes records nothing for a machine that restarts within the node grace period (roughly 40 to 50 seconds). Run `dcctl ha verify`. Repeats for one service with no node event point at the network between that service and the broker. |

- **`devicechain_<area>_nats_connection_dead_total{detected_by}`**: connections this service gave
  up. `detected_by="ping"` means the server left its pings unanswered; `detected_by="write"` means
  a write made no progress for 10 seconds. Combine the pods with `sum`.
- **`devicechain_<area>_nats_connection_dead_last_timestamp_seconds`**: when the service last gave
  one up, in Unix seconds, or 0 if it has not since it started. The alert reads this one, so it
  still fires when Prometheus itself was on the lost node and missed the moment the counter moved.

The service's log says the same thing and names the server: `Disconnected from NATS: the server
left 2 pings sent 10s apart unanswered…` or `Disconnected from NATS: a write to the server made no
progress for 10s…`, then `Reconnected to NATS`. The second broker connection `event-sources`
holds, the one that observes device connections to the broker, is bounded the same way but is
not counted: its log says `A write to the NATS server made no progress` or `Lost the NATS
system-account connection`.

## An external MQTT source nobody reads {#external-mqtt-owner}

A source that reads from an MQTT broker you run is read by one `event-sources` pod at a time; the
others stand by and stay Ready, because they still serve everything else (see
[Transport matrix](../reference/transport-matrix.md#external-mqtt-broker)). So a source that
nobody is reading is not a failed pod: it is a pod that took the source over and could not reach
your broker, and is trying again every 15 seconds, or pods that cannot agree which of them reads
it because they cannot reach the platform's message broker.

- **`devicechain_eventsources_external_mqtt_owner{source}`**: 1 on the pod reading the source, 0
  on every other pod. Every pod exports it for every external source from the moment it starts,
  so combine the pods with `sum`: 1 is healthy.
- **`devicechain_eventsources_total_msg_not_owner{source}`**: messages a pod was still delivered
  after it lost the source, dropped rather than stored, because the pod that took over receives
  the same messages.
- **`devicechain_eventsources_total_msg_publish_failed{source}`**: events from the source that were
  read from your broker and decoded, but not stored because publishing them to the platform's
  event stream failed. That source does not retry, so each one is lost. The pod's log has a line
  for each, naming the source and the tenant. A rate above 0 means the platform's own broker was
  refusing or not answering; look there, not at your broker.

The source publishes through a window of events waiting for the platform broker's acknowledgement
at once, so it is not held to one acknowledgement at a time per decode worker, which on a
replicated broker (an acknowledgement takes about 11 ms) capped a source near 440 events a second
for the whole cluster, because one pod reads each source.

| Setting (`event-sources` config) | Default | What it does |
| --- | --- | --- |
| `externalMqtt.publishWindow` | `128` | How many events one external MQTT source keeps waiting for the platform broker's acknowledgement at once, from 1 to 1024. A value outside that range, or an explicit 0, stops the service at startup; it is never adjusted. Publishing can go no faster than this number divided by how long an acknowledgement takes. A fixed setting, read at startup. **It is also how much more a pod can lose.** This transport is at-most-once: your broker is told a message arrived as soon as the platform has queued it for decode, and a pod that stops suddenly loses what it had taken and not yet stored, which is up to 105 messages queued or being decoded, plus up to this many waiting for an acknowledgement. A pod that stops cleanly, or hands the source to another pod, waits (up to 8 seconds) for those to be stored first. Watch `devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="external-mqtt"}`: a rate near 1 means the window is what limits publishing. |

`ExternalMqttSourceNotReadByOnePod` (warning) fires when that sum has not been 1 for two minutes.
At 0, nothing is reading the source and what your broker delivers meanwhile is lost; the log of
the pod that last took the source over says why. An install with no external source exports no
such series, and the alert stays quiet.

`ExternalMqttSourceLosingEvents` (warning) fires when a source has been losing events to failed
publishes (`devicechain_eventsources_total_msg_publish_failed` rising) for ten minutes. Those events
were read from your broker and are gone, so look at the platform's message broker, not yours; the
event-sources log names the source and tenant of each. A single burst, such as what was in flight
when a stop gave up waiting, clears on its own and does not raise it.

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

The alert reads `devicechain_<area>_reader_held_past_ack_wait_total{durable, stage}`. The counter
exists at 0 from the moment the reader is created, so `increase()` sees a pod's first occurrence.
Each pod counts only the messages it held, so combine pods with `sum`, not `max`.

## Fetching ahead {#fetch-ahead}

By default a service pulls 256 messages from the broker and asks for the next 256 while it is still
working through the ones in hand, so the time each pull takes overlaps the work instead of being
added to it. This is on to keep the detection engine's backlog small, not to raise throughput:
with it on, the number of events waiting in front of detection stayed in the hundreds where it
peaked in the thousands without it, at no cost in CPU, and the rate the platform sustains did
not change. The `infrastructure.nats.fetch` settings change it. They are read when a service
starts, so a change is a restart, and they touch no consumer on the broker.

| Setting | Default | What it does |
| --- | --- | --- |
| `batch` | 256 | How many messages one pull asks for, from 1 to 256 (anything else is refused at startup). A larger batch spreads the pull's time over more messages. It is also the most one dropped connection can lose from a single pull, and the detection engine reads that range back from the stream before it carries on. |
| `ahead` | on | Ask for the next batch while the current one is still being handed out, so the pull overlaps the work. Set `ahead: false` to pull one batch at a time. Messages are still handled in the order they arrive. |
| `aheadHoldBudgetMillis` | 1000 | A batch asked for ahead waits while the one before it is handled, and its acknowledgement window is already running. If recent batches take longer than this to hand out, the service stops asking ahead and goes back to one pull at a time. At most a tenth of the acknowledgement window, or the service refuses to start. |

Services that read only as many messages as they have workers free (alarm notifications and
outbound connectors) ignore these settings. When a service stops, a batch it had asked for ahead
is not acknowledged, so the broker hands it to the next reader once its window closes.

## Keeping traffic on one broker server {#leader-affinity}

On a clustered broker each stream, and each consumer that reads it, is led by one of the NATS
servers. A service connected to a different server pays an extra hop between servers for
everything it does with them: each publish is forwarded to the stream's leader and its
acknowledgement is forwarded back, and each delivery and acknowledgement crosses between the
consumer's leader and the reader's server. On a three-server cluster under load these forwarded
messages were about a quarter of everything the broker handled, and the broker's work grows with
the number of messages, not their size. The two `infrastructure.nats.affinity` settings remove
them. `publishToLeader` is on by default and `consumerLeaders` is off; both are read when a
service starts. Neither changes what is stored, in what order, or what is acknowledged.

| Setting | Default | What it does |
| --- | --- | --- |
| `consumerLeaders` | off | When a consumer's leader has been on a different server from its reader for three samples in a row (about a minute and a half), the service asks the broker to move the leader to the reader's server. It does not ask while the broker is unhealthy: while a stream or consumer has no leader or a replica that is behind, or within two minutes of the service reconnecting. It moves one consumer at most once a minute, and it leaves the leader where it is when the service's replicas are spread across servers with no majority on one. One replica asks, and the others do not. |
| `publishToLeader` | on | Services that keep many publishes in flight (the resolved events `device-management` publishes, and the device events `event-sources` forwards) open a second connection to the server that leads the stream. When the broker elects a new leader they move to it, after the publishes already in flight have been acknowledged. If that connection cannot be opened, or is lost, they publish through their usual connection, as they do with this off. |

Set `publishToLeader: false` to turn the publishing half off, or `consumerLeaders: true` to turn
the other half on. On a three-server cluster under load, publishing to the leader cut the broker
messages handled per event from about 11 to 9 or 10, cut the median time `device-management`
waits for the broker to acknowledge a resolved event by a third to nearly a half, and raised
the most one `device-management` replica resolved by about 6%. Through a forced leader election, a rolling restart and a scale-up it kept
publishing and followed the new leader. Moving a consumer's leader briefly pauses that consumer's
deliveries, and on the same cluster it worked as described but resolved no more events, so
`consumerLeaders` stays off. On a single-server broker neither has anything to do.

- **`devicechain_<area>_jetstream_consumer_leader_local{stream, durable}`**: 1 while the
  consumer's leader is the server this service is connected to, 0 while it is another server.
  Absent while the consumer cannot be read. When several replicas read one consumer from
  different servers, the ones in the minority read 0, and that is expected.
- **`devicechain_<area>_jetstream_publish_leader_local{suffix, writer}`**: 1 while every such
  writer of that name on the stream publishes on a connection to its leader, 0 while one
  publishes through another server. `writer` is the same name the publish-window metrics carry.
- **`devicechain_<area>_jetstream_consumer_leader_moves_total{stream, durable, result}`**: moves
  this service asked the broker for. `result="moved"` is a move made; `result="failed"` is one
  the broker refused or did not answer, which changed nothing.

The two gauges are exported with the settings off too, so turning a setting off shows what it
was doing.

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
| `ReplayCoveredDeliveriesExhausted` | A consumer that reads its stream from its own checkpoint ran out of delivery attempts in the last 15 minutes, because the checkpoint has not been saved for longer than the broker keeps redelivering. Nothing has been lost yet. | Fix whatever stops the service named by the `job` label from saving its checkpoint, usually its database connection. While the service runs, it saves what it has read once the checkpoint succeeds. If it restarts first, it reads the stream again from the last saved checkpoint, and events the stream has already discarded cannot be read again, so also watch `JetStreamDurableUnreadNearFull`. |

## A container that restarted {#container-restarts}

A DeviceChain service ends itself, with a non-zero exit status, when it can no longer do its
job: when one of its loops' reads from the message broker have kept failing for two minutes
with no answer in between, or when one of its parts reports that the process cannot continue.
Its liveness probe also fails, so Kubernetes restarts it, when its broker connection has
closed for good. The pod is usually ready again within seconds. Messages the container had
read from the broker but not acknowledged are delivered again, so a restart does not drop
them. Requests a device or client had in flight to the restarted container fail and must be
retried, and each restart lengthens whatever outage caused it.

| Alert | What it means | What to do |
| --- | --- | --- |
| `InstanceContainerRestarted` (warning) | Kubernetes restarted the container named by the `pod` and `container` labels in the last 15 minutes. It fires once per container and clears 15 minutes after that container's last restart. | Read the log of the container that exited: `kubectl logs --previous -n <namespace> <pod>`. The last error line says why, usually after "A component has declared this process unfit to continue". A read loop that gave up names its stream and its last broker error. If several services restarted together, look at the broker first: check that its servers are running and run `dcctl ha verify`. If the restarted container is a broker server (`dc-nats-*`), read its previous log too; its streams take a few minutes to be current again. If the container's last state is `OOMKilled`, raise its memory limit. Restarts in the first minutes of an install or an upgrade are expected. |

The alert reads `kube_pod_container_status_restarts_total` from kube-state-metrics, which the
monitoring stack installs. If you run your own Prometheus without kube-state-metrics, it never
fires. A pod that is replaced rather than restarted, for example during an upgrade or after
`kubectl delete pod`, starts a new count and does not fire it. A pod that keeps restarting is
also reported by the monitoring stack's own `KubePodCrashLooping`, after 15 minutes.

The alert also reports a restart that happened while kube-state-metrics itself was being moved,
for example after its node was lost, as long as kube-state-metrics is back within 15 minutes.
After such an outage it clears 15 minutes after kube-state-metrics last reported before the
outage. A restart during a longer kube-state-metrics outage is not reported.

## Tenants metered at the platform default {#tenant-ceilings}

Every service that enforces a per-tenant ceiling reads each tenant's ceiling from
user-management. Until it has an answer it meters the tenant at the platform default, and the
HTTP ingest endpoint gives tenant names it cannot confirm a bounded set of allowances.
[Governance](../concepts/governance.md#unresolved-ceilings) explains both.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `TenantsMeteredAtPlatformDefault` | warning | For 15 minutes, the service named by the `job` label has kept metering tenants at its platform default because user-management was unreachable or failing. A tenant whose ceiling is above the default is shed early, and one whose ceiling is below it is admitted past its ceiling. | Check that user-management is running and that the service can reach it. |
| `RateLimiterOverflowInUse` | warning | For 10 minutes, HTTP ingest has been admitting requests for tenant names it could not confirm through the one allowance they all share. Many unconfirmed names are arriving, which usually means requests naming invented tenants. | Look at who is sending HTTP ingest requests. See [tenant names that cannot be confirmed](../concepts/governance.md#unconfirmed-tenants). |
| `ReactShedLettersOverBudget` | warning | For 10 minutes, the detection engine has shed outbound actions faster than it records them one by one, so the excess is summarised in one dead letter per tenant per minute. A tenant is well over its outbound ceiling. | Find the tenant in `dcctl dead-letters` (reason `shed`) and check its rules, or raise its outbound ceiling if the traffic is legitimate. See [outbound governance](../concepts/outbound-connectors.md#governance). |
| `RateMeteringClockFallback` | warning | For an hour, the service named by the `job` label has metered outbound actions on broker or arrival time because they carried no trigger time, so a catch-up after a restart can be shed as a flood again. A trigger time later than the broker time of the message carrying it is also metered on that broker time, but it is counted as source `capped` and does not fire this alert: it means the pod and broker clocks disagree, not that the time is missing. | Check that event-processing and outbound-connectors run the same release. |
| `ConnectorDispatchRateLimited` | warning | For 15 minutes, outbound-connectors has shed dispatches for being over their tenant's outbound rate. The detection engine meters the same ceiling on the same timeline and sheds over-quota actions before dispatching them, so these were admitted at one end and refused at the other. A tenant over its ceiling does not fire this; it fires `ReactConnectorEgressShedding` in the detection engine. | Check that the platform default outbound rate (`outboundCallsPerSecond` and `outboundBurst`) is the same for event-processing and outbound-connectors, and whether `TenantsMeteredAtPlatformDefault` is firing for either. Sends that fail and are retried are also metered again at the connectors end, so look for a failing destination too. That also means a single tenant whose destination keeps failing while it sends near its quota can raise this alert on its own, which is why it is a warning and not critical. The shed dispatches are dead letters with reason `rate_limited`. |

The series behind these alerts:

- `TenantsMeteredAtPlatformDefault` reads `devicechain_<area>_governance_unresolved_admissions_total{dimension, cause}`
  with `cause="unreachable"`.
- `RateLimiterOverflowInUse` reads `devicechain_eventsources_ratelimit_overflow_admissions_total`.
- `ReactShedLettersOverBudget` reads
  `devicechain_eventprocessing_react_connector_shed_unlettered_total{action}`.
- `RateMeteringClockFallback` reads `devicechain_<area>_rate_clock_fallback_total{source}`, where
  `source` is `append`, `capped` or `now`. The alert ignores `capped`.
- `ConnectorDispatchRateLimited` reads the `rate_limited` outcome of
  `devicechain_outboundconnectors_connector_dispatch_total`.

Only the `rate_limited` outcome of `devicechain_outboundconnectors_connector_dispatch_total` is
alerted on. Its other failure outcomes are one tenant's own configuration, such as a webhook that
fails or a destination the platform refuses to reach. Those dispatches are already dead-lettered
and listed by `dcctl dead-letters`, and they do not page the operator.

## Event resolution {#event-resolution}

`device-management` resolves every inbound event before anything stores or evaluates it. A
resolver authenticates the event's credential, which is one read from the relational database,
then looks up the device's profile, its relationships and its group scope in the message
broker's key-value store, and hands the resolved event on to be published. Those three lookups
are made at the same time. Whatever the key-value store cannot answer is read from the database
one lookup at a time, so a resolver still holds at most one database connection. A resolver
spends most of each event waiting for replies rather than using CPU. Several resolvers
work at once. Events that arrive while all of them are busy wait in front of them, in order: up
to 100 in the pod's hand-off queue and, behind them, the batches each inbound reader has fetched
from the stream (one reader unless `resolution.inboundReaders` says otherwise): up to 256
in the batch being handed out and, with [fetching ahead](#fetch-ahead) on, up to 256 more in the
batch asked for ahead.
The rest wait in the stream.

| Metric | What it tells you |
| --- | --- |
| `devicechain_devicemanagement_resolve_workers` | How many resolvers the pod runs. |
| `devicechain_devicemanagement_resolve_inflight` | How many of them are busy. When it stays at `resolve_workers`, events are arriving faster than the pod resolves them, and they queue in front of it. The `device-management` inbound consumer's pending count then grows (see [A consumer that stays behind](#consumer-backlog)). |
| `devicechain_devicemanagement_resolve_messages_total` | Events resolved, by result. While the resolvers are all busy, its rate is how many events the pod can resolve per second. |
| `devicechain_devicemanagement_resolve_not_gateway_child_total` | Events refused because a gateway published for a device that is not assigned to it. See [the metrics reference](./metrics-reference.md) and [gateway identity](./edge-services.md#gateway-identity). |

`resolve_duration_seconds` includes the time a resolver waits to hand its result on. Its lowest
bucket is 5 ms, so a quantile below that is an estimate, not a measurement.

### Tuning it

| Setting (`device-management` config) | Default | What it does |
| --- | --- | --- |
| `resolution.workers` | `10` | Resolvers running at once. Each holds one database connection while it reads an event's credential from the database, which it does whenever the credential was not verified on that replica within `inMemoryCache.ttlSeconds` (see [Caches that stop answering](#kv-caches)). So it must be below the service's connection pool (`rdbConfiguration.maxOpenConnections`, 20 unless set), which it shares with the GraphQL API, the MQTT connect checks and the consumer that applies alarm raises and resolves. More than half the pool is allowed, and logged at startup. A resolver's lookups in the key-value store run at the same time, but its database reads still run one at a time, so it never holds more than one connection. |
| `resolution.publishWindow` | `512` | How many resolved (and failed) events a pod keeps waiting for the broker's acknowledgement at once, from 1 to 1024. A value outside that range stops the service at startup; it is never adjusted. Publishing can go no faster than this number divided by how long an acknowledgement takes, and on a replicated broker an acknowledgement takes about 8 to 11 ms, so 128 held a pod near 11,000 events a second and 512 does not. Raise it when `devicechain_<area>_jetstream_publish_window_full_seconds_total` grows by close to a second per second. A deeper window holds more source events unacknowledged in the pod: if the pod dies they are redelivered after the consumer's acknowledgement wait and published again under the same message id, which the stream recognises (see `jetstream_publish_duplicates_total`). Each event in the window is held in memory, with its source, until it is acknowledged. A fixed setting, read at startup. |
| `resolution.inboundReaders` | `1` | How many pulls a pod keeps in flight at once on the inbound events consumer, from 1 to 4. A value outside that range stops the service at startup; it is never adjusted. Each reader asks the broker for its next batch while the one in hand is resolved, so one reader delivers at most a batch per round trip to the broker: on a busy three-node cluster one reader held a pod near 10,000 events a second while its resolvers were mostly idle. The readers share one consumer, so each event goes to exactly one of them, and they share the pod's resolvers, caches and publishing, so a reader costs far less than a replica. Each reader holds up to two batches unacknowledged (see above), which a restart leaves for the broker to redeliver after the acknowledgement wait. With more than one reader, a backlog is handed out a batch per reader at once, so while the consumer catches up, events up to a batch or so apart can reach the resolved stream in the opposite order, as they can with several replicas. On a slow fleet a batch can span more time than detection's allowed lateness, and detection rules that compare a reading with the previous one, or that count readings in the order they arrive, see the reorder. Raise it only when the resolvers are mostly idle and the `device-management` inbound consumer still falls behind: below about 10,000 events a second per pod the pull is not what limits it, and when node CPU is the limit, another reader does not help. A fixed setting, read at startup. |
| `inMemoryCache.ttlSeconds` | `300` | How long a replica keeps a value in its own memory, from 1 to 3600 seconds. A change drops the value on every replica at once, so this only bounds an eviction message that was lost. The key-value cache's own time to live caps it (see below). See [Caches that stop answering](#kv-caches). |
| `deviceCacheTtlSeconds`, `relationshipCacheTtlSeconds`, `metricDefCacheTtlSeconds`, `membershipCacheTtlSeconds` | `300` | The time to live of each key-value cache in the broker, from 1 to 3600 seconds. |
| `inMemoryCache.perDeviceCacheEntries` | `131072` | The most entries each replica keeps in memory of each of the three caches kept per device: a device by its token, its tracked relationships, its group memberships. See [Caches that stop answering](#kv-caches). |
| `inMemoryCache.perDeviceCacheMiB` | `24` | The most memory, in MiB, each of those three caches takes in each replica. Raise the service's memory limit with it. See [Caches that stop answering](#kv-caches). |
| `inMemoryCache.credentialCacheMiB` | `32` | The most memory, in MiB, the device credential cache takes in each replica, from 1 to 256. About 29,000 credentialed devices fit in the default; 60,000 need about 70. Raise the service's memory limit with it. See [Caches that stop answering](#kv-caches). |

Raise it when `resolve_inflight` stays at `resolve_workers` while the pod has CPU to spare. If the
pod is at its CPU limit instead, more resolvers do not help: give it more CPU (see
[Service sizing](./bootstrap.md#service-sizing)). Measured in-process
against a three-server broker, with every lookup taking 750 µs and made one after another, 5
resolvers resolved about 1,500 events a second and 10 about 2,900. Resolvers finish events out of arrival order, by a fraction of a
second; detection applies them in the order they reach the resolved stream. An out-of-range value
stops the service from starting, and the error names the setting. The service logs the value it is
using when it starts.

### Ingest publish window {#ingest-publish-window}

`event-sources` publishes each decoded event to the broker and keeps a number of those publishes waiting for the broker's acknowledgement at once, the same way `device-management` does.

| Setting (`event-sources` config) | Default | What it does |
| --- | --- | --- |
| `capture.publishWindow` | `1024` | How many events a pod keeps waiting for the broker's acknowledgement at once, from 1 to 1024. A value outside that range, including 0, stops the service at startup; it is never adjusted. Ingest can go no faster than this number divided by how long an acknowledgement takes. On an idle replicated broker an acknowledgement takes about 8 to 11 ms, but on a loaded one it was measured at about 70 ms, where 512 held a pod near 7,100 events a second while MQTT devices published faster and the capture stream filled. Beyond 1024, add replicas. Raise it when `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}` grows by close to a second per second. Each event in the window is held in memory, with the device message it came from, until it is acknowledged, so memory grows by the window times the size of one event and one source message: a few MiB at 1024 for ordinary payloads. If the pod dies those source messages are redelivered after the consumer's acknowledgement wait and published again under the same message id, which the stream recognises (see `jetstream_publish_duplicates_total`). A fixed setting, read at startup. |
| `brokerPresence.publishWindow` | `128` | How many presence transitions a pod keeps waiting for the broker's acknowledgement at once, from 1 to 1024. A value outside that range, including 0, stops the service at startup; it is never adjusted. The broker-presence reader used to wait for each acknowledgement before taking the next connect or disconnect, so it handled about 88 transitions a second per subject (about 11 ms each on a replicated broker) and a reconnect of 10,000 devices took about two minutes on one replica; with 128 it handles roughly 11,000 a second. A reconnect of N devices takes about N times the acknowledgement time divided by this number, spread over the replicas: about a second for 10,000 devices at 128. Watch `devicechain_eventsources_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="presence"}`: a rate near 1 means this window is what limits the tap. The broker does not acknowledge or redeliver connection advisories, so this is also a loss bound: if the pod dies, up to this many transitions it had accepted but not yet had acknowledged are lost, together with any advisories still waiting to be read, and reconciliation recovers them at its next pass (see below). A transition that cannot be written is never dropped silently: it is logged and counted in `devicechain_eventsources_presence_events_failed_total`, and a device whose connect failed does not wake its held commands. A fixed setting, read at startup. |

A transition lost this way is repaired by the next reconciliation pass, which compares the broker's live connections with the stored state. The pass runs every `brokerPresence.reconcileSeconds` (300 by default) on every replica, and once at startup. So in the worst case, a device whose connect was lost reads **offline while it is connected** for up to one interval (five minutes by default) plus the time the pass takes, and a device whose disconnect was lost reads online for as long. A replacement pod's first pass runs as soon as it starts. 128 is the default rather than the ingest writers' 512 because presence arrives in bursts as large as the fleet, not as a steady stream: 128 already drains a 10,000-device reconnect in about a second, and a larger window would add to what a pod that dies mid-burst leaves for reconciliation without making a reconnect noticeably faster.

`BrokerPresenceLosingTransitions` (warning) fires when presence transitions have been failing to reach the event stream (`devicechain_eventsources_presence_events_failed_total` rising) for ten minutes. Each one is lost until the next reconciliation pass, so devices can read offline while connected in the meantime. Look at the platform's message broker, and at the event-sources log for `Failed to emit a broker presence transition`, which names each device. A transition that arrives while the tap is stopping is not counted, so a rolling restart does not raise it, and a single burst clears on its own.

`MqttGatewayStreamUnbounded` (warning) fires when a stream of the broker's MQTT gateway (`$MQTT_msgs`, `$MQTT_out` or `$MQTT_qos2in`) has had no size limit for ten minutes and `event-sources` could not set one (`devicechain_eventsources_mqtt_gateway_stream_unbounded` at 1). The broker creates these streams without a limit when the first MQTT device connects, which on a new instance is long after `event-sources` started, so every pod checks them every 30 seconds and sets the limit as soon as they exist; `devicechain_eventsources_mqtt_gateway_stream_bounded_late_total` counts each time that happened. The alert means the limit could not be set, most often because the broker has no storage left to reserve it. An unbounded stream can fill the broker's storage and take the instance down, so free storage on the broker and look in the `event-sources` log for `Could not bound the MQTT gateway`.

### Fail-closed MQTT acknowledgement {#mqtt-fail-closed}

`MqttFailClosedDurableMissing` (critical) and `MqttFailClosedStoreNotAsDeclared` (critical) can fire only on an instance with `infrastructure.nats.mqttFailClosed` on. The setting is off by default. With it on, a QoS 1 PUBACK from the broker means the platform holds the message (see [quality of service](../guides/connecting-a-device.md#quality-of-service) for what a device sees). `event-sources` declares the broker's own MQTT message store (`$MQTT_msgs`) so that a full store refuses a publish instead of discarding one it has already acknowledged, keeps one consumer of its own on that store for each kind of device publish (events, command responses and device reports), and forwards what those consumers read. The copy of the same publish that reaches a platform stream directly is skipped, so each publish arrives once; a QoS 0 publish is never stored, and still reaches the platform through the capture stream. Every pod checks the store and those consumers every 30 seconds, creates again whatever is missing, and puts back whatever was changed. A store it has to create again takes no device publish until its consumers exist, so the devices are refused for that moment rather than acknowledged into a store nobody reads. During a rolling update that turns the setting on or off, the store stays as the pods with the old setting want it until the last of them has stopped, and changes after that; turning the setting off keeps the consumers until they have been read empty, so nothing they hold is lost. A publish made while the store changes hands can arrive twice, never not at all. The setting is refused at startup on a broker that also serves another instance, because the store is shared by every instance on the broker.

To turn it on, pass `--mqtt-fail-closed` to `dcctl bootstrap <provider> <instance>`, or to `dcctl upgrade <provider> <instance>` for an instance that is already running; `--mqtt-fail-closed=false` turns it off again. `dcctl` records the choice on the instance, so every later `dcctl upgrade` keeps it without the flag being repeated, and an instance told to stay off stays off. A value set by hand on the instance configuration does not last, because `dcctl` composes that document again on each upgrade, and `--values` does not accept this setting. The trade is the one above: while the broker's message store has no leader, a QoS 1 publish is refused instead of acknowledged, and the instance keeps up with less traffic, so size it to stay below that ceiling. The guarantee is stated for a replicated broker, so install the cluster with `--ha`; `dcctl` says so when you turn the setting on for a cluster installed without it. The setting needs the `event-sources` and `device-management` areas, which every profile includes, and `dcctl` refuses it if the area set lacks either. Before it changes anything, `dcctl upgrade` also checks that the stream ceilings the new configuration reserves fit the broker's JetStream file-store limit (an instance keeps the volume it was built with), and refuses with the numbers if they do not; `--mqtt-fail-closed=false` is the way back. The choice belongs to the instance's declaration, so `dcctl destroy` followed by `dcctl bootstrap` starts again from the default.

`MqttFailClosedDurableMissing` fires as soon as a check finds one of those consumers missing (`devicechain_eventsources_mqtt_failclosed_durable_missing_total` rising), and for as long as a pod reports one it could not create (`devicechain_eventsources_mqtt_failclosed_durable_present` at 0). While a consumer is missing, the broker acknowledges device publishes of its kind without keeping them, so each one is lost although the device was told it arrived. A rolling update does not remove a consumer, so it does not raise this alert. Find what deleted the consumer, and look in the event-sources log for `platform durable on the MQTT message store`. `MqttFailClosedStoreNotAsDeclared` fires when a check had to put a store setting back (`devicechain_eventsources_mqtt_failclosed_store_repaired_total`, by setting), or saw another instance on the broker (`devicechain_eventsources_mqtt_failclosed_foreign_instances` above 0). A changed setting is put back within 30 seconds, but while it lasted a full store could discard acknowledged messages, or refuse every publish. A size bound or replica count changed in the configuration is not a change this alert reports. Another instance on the broker shares the store and its size bound, so give each instance its own broker or turn the setting off.

Two things keep a full store from turning into a reconnect storm. While the store is above 80% of its size bound, `device-management` refuses every new MQTT device connection, before it checks any credential, until the store is below 60% again. Devices already connected keep publishing, and the platform reads the store down. A refused device is told that its connection is not authorized (MQTT CONNACK return code 5), because the broker's MQTT gateway cannot send code 3 (server unavailable) for this, so device firmware must keep retrying after code 5 rather than give up. And when the broker closes a device's connection because the store refused or did not acknowledge one of its publishes (the disconnect's reason is `Protocol Violation`), the device is not reported offline for 30 seconds. A device that reconnects within that time never reads offline, so command delivery does not hold its commands and offline rules do not fire. One that does not reconnect is reported offline when the 30 seconds end, at the time it disconnected. `devicechain_eventsources_presence_disconnects_deferred_total` counts the disconnects held back this way. A reconciliation pass that runs while a device is between a refusal and its reconnect can still report it offline until it reconnects.

`MqttStoreBackoffEngaged` (warning) fires when `device-management` has refused new MQTT device connections for two minutes because the store is nearly full (`devicechain_devicemanagement_mqtt_store_backoff_engaged` at 1). `devicechain_devicemanagement_mqtt_store_fill_ratio` is how full the store is, and `devicechain_devicemanagement_callout_refused_store_full_total` counts the connections refused. Nothing is lost while it fires, but devices are being kept off the platform, because the platform is reading device publishes more slowly than devices send them. `devicechain_eventsources_mqtt_failclosed_durable_pending{durable}` and `devicechain_eventsources_mqtt_failclosed_durable_ack_pending{durable}` show which reader is behind: each consumer's messages not yet handed to it, and those handed to it and not yet acknowledged, every 30 seconds. The events consumer reads the whole store and passes over what is not an event, so its pending count includes every kind; the command-response and device-report consumers count only their own. They are absent while a consumer cannot be read, or while its count cannot be trusted: the stream or the consumer has no leader, a replica is offline, or the consumer reports nothing pending while the store still holds messages for it, which a NATS server whose copy of the store is behind can report after a leader change or a restart. Look at `event-sources` and at what reads the inbound event stream, or, for command responses and device reports, at `command-delivery` and `device-management`.

Known limitations of the setting:

- The guarantee is stated for a replicated broker (`streamReplicas` 3). With one replica, a node crash can lose messages the broker had acknowledged and not yet written to disk.
- A consumer deleted on the broker by hand is created again within 30 seconds. Until then, and for a few seconds after while the pods look at the store again, publishes of its kind are acknowledged and not kept, which is why `MqttFailClosedDurableMissing` is critical.
- A message over its tenant's ingest ceiling is still acknowledged and dropped, as with the setting off.
- The edge agent's own MQTT broker is not covered. Its uplink to the platform is a QoS 1 publish like any other device's, and is covered.
- The capture stream still receives a copy of every QoS 1 publish, which its reader skips. If it fills under overload, `JetStreamDurableLostUnread` on it counts those skipped copies too, so it overstates what was lost: only QoS 0 publishes in it are lost, and the QoS 1 ones are held by the store. For the same reason `JetStreamCaptureLeaderChanged` loses nothing of QoS 1 with the setting on.
- The pods judge each other's lease by their own clocks. Clocks more than about 30 seconds apart can make them hand the store back and forth, which costs duplicates and loses nothing.
- A broker crash while the store holds a large backlog is not covered. In one load test at 9,000 QoS 1 events per second, before the store's reader was made faster, the store built up about 1.5 million unread messages, two of the three NATS servers ran out of memory and restarted, and about a third of the acknowledged events never reached the event store. Where they were lost has not been established. Until it has, do not count on the setting across broker crashes, and treat a growing store (`devicechain_eventsources_mqtt_failclosed_durable_pending`) as something to fix, not something the store will absorb.
- The store costs the broker more per publish than the capture stream does. A store that refuses rather than evicts has its limit checked on every publish, at a cost that grows with the number of devices publishing at the same moment, so PUBACKs are slower with the setting on: in one test at 6,000 events per second, 336 ms at the 99th percentile against 35 ms with it off.
- The back-off measures the store's size in bytes, and only refuses new connections: devices already connected keep publishing. A backlog well below 80% of the bound can still be large.
- A backlog in the store itself takes little broker memory, tens of MiB per server for a full store. Each NATS server's memory goes mostly on the duplicate-detection windows, and at a sustained 10,000 events per second it runs close to its soft memory limit with the setting on or off. A server that is short of CPU also queues work in memory, so if a server is ever OOM-killed, raise `nats_memory_limit`.

## Event persistence {#event-persistence}

`event-management` writes events in batches. Each writer takes the events already waiting for it,
up to a limit, and commits them in one transaction, writing each table once for each tenant in
the batch rather than once for each event. Connect and disconnect events are still written one at
a time within the batch. An event is acknowledged only after the transaction holding it has
committed. If an event in a batch is refused, nothing in that transaction is kept: the refused
event is written again on its own, and is retried or reported exactly as it would be without
batching. The rest of the batch is committed again without it. An event refused because its tenant
has been deleted is the exception: the refusal applies to every event of that tenant, so none of
them is written again. They are acknowledged at once, reported nowhere, and counted as `erased`
(see [Tenant deletion](./tenant-deletion.md#database-writes)). When the database refuses a row in
a statement that carries several events, it does not say which event the row came from, so the
batch is first written again in a new transaction, one event at a time, to find it. If the
transaction fails for a reason no single event caused, such as a lost database connection, every
event in it is written again on its own.

A writer that finds fewer events waiting than a full batch waits up to 10 milliseconds for more
before it commits (`persistence.lingerMillis`). A writer that finds events already waiting takes them
at once, so under a backlog the wait costs nothing. The wait saves commits only once every writer is
busy: an arriving event goes to a writer that is idle before one that is waiting to fill its batch,
so below roughly a few hundred events per second per replica each event is still committed alone,
up to 10 milliseconds later than it would be without the wait. Batching helps most under load: on a
replicated event store most of each commit is spent waiting for the standby, and a batch pays that
wait once.

| Metric | What it tells you |
| --- | --- |
| `devicechain_eventmanagement_persist_batch_size` | Events per committed transaction. Small batches mean the writers are keeping up. Batches that grow towards the limit mean the writers are busy. With 10 writers sharing one stream, batches seldom reach the limit even when storing is behind, so read this beside the consumer's backlog. |
| `devicechain_eventmanagement_persist_batch_fallbacks_total` | Batch transactions that did not commit, after which their events were written again. An occasional increase is one refused event, or two when the database refused a row in a statement that carried several events, because finding the event takes a second attempt. A steady rate means something is refusing writes repeatedly, such as a deleted tenant whose devices are still sending: each batch that holds its events costs one extra transaction, however many of them it holds. Those events show up in `persist_messages_total` under `erased`. |
| `devicechain_eventmanagement_persist_inflight` | Events writers hold, including those waiting for their batch to commit. |

`persist_duration_seconds` measures each event from when a writer takes it until its batch commits,
including up to `persistence.lingerMillis` spent waiting for the batch to fill, so under light load
its median sits about 10 milliseconds higher than with the wait off.

### Tuning it

| Setting (`event-management` config) | Default | What it does |
| --- | --- | --- |
| `persistence.writers` | `10` | Writers running in parallel. Each holds one database connection while it writes, so it must be below the service's connection pool (`tsdbConfiguration.maxOpenConnections`, 20 unless set). More than half the pool is allowed, and logged at startup, because reads then compete with the writers for the rest. |
| `persistence.maxBatch` | `64` | Most events committed in one transaction, from `1` to `64`. `1` turns batching off. |
| `persistence.lingerMillis` | `10` | How long a writer holding a batch that is not full waits for more events before committing it, up to `1000`. A writer that finds events waiting does not wait. `0` turns the wait off and commits what is already waiting. |

The defaults are the largest batch and half of the default connection pool. When the
`event-management` consumer's backlog keeps growing, storing is behind, whatever the batch size
(see [A consumer that stays behind](#consumer-backlog)). Adding writers is not a sure fix: they
split the same events into smaller batches, and every commit costs the event store CPU. On the
three-node cluster of the [measurements behind these defaults](./bootstrap.md#measured-throughput),
storing stopped rising near 6,000 events per second with batches averaging about 21 there and 28
to 30 above it, below the limit, while two of the three nodes, one of them the event store's, were
at 86 to 95% CPU; which of those held the rate was not isolated. In an earlier measurement, two replicas of 20 writers each cut batches to about 3
events, the event store's database used over 4 cores, and the whole pipeline stored less than one
replica of 10. Writers are per replica. Out-of-range values stop the service from starting, and the
error names the setting. The service logs the values it is using when it starts.

The 10-millisecond wait was measured in the release benchmark, on the six-node cloud cluster in
[Measured throughput](./bootstrap.md#measured-throughput). At 3,000 events per second,
transactions per stored event fell by 37% (from 0.126 to 0.080), the median time to store an event
rose by about 5 milliseconds, and the slowest 1% fell by 18%; that comparison also changed the
event store's keys and its archive compression, the services' CPU requests and their placement, `device-state`'s writers and detection's CPU limit, and the NATS servers' requests and memory limit, so it does not isolate the wait. In a five-minute
run at 6,800 events per second offered, the path that commits one event at a time took 4.8% of
`event-management`'s CPU, at 0.050 transactions per event, against 24% in the previous benchmark,
on the build before the wait and on different service nodes. If it costs more latency than it
saves, set `persistence.lingerMillis: 0`.

### Calculated-metric backfill {#calculated-backfill}

A tenant's [backfill](../user-guide/calculated-metrics.md#backfilling-history) fills calculated
metrics into stored history. It runs in `event-management`: one worker for the whole instance, taking
one job at a time in turn across tenants, at a bounded rate. With no job it costs one lock attempt
and two small queries every ten seconds. A backfill writes only measurement rows. It publishes
nothing to the broker and never reaches live state or detection.

| Setting (`event-management` config) | Default | What it does |
| --- | --- | --- |
| `calculatedBackfill.enabled` | unset | Three states. **Unset**: the worker runs when the connection pool has room for it beside the persistence writers (`persistence.writers` + 1 + 4 spare connections must fit in `tsdbConfiguration.maxOpenConnections`), and stays off, with a startup warning naming the settings, when it does not. **`true`**: you asked for the worker, so a pool that cannot hold it stops the service at startup. **`false`**: the worker does not run. Starting a backfill is then refused as unavailable, with the reason. That reason is read by tenant users, so it says only that the worker is switched off, or that the pool has no room for it; the settings and the pool sizes are in the startup warning, not in the reason. The console asks `calculatedMetricBackfillAvailability` when the page opens (no polling) and disables the control up front with that same reason, so changing this setting shows in the console the next time the page is opened; a job that was running stays running until the worker is turned on again or the job is cancelled. |
| `calculatedBackfill.rowsPerSecond` | `20000` | The most rows the worker reads plus writes in a second, across every job, from `1` to `200000`. This default is provisional and has not been measured on a loaded instance: start low on a busy instance and watch the event store's CPU. |

The worker holds one pooled connection for its lifetime. The span of one job (90 days), the batch
size and the one-active-job-per-tenant rule are fixed.

| Metric | What it tells you |
| --- | --- |
| `devicechain_eventmanagement_calc_backfill_rows_written_total` | Measurement rows inserted. |
| `devicechain_eventmanagement_calc_backfill_samples_total` | Readings evaluated. |
| `devicechain_eventmanagement_calc_backfill_skipped_total{reason}` | Values not computed, by the formula's skip reason, plus `ambiguous`. |
| `devicechain_eventmanagement_calc_backfill_windows_skipped_total{why}` | Stretches left untouched: `compressed`, or `retention` (within reach of the retention policy). |
| `devicechain_eventmanagement_calc_backfill_jobs_finished_total{status}` | Jobs that ended, by status. |
| `devicechain_eventmanagement_calc_backfill_oldest_active_job_age_seconds` | Age of the oldest queued or running job; `0` when none. A job that is stuck makes it grow without bound, so alert on it. |
| `devicechain_eventmanagement_calc_backfill_batch_seconds`, `..._rollup_refresh_seconds` | Time to process one batch, and to refresh the measurement rollup over one stretch. |
| `devicechain_eventmanagement_calc_backfill_rollup_refresh_conflicts_total` | Refreshes that met the rollup's own policy job and were retried. |

Each claim, slice end and final status is also logged once, with the tenant, job token, profile,
version, counts and any failure.

#### Stopping a backfill {#calculated-backfill-stop}

To stop **every** queued and running backfill, connect to the event store database and run:

```sql
UPDATE "event-management".calculated_backfills
   SET cancel_requested_at = now()
 WHERE status IN ('QUEUED', 'RUNNING');
```

Add `AND tenant_id = '<tenant>'` to stop one tenant's. The worker notices at its next commit, and
each job ends `CANCELLED` within one batch. Rows already written stay, and a queued job that no
worker has taken ends the same way once the worker next runs. To stop the worker itself, set
`calculatedBackfill.enabled: false` and restart `event-management`.

### Live device state {#live-state-projection}

`device-state` keeps each device's live state (connectivity, activity, latest readings and last
position) from the same stream of events, and merges them the same way: each writer takes the events
already waiting for it, up to a limit, and merges them in one transaction. Within that transaction,
the state of every device that already has one is written in one statement for each tenant in the
batch, rather than one statement per device; a device seen for the first time is created on its own.
An event is acknowledged only after that transaction commits. Several events for one device in the
same batch leave exactly what merging them one at a time would. A reading or a position replaces the
stored one only when it is strictly newer, so an older or equally old one never overwrites it; times
are compared as the database stores them, to the microsecond. If one tenant's part of a batch is
refused, that tenant's events are merged again one at a time, so only an event that is itself
refused is retried, and the other tenants' events are committed together without them. If the
transaction fails for a reason no tenant caused, such as a lost database connection, every event in
it is merged again on its own.

| Metric | What it tells you |
| --- | --- |
| `devicechain_devicestate_state_batch_size` | Events per committed transaction. Mostly `1` means the writers are keeping up. |
| `devicechain_devicestate_state_batch_fallbacks_total` | Batch transactions that did not commit, after which their events were merged again. A steady rate means one tenant's writes are being refused repeatedly, such as a deleted tenant whose devices are still sending. |
| `devicechain_devicestate_state_write_conflict_retries_total` | Single-event writes run again in place after PostgreSQL aborted them as a deadlock victim or serialization failure. A rate that keeps climbing means the inactivity sweep and the writers are colliding often; an event whose retries are all lost is left for redelivery and counted as `retry` in the message results. |
| `devicechain_devicestate_state_inflight` | Events writers hold, including those waiting for their batch to commit. |

`state_duration_seconds` measures each event from when a writer takes it until its batch commits.

| Setting (`device-state` config) | Default | What it does |
| --- | --- | --- |
| `projection.writers` | `10` | Writers running in parallel, each holding one database connection while it merges. Must be below `rdbConfiguration.maxOpenConnections` (20 unless set). |
| `projection.maxBatch` | `32` | Most events merged in one transaction, from `1` to `64`. `1` turns batching off. |
| `projection.lingerMillis` | `0` | How long a writer waits for more events before merging a batch that is not full, up to `1000`. `0` merges what is already waiting. |

Batching is what carries throughput on a replicated database. Merges for one device wait for each
other, so on a small fleet whose devices send in turn, more writers can mean more batches waiting on
the same devices. On a three-node cloud cluster with about 1,700 to 1,900 devices, 5 writers fell
behind from about 6,800 events per second. 10 writers were chosen in a run that also raised the
service's CPU request and `projection.maxBatch` to 64, so the share of each was not separated. The default
writer count is 10, half the default connection pool, and the default `projection.maxBatch` stays
32: batches in that run averaged about 15, so 32 did not bind on average. Raise it further only when the batches are full and the database has room to spare. The
service logs the values it is using when it starts. If the live state still falls behind, `JetStreamDurableFallingBehind` fires for the
`device-state` consumer (see [A consumer that stays behind](#consumer-backlog)).

An event `device-state` cannot apply by its last delivery attempt is acknowledged and discarded
rather than recorded anywhere, because the live state can be rebuilt from the event history. It is
counted as `dropped` in the [message results](#message-results).
`DeviceStateDroppingEvents` (warning) fires as soon as one is counted, and clears 15 minutes after
the last. Each dropped event left its device's connectivity, a reading or its last position out of
date until a newer event for that device arrives; the stored event history is unaffected. The usual
cause is the service's database refusing or failing writes for longer than the redelivery window.
Check the database, and the `device-state` log for `Giving up on`, which names the write and the
device.

## Replication {#replication}

A highly available instance needs two things: JetStream streams created with the replica count
the instance is configured for, and a NATS cluster large enough to hold them. Either can be wrong
while the configuration looks right. Every service therefore reports what the broker says about
each stream and KV bucket it uses every 30 seconds, and five alerts watch the result:

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `JetStreamNotReplicatedAsConfigured` | warning | For 15 minutes, a stream has had fewer replicas than the instance is configured for, as seen by the service named by the `job` label. The instance is not highly available for that stream. | Make sure the NATS cluster has enough servers for `instance.config.infrastructure.nats.streamReplicas`. A replica increase runs only when a service starts, so once the cluster is large enough, restart the affected Deployments. |
| `JetStreamReplicaPeersDegraded` | warning | For 20 minutes, a stream has had fewer current, online copies than it has replicas. Losing its leader may lose data or availability. The wait is long because newly added replicas copy the stream's data before they count as current, and because under sustained high event rates a copy can fall briefly behind; see below. | Check the health and placement of the NATS pods. Three replicas on pods that share one node do not survive the loss of that node. |
| `JetStreamLeaseBucketNotReplicated` | critical | On an instance configured for more than one replica, the bucket that decides which pod may write has fewer than three replicas. Losing its server blocks every standby from taking over. | As for `JetStreamNotReplicatedAsConfigured`. Do not rely on failover while it fires. |
| `JetStreamClusterUnused` | warning | The broker is clustered, but every stream is configured for one replica, so the instance runs several NATS servers and survives no server loss. | Set `streamReplicas` to match the cluster (`dcctl install --ha` sets both), or scale the NATS cluster down if one server is what you intended. |
| `JetStreamReplicationUnobserved` | warning | For 15 minutes, a running pod has been unable to read the replication state of a stream it could read earlier. The alerts above cannot judge that stream for that pod while this fires, so its replication is unknown, not healthy. | If it fires for every stream at once, the broker or JetStream is unavailable: check the NATS pods and the service's connection logs. If it is one stream, that stream has most likely lost its leader: inspect it with `nats stream info`. |

`JetStreamReplicationUnobserved` has limits worth knowing:

- It fires for **every stream on every pod** during a broker outage. That is deliberate: the
  alert is accurate, and no other alert in the chart reports the broker as unreachable. Group its
  notifications by alert name if that is too many.
- It watches each pod separately, so one replica that loses a stream fires even while another
  replica of the same service can still read it. A pod that is replaced, and a release that stops
  using a stream, do not fire it.
- It only sees a stream the pod has read at least once. A pod that has never been able to read a
  stream since it started does not fire it, and that includes a replacement pod started after the
  problem began. A container restarted in place (after a crash, an out-of-memory kill or a failed
  liveness probe) keeps its pod name, so what the earlier process read still counts and the alert
  still fires.
- It resolves six hours after the pod last read the stream, even if the stream is still
  unreadable. A resolved notification is not proof of recovery.
- A pod that is not running exports nothing, so it cannot fire. Pod health alerts cover that case.

At sustained high event rates, `JetStreamReplicaPeersDegraded` can go pending and clear again
without anything being wrong. A copy of one of the busiest streams falls a moment behind its
leader, counts as not current until it catches up, and then does. In two 10-minute tests at 6,000
events a second on a three-server broker, the busiest stream had a copy that was not current in
about one check in five, and never for more than a few seconds at a time, and the alert went
pending and cleared again repeatedly. It fires only when a stream has been short of current copies
at every check for 20 minutes, so lag of this kind clears it rather than firing it. To check
replication, run `dcctl ha verify`: it keeps re-checking for up to 90 seconds (`--settle`) before
it reports a failure, so a copy that is briefly behind does not fail it, and it passed when run
after each of those tests.

The alerts read these series, which every service that uses JetStream exports:

- **`devicechain_<area>_jetstream_replicas_desired{stream}`**, **`_replicas_actual{stream}`** and
  **`_peers_current{stream}`**: the configured replica count, the count the broker reports, and
  the copies that are current and online. A service that cannot read a stream removes all three
  for it rather than keep reporting its last values.
- **`devicechain_<area>_jetstream_broker_clustered`**: 1 when the connected broker is clustered, 0
  otherwise, including while the service is disconnected. It exists for as long as the pod runs.

No alert reads the next series, but it is the one to look at when publishing is slow:

- **`devicechain_<area>_jetstream_publish_duration_seconds{suffix, mode}`**: how long each publish
  to a JetStream stream took, from sending it to the service acting on the broker's
  acknowledgement or on its failure, by the stream it was sent to. `mode="sync"` is a publish the
  service waited on alone. `mode="pipelined"` is one of several in flight at once (the resolved
  events `device-management` publishes, and the device events `event-sources` forwards from what
  devices publish over MQTT to the platform broker), and its time also includes waiting for every earlier
  publish to be settled, and for the pause the service takes after a failed one, so the two modes
  are not directly comparable. A `mode="sync"` publish the broker never answered is counted at the
  5-second limit, so for that mode the count above the `le="5"` bucket is the publishes that ran
  into it. A `mode="pipelined"` publish can be counted above 5 seconds without having reached the
  limit.
- **`devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix, writer}`**: seconds a pipelined
  writer could not send because its whole window was waiting for acknowledgements. A rate near 1
  means the window, not the broker, limits publishing: raise `resolution.publishWindow` (`device-management`) or
  `capture.publishWindow` (`event-sources`). A rate
  near 0 means the window has room. `writer` says which writer it was, because several share one
  suffix: `resolution` (`device-management`), `capture`, `external-mqtt` and `presence`
  (`event-sources`) and `sparkplug` (`sparkplug-ingest`). Every external MQTT source counts under
  `external-mqtt`. A query that does not name `writer` can `sum by (suffix)` to read the total.
- **`devicechain_<area>_jetstream_publish_window_full_total{suffix, writer}`**: how many sends found the
  window full and had to wait. The seconds above divided by this is the average wait.
- **`devicechain_<area>_jetstream_publish_duplicates_total{suffix, writer}`**: publishes the broker
  acknowledged as copies of a message it already held under the same message id. The message was
  stored once. After a pod restart or a lost acknowledgement this rising is the stream absorbing
  the republish, not a fault.

## Caches that stop answering {#kv-caches}

`device-management` keeps the lookups it repeats for every event (a device by its token, the
device's tracked relationships, its type's published profile, and group memberships) in
key-value buckets on NATS. Each lookup waits at most half a second. A bucket that does not answer
in time, or that no server answers for, is skipped for five seconds: its lookups go straight to
the database, which holds the same data, and then one lookup is tried again. Only when that one
is answered does the bucket stop being skipped. The service logs one warning when a bucket is
first skipped (`A key-value cache stopped answering`) and one line when it answers again (`A
key-value cache is answering again`), with how long that took and how many lookups and writes
went to the database meanwhile. An error the bucket answers with, such as a full bucket refusing
a write, is counted but does not cause it to be skipped.

The usual cause is a NATS server that has dropped off the network without closing its
connections. Every replica of a bucket answers reads, so until the other servers notice the
silence, which takes between one and one and a half minutes, some of the reads are sent to the
server that is gone. Events keep being resolved in that time, at the cost of more database reads.

Each `device-management` replica also keeps what it read from, or wrote to, a bucket in memory
for up to five minutes (`inMemoryCache.ttlSeconds`, and never longer than the cache's time to
live), and answers from there without asking NATS, including while the bucket is being skipped.
The time counts from when the value was read, not from when it was last used. The three caches
kept per device (a device by its token, its tracked relationships, its group memberships) each
hold up to 131,072 entries or 24 MiB per replica, set by `inMemoryCache.perDeviceCacheEntries`
and `inMemoryCache.perDeviceCacheMiB`. That is about 87,000 devices with no tracked relationship,
or about 26,000 with one. The profile and group-scope caches, kept per device type and per
tenant, hold 4,096 entries or 4 MiB. Each drops the least recently used entry when it is full. As
it stores a new entry it also drops expired ones from its least recently used end, stopping at
the first that has not expired. A lookup NATS reported as absent is never kept.

**A change reaches every replica at once, and the time only covers a lost message.** A change that
makes a kept value wrong (a device updated or deleted, a relationship added or removed, a
profile published or rolled back, a group's members changed, a tenant erased) removes the value
from the bucket and then sends a message on NATS that makes every `device-management` replica
drop its copy. A replica that loses its connection to NATS and gets it back drops everything it
holds, because it may have missed messages. So a replica serves a changed value for longer than a
moment only if a message was lost while it was connected, for example because the replica was too
far behind to receive it, and then only until the time runs out. The same is true of a read that
was in flight across the change and stored what it read before it. The messages are counted by
`cache_eviction_broadcasts_total`.

**Device credentials.** Each replica also keeps a device credential it has just verified, with
its device, in memory for five minutes from when it read it. It is never stored in a key-value
bucket, because it holds the credential's password. Each replica keeps up to
`inMemoryCache.credentialCacheMiB` of them (32 MiB by default). Every check of a kept credential
compares its password and expiry as a check of the stored one does, and a credential that failed
to verify is never kept. A change to a credential or its device drops the copy on the replica
that made it, and a message on NATS tells the other replicas to drop theirs. So a revocation, or
a device deleted through another replica, takes effect on the next event everywhere, and when
the copy expires if that message is lost. MQTT connects always read the database.

**Sizing for the fleet.** A value is kept in memory for five minutes from when it was read, so a
device that reports at least that often is answered from memory on every event after its first.
A device that reports less often than that is never answered from memory, and each of its events
costs one read from the key-value bucket, and one from the database for its credential. Size
each replica for the whole active fleet, not for the fleet divided by the replicas: the inbound
stream is one durable shared by every replica, so events are not divided between replicas by
device, and when the number of replicas times a device's reporting interval is under the time,
every replica sees nearly every device. A cache smaller than the fleet cycles through it and
answers almost nothing. For example, 60,000 devices reporting every 10 s, each with one tracked
relationship, need `inMemoryCache.credentialCacheMiB` of about 80 (an entry is about 1.1 KB) and
`inMemoryCache.perDeviceCacheMiB` of about 64 (a relationship set is about 950 bytes) on every
replica, and a memory limit of 768Mi to hold them. The sign is
`kv_cache_local_lookups_total{cache="relationships-by-source", result="miss"}` close to the event
rate, while `kv_cache_local_entries` for that cache stays well below `kv_cache_local_max_entries`.
A fleet too large for the cache instead shows `kv_cache_local_evictions_total{reason="capacity"}`
rising at close to the event rate, with `kv_cache_local_entries` at `kv_cache_local_max_entries`
or `kv_cache_local_bytes` at `kv_cache_local_max_bytes`; for credentials, the same signs are in
`credential_cache_evictions_total{reason="capacity"}`. The chart alerts on this with
`DeviceCredentialCacheTooSmall` and `DeviceLookupCacheTooSmall` (warning): a replica whose capacity
evictions have been more than 5% of its lookups for 15 minutes. When one fires, raise the bound
(`inMemoryCache.credentialCacheMiB` or `inMemoryCache.perDeviceCacheMiB`), and the memory limit
with it: at the defaults the six in-memory caches hold at most 112 MiB. `device-management` runs
with `GOGC=400` and a soft memory limit (`GOMEMLIMIT`) at 75% of its memory limit, so the collector
lets the heap grow to about five times what it holds, and works harder once it nears that soft
limit; where no `GOMEMLIMIT` is set the heap can instead grow to about twice what it holds before
it is collected. To resolve a fleet
faster you can also add resolvers (`resolution.workers`, within the connection pool) or
`device-management` replicas.

Removing an entry after a change (a device deleted, a profile published) is never skipped. It
waits up to five seconds, because only the bucket's leader can accept it. If it still fails, the
service logs `A key-value cache eviction failed`, and the old entry can be served until it
expires, which is the cache's configured time to live, or the in-memory time on replicas that
already had it in memory, whichever is shorter.

- **`devicechain_devicemanagement_kv_cache_unavailable{cache}`**: 1 while the bucket is being
  skipped.
- **`devicechain_devicemanagement_kv_cache_failures_total{cache, op, reason}`**: operations that
  timed out (`reason="timeout"`) or failed (`reason="error"`). An event's lookups are made at the
  same time, so when a bucket stops answering, several lookups can time out together before it
  is skipped, and each counts here.
- **`devicechain_devicemanagement_kv_cache_bypassed_total{cache, op}`**: lookups and writes that
  went to the database instead.
- **`devicechain_devicemanagement_kv_cache_request_duration_seconds{cache, op}`**: how long each
  operation took at the bucket. A lookup or write is cut off at half a second, a removal at five
  seconds. A lookup answered from memory never reaches the bucket, so `op="get"` counts only the
  lookups memory could not answer.
- **`devicechain_devicemanagement_kv_cache_local_lookups_total{cache, result}`**: lookups
  answered from memory (`result="hit"`) or passed on to the bucket (`result="miss"`).
- **`devicechain_devicemanagement_kv_cache_local_evictions_total{cache, reason}`**: entries
  dropped from memory because they were past their time (`reason="expired"`), because the cache
  was full (`reason="capacity"`), or because the entry was removed after a change
  (`reason="deleted"`).
- **`devicechain_devicemanagement_kv_cache_local_entries{cache}`** and
  **`devicechain_devicemanagement_kv_cache_local_bytes{cache}`**: how many entries, and roughly
  how many bytes, a replica holds in memory for the cache. Expired entries count until a lookup
  finds them, the cache drops them from its least recently used end as it stores a new entry, or
  the cache needs the room.
- **`devicechain_devicemanagement_kv_cache_local_max_entries{cache}`** and
  **`devicechain_devicemanagement_kv_cache_local_max_bytes{cache}`**: the most entries, and
  bytes, the cache holds in memory before it drops the least recently used.

A cache built without the in-memory copy has none of the six `kv_cache_local_` series. Today
every `device-management` cache has it.

The credential copy has series of its own:

- **`devicechain_devicemanagement_credential_cache_lookups_total{result}`**: credential checks
  answered from memory (`result="hit"`) or passed on to the database (`result="miss"`).
- **`devicechain_devicemanagement_credential_cache_evictions_total{reason}`**: credentials dropped
  from memory because they were past their time (`reason="expired"`), because the copy was full
  (`reason="capacity"`), or because the credential or its device changed, on this replica or
  another (`reason="revoked"`).
- **`devicechain_devicemanagement_credential_cache_entries`**,
  **`devicechain_devicemanagement_credential_cache_bytes`**,
  **`devicechain_devicemanagement_credential_cache_max_entries`** and
  **`devicechain_devicemanagement_credential_cache_max_bytes`**: what a replica holds, and the most
  it holds.
- **`devicechain_devicemanagement_cache_eviction_broadcasts_total{cache, result}`**: the messages
  that tell the other replicas to drop a copy, sent (`result="published"`), not sent
  (`result="publish_failed"`), received (`result="received"`), or received and dropped as
  unreadable (`result="malformed"`). A steady `publish_failed` means changes reach the other
  replicas only when their copies expire.

Separately, resolving an event that takes longer than five seconds for any reason is logged as a
warning (`Event resolution is slow`): the first one at once, then at most one line every 30
seconds, with how many there were and the slowest.

## AI inference calls {#ai-inference}

Where AI-assisted authoring is enabled (see [AI-assisted authoring](../concepts/ai-authoring.md)),
the `ai-inference` service counts what its tenants ask of it. Both series are bounded: their labels
take a fixed set of values, never a tenant, model or provider, so they do not grow with the number of
tenants.

- **`devicechain_aiinference_ai_inference_calls_total{outcome}`**: tenant inference requests, by how
  each ended.
- **`devicechain_aiinference_ai_inference_tokens_total{direction}`**: tokens the provider reported
  using, by `direction` (`input` or `output`). This is the spend signal: nothing else in the platform
  counts tokens. It is a counter to watch, not a ledger, and nothing is enforced from it.

| Outcome | Meaning |
| --- | --- |
| `served` | The provider answered and a candidate was returned. |
| `rate_limited` | The tenant was over its inference rate ceiling and the call was refused before any provider was reached. A rising count means a tenant is sustained over its quota. |
| `consent_required` | The tenant has not opted in to external AI routing, so nothing left the platform. Expected for a tenant that has not been granted consent. |
| `unavailable` | The platform is not set up to serve the call: no model assigned, the provider disabled, a key missing. The server log names which. |
| `provider_error` | The provider was reached and failed the call (transport error, a non-success status, an empty answer). The external dependency is misbehaving. |
| `timed_out` | The provider was reached and did not answer within `inferenceTimeoutMs` (40 seconds by default, at most 120 seconds). The author sees a timeout rather than a generic failure. |

Read `timed_out` and `provider_error` apart. A rising `provider_error` count says the provider is
failing. A rising `timed_out` count with `provider_error` flat says the model in use is slower than
the timeout allows: choose a faster model, or raise `inferenceTimeoutMs`, which the chart's
`ai-inference` configuration exposes and which refuses to start above its maximum.

## Maintenance passes {#maintenance-passes}

Several services run a maintenance task on a timer: a sweep, a reconciler or a scheduler. Each
exports three series under its own service's name, `devicechain_<area>_<task>_…`:

- **`<task>_passes_total{outcome}`**: passes made, by outcome.
- **`<task>_pass_duration_seconds`**: how long a pass took. A `skipped` pass is not timed.
- **`<task>_last_success_timestamp_seconds`**: the Unix time of the last pass that did its work
  (`complete` or `partial`). It reads NaN until the first such pass, so a rule such as
  `time() - X > threshold` stays quiet on a pod that has just started.

| Service | Tasks |
| --- | --- |
| user-management | `dead_letter_sweep`, `tenant_purge` |
| notification-management | `retention_sweep`, `escalation_scheduler` |
| event-management | `anchor_sweep` |
| device-state | `inactivity_sweep` |
| event-sources | `presence_demote`, `presence_reconcile`, `presence_canary` |
| device-management | `ownership_drain` |
| command-delivery | `command_sweep`, `hold_reconcile`, `stranded_reconcile` |
| event-processing | `detect_tick`, `rule_health_flush`, `shed_summary` |
| lwm2m-ingest | `tenant_sweep` |
| sparkplug-ingest | `presence_probe` |

Three of these run only while something drives them: `detect_tick` and lwm2m-ingest's
`tenant_sweep` on the replica that holds leadership, and `presence_probe` once per connected
Sparkplug session, with every session on one series. On a standby replica, or while
sparkplug-ingest has no session, the last-success time stays NaN or keeps the value from its
last term. Take `max by (job)` across replicas before comparing it with `time()`.

For example, user-management's tenant-deletion coordinator exports
`devicechain_usermanagement_tenant_purge_passes_total`.

| Outcome | Meaning |
| --- | --- |
| `complete` | The pass ran and finished its work. |
| `partial` | The pass ran and did some of its work, for example some tenants but not others. |
| `failed` | The pass could not do its work. |
| `skipped` | Another replica holds the task's lock, so this one correctly did not run. Not a fault, and it does not move the last-success time. |
| `cancelled` | The pass was cut short because the service was stopping. Not a fault. |

Alert on `failed` and on a last-success time that has stopped moving, not on `skipped` or
`cancelled`: a service with more than one replica skips on every replica but one, and every
deploy can cancel a pass. The sweeps in user-management, notification-management and
event-management start each pass at a random point within 10% either side of their interval,
so replicas that started together do not all reach the database at once.

A task can pass cleanly while the work it exists for is stuck. Tenant deletion is the case the
chart alerts on separately; see [Tenant deletion](./tenant-deletion.md#stalled-alert).

## Backups that stop shipping {#backup-archiving}

A database's backups fail quietly. Archiving runs beside the writes rather than in their way,
so a write-ahead log archive that cannot reach its destination slows nothing down and trips no
health check. But PostgreSQL keeps every segment it has not shipped, on the database's own
volume, and only the primary holds them: the replicas do not help. When that volume fills,
PostgreSQL stops, and the database operator does not fail over a full disk. The usual chain is:
the backup object store fills, archiving fails, then the primary's volume fills.

The chart's alerts for each link of that chain:

| Alert | Fires when | What to do |
| --- | --- | --- |
| `PostgresWALArchivingFailing` | The last archive attempt failed more recently than the last one succeeded, for 5 minutes. | Check that the object store is reachable, has space and accepts the credentials. |
| `PostgresWALArchiveBacklog` | A database has more than 32 finished log segments (512 MiB) waiting to ship, for 5 minutes. This includes an archiver that is slow or hung and records no failure at all. | If the failing alert fires too, fix the destination. If not, check the backup sidecar's logs and the store's free space, and grow the database volume if the backlog keeps rising. |
| `BackupDestinationFillingFast` | The in-cluster object store is more than 65% full and, at its rate over the last 10 minutes, will be full within the hour. | Find what is writing: an instance ingesting faster than the store was sized for, backups left by instances that no longer exist, or a base backup schedule that stopped, so nothing is pruned. Grow the store, remove what no running instance owns, or move backups off the cluster. |
| `BackupDestinationAlmostFull` | The in-cluster object store is more than 85% full, for 15 minutes. | The same, with less time. |
| `PostgresHasNeverArchivedWAL` | A database has never shipped a log segment, for an hour. The first base backup forces a segment switch, so a working cluster leaves this state within minutes of creation; an hour in it means archiving has never worked here, not that the database is quiet. It records no failure, so the failing-archival alert cannot see it: an archiver that hangs rather than errors looks exactly like this. | Check the backup sidecar's logs and that the object store is reachable. |
| `PostgresWALArchivingNotConfigured` | A database has archiving switched on and no backup plugin behind it, for 15 minutes. Every log segment is "archived" by a command that discards it and reports success, so nothing else tells you: the archived count rises, no failure is recorded, the disk does not grow and every other backup alert stays green. **The database cannot be restored to any point in time.** | Check that the Cluster still carries its barman-cloud entry under `spec.plugins` and that its ObjectStore exists. |
| `DatabaseVolumeFillingFast` | An event-store volume is more than half full and, at its rate over the last 15 minutes, will be full within the hour, for 3 minutes. | If only the primary climbs, it is unshipped log: fix archiving first. If every member climbs, it is data: grow the volume or shorten data retention. |
| `DatabaseVolumeFillingUp` | A database volume is more than 75% full, for an hour. Database storage here does not come back on its own: several tables are append-only by design and nothing prunes them (the audit journal and the version histories of device profiles, entity groups, dashboards, connectors and geofence sets, plus the geofence geometry archive), because a stored event names the version it was resolved against and a rollback returns to a stored version. Retention settings do not apply to them; those prune the time-series store. | Grow the volume while there is room to work, and find what is producing versions. On the relational primary, count rows per tenant in a history table, for example `SELECT tenant_id, count(*) FROM "device-management".geo_fence_set_versions GROUP BY tenant_id ORDER BY 2 DESC LIMIT 10;`. One tenant re-authoring in a loop is the usual answer. For an event-store volume, check the data-lifecycle retention settings; for a `-wal` volume, the archiving alerts above are where the cause is. |
| `DatabaseVolumeAlmostFull` | A database volume is more than 85% full, for 15 minutes. | Grow it now. |

The two rate alerts exist because the fixed thresholds are too slow for a volume that fills in
minutes, which is what sustained ingest does to one. They were checked against a benchmark in
which the event-store primary went from 45% to full in about nine minutes after the object store
filled: the rate alert fires about two minutes before the volume is full, while the 85% alert,
which waits 15 minutes, would have fired only after it.

The object store is shared by every instance on the cluster, and so is the relational database.
Each instance's chart carries these alerts, so an alert about a shared volume or the shared
database is raised once per instance. The default store is sized for one instance ingesting
continuously: with more, it can fill before any event store does, and these alerts are the
warning.

For how large the in-cluster store needs to be, see
[Backup store size](./bootstrap.md#backup-store-size).

### Volume-snapshot base backups {#snapshot-backup-alerts}

On a cluster installed with
[`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups), the daily base backup is a
volume snapshot and the one in the backup store is weekly. `PostgresNoRecentBaseBackup`, which
watches the backup store's base backups, then waits 8.5 days instead of 36 hours, and three more
alerts are rendered:

| Alert | Fires when | What to do |
| --- | --- | --- |
| `PostgresNoRecentSnapshotBackup` | A database has completed no snapshot in 36 hours, or its snapshot schedule has never completed one, for an hour. | Read the events on the `<cluster>-snapshot` ScheduledBackup and its latest Backups. A VolumeSnapshotClass that was deleted, or belongs to another storage driver than the database volumes, fails every snapshot. Point-in-time recovery still works from the weekly base backup meanwhile. |
| `DatabaseSnapshotPruningStalled` | The DeviceChain operator has not finished pruning a snapshot schedule for over an hour, for 30 minutes. | Check the operator is running, at the release the cluster was installed with, and read the ScheduledBackup's events. If the operator is running and its log shows no `snapshot retention pass failed` error, but the ScheduledBackup has never carried a `devicechain.io/snapshot-retention-checked-at` annotation, and its events show no `SnapshotRetentionIgnored` warning (a schedule whose method is not `volumeSnapshot`), the operator is not selecting it: it acts only on ScheduledBackups labelled `app.kubernetes.io/component: database-snapshot-backup` that carry `devicechain.io/snapshot-retention`, in a namespace DeviceChain created. Until it resumes, every snapshot is kept, each a full copy of the database. |
| `DatabaseSnapshotBackupsUnobserved` | Snapshot backups are on, but no snapshot schedule is visible to monitoring, for an hour. | Check the snapshot ScheduledBackups exist, and that the monitoring stack's kube-state-metrics reads CloudNativePG Backups and ScheduledBackups. Until then, neither alert above can fire. |

These read the Backup and ScheduledBackup objects through kube-state-metrics, not CloudNativePG's
own backup gauge, which is set only on each database's primary.

## The database control plane {#database-control-plane}

A database can be healthy and still be unable to fail over. The PostgreSQL operator
(CloudNativePG) and its backup plugin decide which instance becomes the primary. A Cluster whose
plugin the operator cannot reach stops reconciling, so it is never promoted: the primary can be
lost while healthy, fully caught-up replicas sit there and nothing elects one. Every instance pod
stays `Running` throughout, and none of the backup alerts above fires, because nothing about
PostgreSQL is wrong. The opposite failure also exists: a Cluster that reports `Ready` while every
service is locked out of it. These five alerts cover both, and the monitoring gaps that would hide
them. They are rendered when the chart is told the namespace of the database operator.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `CNPGClusterNotReady` | critical | A PostgreSQL Cluster has reported `Ready=False` for 15 minutes. **This blocks failover.** A Cluster passes through `Ready=False` during a switchover, a rolling restart and its first bootstrap, but a recovery takes seconds, so a quarter of an hour is not mid-operation. A phase of "Cluster cannot proceed to reconciliation" points at the operator or the backup plugin rather than at PostgreSQL. | Read the Cluster's `.status.phase` and conditions (the alert names the Cluster and the reason). If the phase mentions plugins, check `CNPGControlPlaneUnavailable`. |
| `CNPGControlPlaneUnavailable` | critical | A Deployment in the database operator's namespace has had no available replicas for 10 minutes. With the operator down, no Cluster can fail over, scale or be reconfigured, and the databases keep serving unmanaged. With the backup plugin down the operator cannot reconcile any Cluster that references it, so the same failover block applies. | Restore the Deployment named on the alert. Expect `CNPGClusterNotReady` to follow within a quarter of an hour if you do not. Restarting kube-state-metrics restarts this alert's timer, so an outage can be reported later than it began. |
| `CNPGClusterUnusable` | critical | For 5 minutes, at least one instance of a Cluster cannot be queried by its own metrics collector, while the pods and the Cluster's `Ready` condition may look healthy. The alert cannot say which instance. If it is the primary, clients are affected, and the likeliest cause is **connection exhaustion**: every database-backed service opens a pool of up to 20 against the primary, a rolling upgrade briefly doubles the pods, and services then fail with "remaining connection slots are reserved for roles with the SUPERUSER attribute". If it is a standby, clients are unaffected and a replica is failing. | As a superuser, which still works while this fires, compare `SELECT count(*) FROM pg_stat_activity` with `SHOW max_connections` on each instance. Changing `max_connections` is itself a rolling restart, which the 5-minute wait rides out. |
| `CNPGCollectorAbsent` | warning | For 10 minutes nothing has reported that collector series in a database namespace, so whether the instances can answer is unknown, and `CNPGClusterUnusable` cannot fire because it reads the same series. | Check that the CloudNativePG PodMonitor for the Cluster exists and selects its pods, that Prometheus can read the namespace, and that the instance pods are running and serving port 9187. |
| `CNPGClusterStateUnobserved` | warning | For 30 minutes nothing has reported Cluster conditions in a database namespace, so the health of failover is unknown, and `CNPGClusterNotReady` cannot fire. This is a monitoring gap, but the gap that hides a failover block. | Check that kube-state-metrics is running, that it was started with a custom-resource-state config (the OpenTofu monitoring module sets it when CloudNativePG is enabled), and that its role can list `postgresql.cnpg.io/clusters`. A missing permission shows only as a `forbidden` line in its log. |

## Command delivery {#command-delivery-alerts}

Three alerts cover the states in which command delivery reports something false while nobody is
looking. The service exports many more counters, listed in the
[metrics reference](./metrics-reference.md#command-delivery), and a Command Delivery dashboard
charts them. The thresholds are reasoned rather than measured, and each is checked in both
directions by unit tests.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `CommandClaimsStranded` | critical | In the last 30 minutes, for 5 minutes, at least one command was left reading `SENT` after both its publish and the release back to `QUEUED` failed. It was never delivered. On LwM2M the stranded reconciler re-arms it within about ten minutes and it is delivered when the device next wakes; on MQTT nothing retries it, and it expires as `TIMEOUT`, which records the failure against the device although the device never received anything. | Treat every MQTT `TIMEOUT` reported in this window as unattributed, and do not send engineers to inspect the devices named. Look for a broker outage that overlapped a database outage: it takes both to strand a claim. For LwM2M, `stranded_recovered_total{disposition="PARKED"}` confirms the re-arm, while `CANCELLED` means the command's batch had been called off and the row was cleaned up. |
| `CommandPresenceReadErrors` | warning | For 15 minutes, the dispatch sweep has been unable to read the presence projection. The presence gate, which withholds commands for devices that are absent, **fails open** by design, so commands are being dispatched without the check. Nothing else shows it: dispatch looks normal and the holds counter falls to zero, which reads as "no devices are absent" rather than "nothing is being checked". | Check the device-state service and the health of the presence projection. Until it is fixed, a quiet holds panel is not reassuring. |
| `CommandBatchReserveRefusals` | warning | For 30 minutes, fleet writes have been refused that would have fitted a tenant's own undelivered-command ceiling, and were refused only by the headroom the delivery machinery reserves for itself. Nothing is broken and every caller was told; the ceiling and the reserve disagree about what a normal fleet write is. | Raise the tenant's ceiling or lower the reserve, deliberately. The counters carry no tenant label, so the dashboard shows which codes and which bound but not which tenant. Find the tenant in the recorded command batches: a batch that fanned out partially records a per-device `HELD_CEILING_EXCEEDED` refusal, while one refused whole records nothing. |

## Device self-registration {#self-registration-alerts}

Three alerts cover the endpoint devices register themselves through
(`POST /api/device-management/provisioning/{tenant}/register`). The counters behind them are in
the [metrics reference](./metrics-reference.md#device-management). The thresholds are reasoned
rather than measured, and each is checked in both directions by unit tests.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `ProvisioningAuthFailuresSustained` | warning | For 15 minutes, more than one self-registration request a second has been refused because the provision key or secret was not valid. Every cause is counted together and answered the same way, on purpose: an unknown tenant, an unknown key, a wrong secret, and a profile that is disabled or expired. A steady rate is either a fleet registering with a secret that is wrong or was rotated, or someone probing the endpoint. | Check the provisioning profile the fleet should be using: enabled, not expired, secret current (rotate it if it leaked). Look at the sources in the ingress log. Nothing else needs to be done for a probe: the secret is 256 random bits and cannot be guessed. |
| `ProvisioningRateLimited` | warning | For 15 minutes, self-registration requests have been refused with `429` at more than one every ten seconds. The `result` label says which limit: `rate_limited_source` is one client address using up its allowance, `rate_limited_tenant` is the tenant's registrations per second at its governance ceiling. Devices wait the `Retry-After` they are told and try again. | For `rate_limited_source`, check that `infrastructure.http.trustedProxies` names the ingress controller and that the controller preserves the client address. Without them every device shares one allowance. For `rate_limited_tenant`, raise the tenant's provisioning ceiling or spread the factory run out. |
| `ProvisioningMisconfigured` | warning | In the last 10 minutes at least one request was refused by a provisioning profile that cannot work: its device type was deleted, its strategy is not one the platform knows, or it mints a credential type this build does not support. Every device using that profile is refused until it is fixed. | Find the profile in the service log (the token is logged at `ERROR`) and fix or recreate it. |

## Safety gates that switched themselves off {#safety-gates}

A few services carry a safety gate that depends on another service, and each comes up **off**, with
one warning at startup, when its configuration is absent. That is a deliberate availability trade:
the device plane must not hard-depend on those services. It is also invisible, because work keeps
flowing and nothing errors while the thing the gate exists to prevent resumes. A chart install always
supplies the configuration, so an off gate means a misconfigured or hand-edited install.

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `SafetyGateDisabled` | warning | For 5 minutes, a service named by the `job` label has run with the gate named by the `gate` label off. One replica started without its configuration is enough, because it is already off for its share of traffic. With `tenant_lifecycle` off, work is admitted for deleted tenants. With `presence` off, commands to absent devices are published and dropped. With `rule_validation` off, detection rules that cannot compile are allowed to publish and are skipped when loaded. | The service logged a warning at startup naming what is missing: the service secret, or the address of the service the gate consults. Restore it and restart the service. |

## Profiling a service {#profiling}

Metrics show how much work a service does and how long it takes. They do not show where the
time goes inside it. For that, a service can serve Go's runtime profiles: a CPU profile, the
heap and allocation profiles, a goroutine dump, and an execution trace. The profiles are served
on a listener of the service's own, separate from its HTTP port.

### Why it is off

The profiles expose the service's internals, including function names, call stacks and where
it allocates memory. A CPU profile or a trace also costs CPU for as long as it runs. The
listener is a measuring tool, not a monitoring feed, so it stays off until you turn it on for
the service you want to measure.

When it is on, it listens on the pod's loopback address, `127.0.0.1:6060`, by default. The
listener is never a container port, a Service port or an ingress route. It has no
authentication of its own. Only someone the cluster allows to port-forward to the pod can
reach it, so access is controlled by the cluster's permissions, not by DeviceChain.

### Turning it on

Set it per service under `functionalAreas`. Only that service's pods restart:

```yaml
functionalAreas:
  device-management:
    profiler:
      enabled: true
```

- **Installed from the Helm chart directly:** add the block to your values and run
  `helm upgrade` as you normally do.
- **Installed with `dcctl bootstrap`:** `dcctl` has no option for this. Change the release
  with `helm upgrade`, using the chart version the instance already runs. `helm list -n default`
  shows that version, and the release is named `dc-<instance>`:

  ```bash
  helm get values dc-<instance> -n default -o yaml > dc-values.yaml
  # add the profiler block above to dc-values.yaml
  helm upgrade dc-<instance> oci://ghcr.io/devicechain-io/charts/devicechain \
    --version <chart-version> -n default -f dc-values.yaml
  ```

  This changes only the pod settings, not the instance configuration Secret that `dcctl`
  owns. The next `dcctl upgrade` or `dcctl bootstrap` recomputes the release and turns the
  listener off again.

The setting is refused at install time if the service is not deployed, because it would do
nothing. It is also refused if the address uses a port the service's pod already serves.
Once the pod is running, it logs `Profiling listener is ON` with the address.

### Capturing a profile

Forward a local port to the pod, then point Go's tools at it. You need a Go toolchain on your
own machine; the service images do not include one.

```bash
kubectl -n dci-<instance> port-forward deploy/device-management 6060:6060

# a 30-second CPU profile, opened in a browser
go tool pprof -http=:8081 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'

# the heap, saved to open later
curl -o heap.pb.gz http://127.0.0.1:6060/debug/pprof/heap

# a 5-second execution trace
curl -o trace.out 'http://127.0.0.1:6060/debug/pprof/trace?seconds=5'
go tool trace trace.out
```

A port-forward to a Deployment reaches **one** of its pods. When a service runs more than
one replica, forward to the pod you mean by name (`kubectl port-forward pod/<name> …`).

| Path under `/debug/pprof/` | What it is |
| --- | --- |
| `profile?seconds=N` | CPU profile, 1 to 60 seconds (default 30) |
| `trace?seconds=N` | Execution trace, 1 to 60 seconds (default 1) |
| `heap` | Live heap, sampled (`?gc=1` runs a collection first) |
| `allocs` | Every allocation since the process started, sampled |
| `goroutine` | Every goroutine's stack (`?debug=2` for the full text form) |
| `threadcreate` | Stacks that created operating-system threads |

`?debug=1` returns a snapshot profile as text rather than the binary format.

### What it does not serve, and why

- **`block` and `mutex` answer 404.** Their sampling is off in these services, so they would
  always be empty, and an empty profile would read as "no contention" when nothing was
  measured.
- **A heap or allocation profile over a time window (`?seconds=` on those) is refused.**
  Take two snapshots and compare them with `go tool pprof -base first.pb.gz second.pb.gz`.
- **`cmdline` and `symbol` are not served.** The profiles carry their own symbols.
- **One CPU profile and one trace at a time.** A second request for either answers 409 until
  the first ends.
- **A profile in progress when the service shuts down is abandoned** so that shutdown does not
  wait for it. A CPU profile answers 503. A trace has its connection cut, so your tool reports
  an error instead of saving a trace that looks complete.
- **A download that stops being read is cut.** A profile must reach you within 10 seconds of
  the time you asked for (`?seconds=` plus 10). A client that stalls longer than that, such as
  a stuck `kubectl port-forward` or a suspended `curl`, has its connection closed, so it cannot
  keep the trace running. At shutdown, a connection still open a second after the listener
  starts to stop is closed.
- **Each profile is served on a connection of its own.** The response closes its connection, so
  a tool that reuses connections opens a new one for the next request.

### Another address

`profiler.address` sets where the listener binds. It must be an IP address and a port, for
example `0.0.0.0:6060` for a profiler that collects from other pods. The service refuses to
start on a host name, a missing port, or its own port 8080.

:::warning Any address other than loopback is open to the cluster network
The listener has no authentication, and the chart renders no network policy that limits who
can connect to a service. So on a non-loopback address, anything that can reach the pod's IP
can read its profiles unless you add an ingress network policy of your own.
The service logs a warning when it starts on such an address.
:::

### Turning it off

Set `enabled: false` or remove the block, and upgrade the release the same way you turned it on.
Only that service's pods restart.

## Related

- **[Bootstrap an Instance](./bootstrap.md#install)** — `dcctl install`, the command
  that deploys the monitoring stack, and its flags.
- **[Deployment & Operator](./kubernetes-operator.md)** — how the chart renders
  per-service workloads with their health probes.
