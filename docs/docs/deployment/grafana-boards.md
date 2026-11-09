---
sidebar_position: 5.5
title: Grafana Boards
---

# Grafana Boards {#grafana-boards}

The [monitoring stack](./observability.md#the-monitoring-stack) that `dcctl install` deploys
ships ten Grafana boards for every instance: an **overview** that answers "is everything
up?", seven **stage boards**, one for each stage a message passes through on its way from
a device to a delivered notification, plus the platform underneath them, and two boards for the
optional [Sparkplug and LwM2M services](./edge-services.md). Each board is a starting point for
one question, and each links to the next board you would open. The edge agent runs outside the
cluster and has its own [dashboard file](#edge-agent-dashboard) instead.

Everything on the boards is built from the metrics the services already export (see the
[metrics reference](./metrics-reference.md)) and from the standard Kubernetes, database and
message-broker series the monitoring stack collects. Nothing needs to be switched on to get
them.

The pictures on this page were taken on a single-node local instance carrying a steady synthetic load from four tenants: telemetry, alarms raised and cleared by rules, and commands that devices answer. Times are in UTC; the board pictures cover 30 minutes and the outage pictures 15.

## Finding the boards {#finding-the-boards}

[Open Grafana](./observability.md#reaching-grafana) and look for the folder
`devicechain-<instance>`. Each instance has its own folder and its own copy of every board,
titled `DeviceChain — <name> [<instance>]`. A board only ever reads its own instance's data:
there is no instance picker, and one instance's boards cannot show another's.

Every board has a **DeviceChain boards** menu at the top that lists this instance's boards, and
the overview ends with a row of links to the stage boards. The one thing you pick is the
**data source**, which is the Prometheus that the stack installed.

Every board starts with the same **Firing alerts** table. It lists the alerts that are firing
right now for this instance and that belong to the board's stage, so a stage board is quiet
when its stage is fine. An empty table means none are firing; Grafana shows it as *No data*.
The table reads the instance's Prometheus rules, so it stays empty if those rules are not
installed. The overview lists every
alert, which means an alert that no stage board matches is still visible there.

## Reading a panel {#reading-a-panel}

Every panel has a description: hover the small `i` in its corner. It says what the panel
counts, what a normal value looks like, and what to do about an abnormal one. The conventions
below hold across all the boards.

- **Stat tiles are coloured by what they mean.** Green is healthy, yellow means look soon, and
  red means act. A tile named "over the range" or "selected range" counts what happened across
  the time range you picked, so widen the range to look further back; a tile without that
  wording shows the current value.
- **A tile that should stay at zero shows `0`, not "No data".** Where a count has nothing to
  count, the board substitutes zero on purpose. A panel that shows "No data" instead is
  telling you the series has not been reported yet, which is different from nothing having
  happened.

### Backlog panels, and what they show while a reader is down {#backlog-panels}

A **backlog** is the number of messages in a stream that a reader has not been handed yet.
It is the most useful early warning in the platform, because a reader that is slower than
its producers falls behind long before anything fails. See
[A consumer that stays behind](./observability.md#consumer-backlog) for what the backlog
means and how it is measured.

Each reading service reports its own backlog, but a service that has stopped cannot report
anything. A reader that crashed, is being restarted or was scaled to zero is exactly when you
most want the number, and a board that goes blank at that moment is no use. The backlog panels
therefore have **two sources, in order**:

1. **The reader's own report**, while the reading service is up.
2. **The message broker's own count** for the same reader, from the broker's metrics, when the
   reader is not reporting.

A panel that can fall back this way says so in its description (*"While the reading service
is down this shows the message broker's own count"*). Both sources count the same messages, so the panel keeps
one continuous line when one takes over from the other.

![The events-waiting-to-be-stored panel with event-management scaled to zero: the line climbs steadily.](/img/grafana/fault-persistence-backlog.webp)

*Events waiting to be stored, about two and a half minutes after `event-management`, the reader that stores events, was scaled to 0. The reader cannot report while it is down, so this line is the broker's own count of the same messages, and it climbs as events keep arriving.*

![The same panel after event-management was scaled back to one replica: the line drops back to zero.](/img/grafana/recovered-persistence-backlog.webp)

*The same panel once `event-management` was scaled back to 1. The backlog drains to zero within about a minute of the reader starting. The brief drop to zero before the last peak is the restarted reader taking the waiting events in flight, which the broker no longer counts as unread, before its own report is first scraped.*

Nineteen panels carry the broker fallback:

- **Messaging:** *Unread backlog per reader*, *Handed out, not yet acknowledged* and
  *Share of stream capacity left unread*.
- **Ingest:** *Backlog against its limit*, *Backlog and stream fill over time*, *Arriving vs
  backlog growth* (its backlog growth line) and *Capture backlog in messages* (its first line).
- **Event processing:** *Consumer lag (undelivered)*, *In-flight (delivered-unacked)*,
  *Consumer backlog*, *Derived-event backlog* and *Consumer unread backlog (share of stream
  capacity)*.
- **Delivery:** *Waiting to be sent*, *Dispatch backlog*, *Notification backlog* and
  *Update report backlog*.
- **Persistence:** *Events waiting to be stored* and *Updates waiting to be applied*.
- **Overview:** the *Largest backlog share* tile.

*Messages waiting to be redelivered* on Messaging is the broker's own count at all times, so
it has no reader source to fall back from.

Two limits are worth knowing:

- **The fallback needs the broker's metrics.** The monitoring stack scrapes the broker, so on
  a default install the fallback works. If you run your own Prometheus without scraping the
  broker, a backlog panel goes blank when its reader stops, as it did before.
- **Loss counters have no fallback.** The tiles and lines that count messages the stream
  *discarded before the reader read them* are counted by the reader itself: on Ingest,
  *Messages lost before being read, over the range* and *…, any consumer* and the second line
  of *Capture backlog in messages* (the unread gap); on Messaging, *Messages lost unread
  (range)* and *Messages lost unread /s*; and the overview's *Messages lost*. While the reader
  is down these tiles read 0, and that is not evidence that nothing was lost. Messages
  discarded during the outage are counted only after the reader returns, as a lower bound, at
  the time it returns. During an outage, judge the risk from *Share of stream capacity left
  unread*, which keeps working: at 100% the stream starts discarding messages the reader has
  not seen. See [Messages a consumer never read](./observability.md#unread-loss).

![The messaging board's readers group during the same outage: the unread backlog and the share of stream capacity rise together for the stopped reader.](/img/grafana/fault-messaging-readers.webp)

*Messaging during the same outage, at **Readers of streams**. **Unread backlog per reader** rises for the stopped reader (the legend lists every reader; hover the line to name it), and **Share of stream capacity left unread** shows how much of the stream's limit that backlog uses: here under 1%, far from the limit.*

A missing series means "not measured", never "nothing waiting". If a backlog panel is empty,
neither source has reported: check that the broker is being scraped (Messaging, *Broker servers
up*) and that the reader's durable exists.

## The overview {#overview-board}

![The overview board under steady load: every tile under "Is everything up?" is green, and the five lines of "Events through the pipeline" lie on top of one another.](/img/grafana/overview.webp)

*The overview under a steady load. Every tile under **Is everything up?** is green, and the lines in **Events through the pipeline** lie on top of one another: events received, matched to a device, stored, applied to device state and fed to rules all move at the same rate. A stage whose line falls away from the others is the stage board to open.*

**Use it to answer "is everything up, and is anything being lost?" in one glance.** Start
here, then follow the link to the stage board for whatever is not green.

| Group | What it shows |
| --- | --- |
| Is everything up? | Tiles for areas not ready, scrape targets down, missing replicas, container restarts, messages lost, and the largest backlog share across all readers. |
| How much is flowing | Events through the pipeline by stage, and work that did not succeed, side by side so a drop in one is read against the other. |
| Failures that should stay at zero | Messages refused at the door, messages that failed, commands given up on or that cannot be delivered, and rule events fed against rule actions sent. Any non-zero tile is worth a look. |
| Replicas and restarts | Available replicas by deployment, container restarts by pod, and the two area tiles described next. |
| Stage boards | Links to the seven stage boards. |

Alerts behind it: all of them. The most important for this board are
[`InstanceAreaUnavailable`](./observability.md#container-restarts) and
[`InstanceContainerRestarted`](./observability.md#container-restarts).

### Areas that are down, and areas scaled to 0 {#scaled-to-zero}

An area whose pods are all gone stops reporting everything of its own: its readiness, its
metrics, its scrape target. The *Areas not ready* and *Scrape targets down* tiles can then
read zero while the area is not running at all, because an absent series is not a zero. Two
tiles read the Kubernetes deployments instead, which keep saying what is wanted and what is
there:

| Tile | Counts | Colour | Alert |
| --- | --- | --- | --- |
| *Areas with no pod available* | Areas and the console that **want** at least one pod and have none available. | Red from 1. | `InstanceAreaUnavailable`, after ten minutes. |
| *Areas scaled to 0* | Areas and the console set to run **zero** pods. | Blue from 1. | None. |

The difference is intent. An area at zero was scaled down by hand
(`kubectl scale … --replicas=0`), for example paused for maintenance, so it is shown as its own
state rather than as an outage, and it never alerts. The two tiles never count the same area. If an area
shows under *Areas scaled to 0* and you did not scale it, scale it back: it is not serving,
and nothing will page you about it. The tiles count the areas and the console, which is also a
deployment of the instance; the message broker's debugging pod is left out of both, and out of
the alert, because it is not an area.

An area the instance's profile does not run has no deployment at all, so it appears in
neither tile.

![The overview while event-management is scaled to zero: the stored line drops away from the others, "Areas scaled to 0" reads 1 in blue, and "Areas with no pod available" stays at 0.](/img/grafana/fault-overview.webp)

*The overview while `event-management` is scaled to 0. In **Events through the pipeline** the stored line drops away while every earlier stage keeps its rate. **Areas scaled to 0** reads 1, in blue, and **Areas with no pod available** stays at 0, because the area was stopped on purpose.*

## Ingest {#ingest-board}

![The ingest board under steady load: events arrive at a steady rate and the backlog-growth line stays at zero.](/img/grafana/ingest.webp)

*Ingest under the same load. Events arrive at a steady rate, and the backlog-growth line in **Arriving vs backlog growth** stays at zero, so the instance is keeping up; above zero, events are arriving faster than they are forwarded. The devices here send over HTTP, so the MQTT capture stream stays empty.*

**Use it when events are not arriving, are being refused, or the MQTT path is under strain.**
It covers the front door: what the transports accept, what they turn away, and the backlog of
MQTT messages waiting to be forwarded.

| Group | What it shows |
| --- | --- |
| Backlog of MQTT messages waiting to be forwarded | The capture stream's backlog against its limit, its fill, messages lost before being read, and leader changes. Its backlog panels have the [broker fallback](#backlog-panels), except the unread-gap line of *Capture backlog in messages*. Above 10% and rising, add `event-sources` replicas; at 100% the oldest unread messages are discarded. |
| Refused or lost at the door | Messages refused to protect unread data, held back by rate limits, rejected as malformed, refused for a deleted tenant or a credential in the MQTT body, and external publishes that failed. |
| Protection of the MQTT message store | Required consumers present, store settings put back, gateway streams without a size limit, and connections refused because the store is full or busy. |
| Device presence and external sources | Online and offline changes not recorded, external MQTT sources without exactly one reader, and the device sign-in cache. |
| Device self-registration | Devices registered, refusals by reason, and time to answer. |
| Sparkplug ingest, LwM2M ingest | Only when those optional services run: active readers or serving state, failures, and per-protocol traffic. Each has a full board of its own: [Sparkplug ingest](#sparkplug-ingest-board) and [LwM2M ingest](#lwm2m-ingest-board). |

Alerts behind it: the MQTT and capture-stream alerts described in
[Backpressure on the ingest path](./observability.md#ingest-backpressure) and
[Messages a consumer never read](./observability.md#unread-loss), the external-source alerts in
[An external MQTT source nobody reads](./observability.md#external-mqtt-owner), the
registration alerts in [Device self-registration](./observability.md#self-registration-alerts), and the
credential-cache and rate-limiter alerts, and the Sparkplug and LwM2M ingest alerts.

## Messaging {#messaging-board}

![The messaging board under steady load: the broker server tiles, its CPU, memory and client connections, and messages through the broker.](/img/grafana/messaging.webp)

*Messaging under the same load: the broker servers, their CPU, memory and client connections, and the messages passing through them. When something is backed up and you do not know where, the **Readers of streams** group further down names the reader.*

**Use it when something is slow or backed up and you do not yet know where.** The message
broker connects every stage, so this board shows the shared plumbing: the broker servers, the
streams they hold, who is reading them and how far behind, and whether copies are in sync.

| Group | What it shows |
| --- | --- |
| Message broker servers | Servers up, who leads the cluster, clients dropped as too slow, request failures, disk, CPU, memory and connections. |
| Streams | Streams over 80% full, streams refusing new events, stream fill by size and by count, and the largest streams on disk. |
| Readers of streams | Per-reader unread backlog, messages handed out and not acknowledged, share of stream capacity left unread, messages lost unread, and messages held past acknowledgement. The [backlog fallback](#backlog-panels) applies to the first three. |
| Publishing and backpressure | Events refused by backpressure, time to publish, and the share of time publishers wait for the broker. |
| Copies and leaders | Streams with fewer copies than configured or out-of-date copies, stream leader changes, and which servers led the MQTT gateway store. |
| Failed deliveries and dead letters | Messages that ran out of delivery attempts, dead letters saved, not saved or lost, and broker connections that died silently. |
| Key-value buckets and caches | Cache reads answered from memory, memory used, evictions, failures and operation time. |

Alerts behind it: the [`JetStream*`](./observability.md#consumer-backlog) alerts,
[`BrokerConnectionDiedSilently`](./observability.md#broker-connection-dead),
[`ReaderHeldMessagePastAckWait`](./observability.md#held-past-ack-wait),
[`MaxDeliveryRecordsWaiting`](./observability.md#max-delivery-records), the
[`ReplayCoveredDeliveriesExhausted`](./observability.md#ingest-backpressure),
[`BrokerPresenceLosingTransitions`](./observability.md#event-resolution), the
[replication](./observability.md#replication) alerts and the dead-letter alerts.

## Persistence and state {#persistence-board}

![The persistence board under steady load: events are stored at the arrival rate, none are refused, and the events-waiting panel lies flat on zero.](/img/grafana/persistence.webp)

*Persistence under the same load. **Events stored per second** matches the arrival rate on Ingest, **Events not stored** is 0, the 95th-percentile time to store one event is in tens of milliseconds, and **Events waiting to be stored** (bottom right) lies flat on zero.*

**Use it when events are accepted but not showing up, or a device's live state is out of date.**
It follows an event into the event store and the live device state, and then looks at the
databases underneath.

| Group | What it shows |
| --- | --- |
| Event storage | Events stored per second, events not stored, retries, writes in progress, time to store an event, events per transaction and events waiting to be stored (with the [backlog fallback](#backlog-panels)). |
| Live device state | The same set for the device-state projection: updates per second, lost, retried, time per update, and updates waiting to be applied (with the fallback). |
| Background jobs in the event store | Time since the last history repair, calculated-metric rebuilds, and rollup refresh time and collisions. |
| Databases | Server and cluster state, standbys streaming, connections, longest open transaction, commits and rollbacks, size, standby delay, and which pod is the primary. |
| Write-ahead log and backups | Log segments waiting to ship, shipping failures, time since the last segment shipped, and the age of the newest base backup. |
| Disk space | The fullest database disk and backup disk, the shortest time until the event-store disk is full, and the largest history tables. |
| Tenant removal | Deletions in progress, the oldest open deletion, completions and failures, and the stores holding a deletion up. |

The relational database and the backup object store live in a namespace shared by every
instance in the cluster. Those panels say so in their descriptions and show the shared
database, not only this instance's.

Alerts behind it: [`DeviceStateDroppingEvents`](./observability.md#live-state-projection),
the [backup-archiving](./observability.md#backup-archiving) and
[database control plane](./observability.md#database-control-plane) alerts, the
`Database*` volume alerts, `BackupDestination*` and `TenantPurge*`.

## Event processing {#event-processing-board}

![The event-processing board under steady load: consumer lag is zero, in-flight messages rise and fall, and the watermark lag holds steady.](/img/grafana/event-processing.webp)

*Event processing while rules raise and clear alarms and send commands. **Consumer lag** is 0, and **In-flight** rises and falls as batches are handed out and acknowledged, which is normal under load. **Watermark lag** holds level; a line that keeps climbing means detection is falling behind the clock.*

**Use it when rules are not firing, alarms are late, or reactions are not being carried out.**
It watches the detection and reaction engine described in
[Event processing](../concepts/event-processing.md).

| Group | What it shows |
| --- | --- |
| Pipeline health | Consumer lag, in-flight messages, watermark lag, rules active, events applied and checkpoints per second. Consumer lag, in-flight and the consumer backlog panels have the [backlog fallback](#backlog-panels). |
| DETECT engine | Fan-out throughput, detections rejected before publish, evaluation errors, and the version-drop safety gates. |
| REACT dispatcher | Actions dispatched, dispatch faults, the derived-event backlog (with the fallback), concurrency and dispatch lag (p50 and p99). |
| State budget | Live keyed-state entries, and the tenants that are over their rule, live-key or retained-sample budget. |
| Snapshot and restore | Checkpoint cost, snapshot size and restore cost at startup. |
| JetStream streams | Stream fill and the consumer's unread share of stream capacity. |
| Geofence archive | Fence geometry that is unresolved or mismatched, and the geometry cache hit rate, occupancy and evictions. |
| Rule health and derived publish | Rule-health updates discarded, backlog and write latency, and the share of time spent publishing derived events. |

Alerts behind it: the `Detect*` and `React*` alerts, `GeoFenceSetPublishFailing` and
`DeviceFactPublishFailing`. See [Running the detection engine](./detection-engine.md) for how
the engine's consumer is run and scaled.

## Command delivery {#command-delivery-board}

![The command-delivery board while rules send commands: every fleet-write tile reads zero and the refusal graphs are flat.](/img/grafana/command-delivery.webp)

*Command delivery while rules send commands that the devices answer. Nothing wrote to a fleet in one batch during this window, so every fleet-write tile reads 0 and the refusal graphs stay flat. The commands the rules sent go through the dispatch path; the overview's *Commands given up* and *Commands that cannot be delivered* tiles read 0 for them.*

**Use it when commands are not reaching devices, or fleet writes are being refused.**

| Group | What it shows |
| --- | --- |
| Fleet writes | Batches created, replayed and refused per second, devices accepted, left out of a partial batch, and refused by code and by bound (ceiling or reserve). |
| Dispatch | Stranded claims, claims lost, and commands that are undeliverable. |
| Holds | Holds placed and released, and presence read errors. |

Alerts behind it: every `Command*` alert, described in
[Command delivery](./observability.md#command-delivery-alerts).

## Delivery {#delivery-board}

![The delivery board opened at the hand-off from automation rules: raise-alarm, clear-alarm and send-command actions handed to their sinks, nothing refused, and alarm notifications handled with no retries.](/img/grafana/delivery.webp)

*Delivery, opened at **Hand-off from automation rules**. The rules hand raise-alarm, clear-alarm and send-command actions to their sinks, none is refused, and the alarm notifications that follow are handled with no retries. The board's first group, outbound connectors, belongs to an optional service this instance does not run, so it shows no data.*

**Use it when an alarm happened but nobody was told, or when a connector or update is stuck.**
This is the last mile, from a rule's reaction to a person or an outside system. The
connector and software-update groups belong to optional services: on an instance that does
not run one, its group stays on the board and shows *No data*.

| Group | What it shows |
| --- | --- |
| Outbound connectors | Sent, retrying, given up on, blocked destinations and lost dispatches, by outcome, connector type and failure reason, plus the dispatch backlog. |
| Hand-off from automation rules | Actions handed to each sink, actions that could not be carried out, actions held back for being over quota, and rule results set aside after retries. |
| Alarm notifications | Handled, retrying and never delivered, refusals, quiet-hours holds, escalation passes, time to handle, and the notification backlog. |
| Software updates | Device reports by verdict, downloads and grants by result, rollout progress, attempts that went quiet, and the update-report backlog. |

All four backlog panels use the [broker fallback](#backlog-panels).

Alerts behind it: `ConnectorDispatchRateLimited`, `ReactConnectorEgressShedding` and
`ReactShedLettersOverBudget`. The notification group has no alert of its own; read it on this
board. The software-update group has four: `UpdateDownloadsNotConfigured`,
`UpdateReportsMostlyRefused`, `UpdateRolloutStepsFailing` and `UpdateAttemptsGoingUnknown`.

## Platform {#platform-board}

![The platform board under steady load: no replicas missing, no pods unready, no restarts, and every deployment's available replicas equal to what it wants.](/img/grafana/platform.webp)

*Platform under the same load: no replicas missing, no pods unready, no restarts, and every deployment's available replicas equal to what it wants. **Memory in use against limit**, at the bottom, is the panel to read before changing a service's limits.*

**Use it when a service looks unhealthy and you need to know whether it is the service or the
node under it.** It is the one board that is about the processes, not the message flow.

| Group | What it shows |
| --- | --- |
| Service health | Replicas missing, pods not ready, container restarts, and containers last stopped for lack of memory, with replicas available against wanted. |
| CPU and memory | Memory against its limit, CPU against its request, and CPU throttling. |
| Service runtime | Goroutines, heap, resident memory, garbage collection runs and pauses, and open file handles against the limit. |
| Sign-in | Credential checks by outcome, throttled sign-ins, single sign-on refusals, identity-provider fetch failures and token key fetches. |
| Tenant deletion | The same deletion view as on Persistence, from the platform side. |
| Limits and safety gates | Safety gates switched off, tenants metered at the platform default, shared allowance in use, outbound actions shed, and the ceilings hit by device traffic and by outbound calls. |
| AI inference | Inference calls by outcome and model tokens used. |

Alerts behind it: [`InstanceContainerRestarted`](./observability.md#container-restarts),
the `Sso*` and credential-store alerts, `TenantPurge*`,
[`SafetyGateDisabled`](./observability.md#safety-gates) and
[`TenantsMeteredAtPlatformDefault`](./observability.md#tenant-ceilings), `RateLimiterOverflowInUse`,
`RateMeteringClockFallback`, `ReactShedLettersOverBudget` and `ConnectorDispatchRateLimited`.

## Sparkplug ingest {#sparkplug-ingest-board}

**Use it when Sparkplug nodes stop reporting, or when the Sparkplug host is not reading.** It
follows one transport from the broker connection to the readings stored, and on to the commands
sent back to edge nodes. The service is optional: on an instance that does not run it, every
panel shows no data or zero.

| Group | What it shows |
| --- | --- |
| Is it reading? | Active readers (exactly one is healthy), broker connection failures, refused group subscriptions, messages dropped, undecodable messages, and nodes that cannot register. |
| What arrives | Messages by type, and the readings, presence changes and devices registered that they produced. |
| What was not stored | Readings skipped by reason, and everything dropped: at ingest, undecodable, from unknown devices, for a tenant being deleted, too old, and shed over a tenant's ceiling. |
| Presence and rebirths | Rebirth requests and the ones dropped, liveness probes and the devices they marked offline, registration recovery, and how long a probe pass takes and when the last good one ran. |
| Commands to edge nodes | Commands awaiting an outcome, unconfirmed commands, command faults, breaker trips and the sources whose breaker is not closed, commands by result, time to confirm, and each source's breaker state. |

Alerts behind it: the `SparkplugIngest*` and `SparkplugCommand*` alerts in
[Running the Edge Services](./edge-services.md#edge-alerts).

## LwM2M ingest {#lwm2m-ingest-board}

**Use it when LwM2M devices cannot be reached, stop reporting, or their commands do not land.**
It covers the CoAP and DTLS server, the fleet it holds, the readings it stores, and the command
path to devices. The service is optional: on an instance that does not run it, every panel shows
no data or zero.

| Group | What it shows |
| --- | --- |
| Is it serving? | Leaders and serving replicas (each exactly one), notifications dropped, commands not carried out, handshakes refused, and sessions refused at the ceiling. |
| The fleet it holds | Registrations, sessions and observations, the registration lifecycle, failover markers, refused registrations, DTLS handshakes, and health-probe requests. |
| Readings | Notifications and readings stored, notifications that produced no reading, non-numeric records, everything dropped, and devices created by telemetry. |
| Commands | Commands dispatched by operation, commands set aside and why, commands for offline or other devices, command faults, and the drain of the backlog. |
| Device information and housekeeping | Device information harvests and their write problems, and the sweep that removes deleted tenants' registrations. |

Alerts behind it: the `Lwm2mIngest*` alerts in
[Running the Edge Services](./edge-services.md#edge-alerts).

## Edge agent {#edge-agent-dashboard}

The edge agent runs on hardware at your sites, and its metrics endpoint listens on the box's
loopback address only. The instance's Prometheus never scrapes it, so the chart installs no board
for it. The dashboard is a file you import into the Grafana that reads **your own** Prometheus:
[`edge-agent.json`](https://github.com/devicechain-io/devicechain/blob/main/backend/edge/dc-edge-agent/dashboards/edge-agent.json).

To use it, get the agents' metrics into a Prometheus. The metrics endpoint listens on the box's
loopback address (`127.0.0.1`, port `local.metricsPort`, default 9090), so the collector has to be a
process on the box itself: nothing off the box can reach it. Every agent's series must carry a
**unique `agent` label**. The board's picker, legends and groupings all use it, and without one the
series of different boxes have identical labels and overwrite one another where they are received.
Set it as an external label on each box's collector. For Prometheus in agent mode:

```yaml
global:
  external_labels:
    agent: site-berlin-01   # unique per box
scrape_configs:
  - job_name: dc-edge-agent
    static_configs:
      - targets: ["127.0.0.1:9090"]
remote_write:
  - url: https://prometheus.example.com/api/v1/write
```

The board's process panels (memory, CPU, goroutines, open files) select the job `dc-edge-agent`, so keep that `job_name`: with another name those panels are empty.

The agent's default port, 9090, is also Prometheus's default listen port, so give one of them another:
start the collector on a different one, as below, or set `local.metricsPort` in the agent's
configuration and scrape that port.

```bash
prometheus --enable-feature=agent --config.file=agent.yml --web.listen-address=127.0.0.1:9091
```

Then in Grafana choose **Dashboards, New, Import**, upload the file, and pick that Prometheus as the
data source. The board has two pickers, **Agent** (every agent that sends the label) and **Lookback**
(how far back to look for agents that have stopped reporting). A site that loses its link stops
sending anything, including its own disconnected state, so the *Known agents reporting* and *Agents
silent* tiles and the *Agents* table compare what is reporting now with what was seen in the
lookback window. The board has no *Firing alerts* table, because the agent ships no alert rules.

| Group | What it shows |
| --- | --- |
| Is every site connected? | The share of known agents reporting, agents silent and agents disconnected, the oldest buffered event, the fullest buffer, events lost to overflow, events refused, agents with an open local port, and a table with one row per agent. |
| Connectivity | The cloud link of each agent over time, and publishes waiting for the cloud. |
| Store-and-forward backlog | Age of the oldest buffered event, buffered events, buffer used against its limit, buffer fill, and events lost to overflow. |
| Throughput | Events read for forwarding and acknowledged by the cloud, forwarded per agent, and failed forward attempts. |
| Refused and rejected events | Refusals by reason, refused events kept or unmatched, events discarded on the box, and whether the local port requires a credential. |
| The agent process | Memory, CPU, goroutines and open files of each agent. |

Every panel reads a series the agent exports. A test in the agent's source tree checks the file
against the agent's real metrics, so a renamed metric fails the build instead of leaving a site with
an empty graph.

## What the boards do not cover {#not-covered}

- **The Kubernetes operator** runs in its own namespace. Nothing scrapes its metrics, and no
  board shows it, not even the replica panels, which read only this instance's namespace.
- **HTTP and GraphQL request rate, errors and latency** per service are not exported, so no
  board charts them.
- **End-to-end event age** from ingest to stored or detected is not measured. The backlog
  panels are a stand-in: a growing backlog is a growing delay. The nearest actual measure of
  age is `detect_watermark_lag_seconds`, shown on Event processing as *Watermark lag*, which
  is how far the detection engine's watermark trails the wall clock.
- **Edge agents** run on customer hardware and are not scraped by the instance's Prometheus, so
  no board in the chart shows them. Import the [edge agent dashboard](#edge-agent-dashboard) into
  the Grafana that reads your own Prometheus.

## Related {#related}

- [Observability and metrics](./observability.md): what every service exposes, and the
  runbook entry for each alert named above.
- [Metrics reference](./metrics-reference.md): every series by service.
- [Tuning an instance](./tuning-an-instance.md): what to change when a board shows a limit
  being reached.
