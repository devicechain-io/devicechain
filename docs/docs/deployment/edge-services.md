---
sidebar_position: 8
title: Running the Edge Services
---

# Running the Edge Services

Three DeviceChain components sit at the edge of the platform, and none of them behaves like the
stateless services around them. [Sparkplug-B ingestion](../concepts/sparkplug.md) and
[LwM2M ingestion](../concepts/lwm2m.md) are **presence-asserting transports** — they are *told* when a
device connects and disconnects rather than guessing it from silence. The **edge agent** is a separate
binary that runs on a box at a site, buffers locally when the link to the cloud is down, and forwards
when it comes back.

All three run as a **single instance**, all three hold live state that no database holds, and what a
restart costs differs from one to the next. This page is the operator's contract: how many run and
why, what a failover loses, what presence does and does not guarantee, and what to watch.

If you are looking for what each protocol does or how a device maps onto the platform, start at
[Sparkplug-B](../concepts/sparkplug.md), [LwM2M](../concepts/lwm2m.md) or
[Device Presence](../concepts/device-presence.md) instead.

Both ingest services are **opt-in**. Neither is in the default set of functional areas — you enable
them deliberately — and each hard-depends on device management, which resolves what they produce.

## One instance each, and the reasons differ

It is worth knowing that these are three separate arguments, not one policy applied three times.

| Service | Why exactly one | What a second one would do |
|---|---|---|
| **Sparkplug ingestion** | It joins your broker as a Sparkplug **Host Application**, and a Sparkplug environment has one of those. | Publish conflicting host STATE and ingest every message twice. |
| **LwM2M ingestion** | DTLS is a **stateful session over one bound UDP socket**. | A standby that also bound the socket would silently receive — and drop — the share of datagrams sent its way. Traffic disappears rather than failing loudly. |
| **Edge agent** | It owns a local spool directory and one identity on the cloud uplink. | Two on one directory collide on file locks; two sharing an identity kick each other off the uplink in a loop. |

For the two ingest services this is enforced rather than merely documented. Each takes an
**ownership lease** with a 30-second window: a replacement pod connects nothing and binds nothing
until it holds the lease, so the window in which two of them serve is **bounded** — by the lease
window plus one renewal interval (about ten seconds), because a leader that has lost the lease
evicts itself only when its next renewal notices, and then still has to unwind its broker or DTLS
state. It is bounded, not eliminated, and **nothing fences a stale leader's writes on these two
paths** — the lease's epoch is carried but no ingest path rejects on it. The chart also refuses to
render either area at more than one replica **alongside its default `Recreate` strategy** — but
`strategy` is an overridable per-area value, so overriding it to `RollingUpdate` defeats that guard
and the area will render at any replica count. Do not.

:::warning Neither ingest service gets a pod disruption budget
The chart skips a disruption budget for any area running a single replica, because a budget demanding
one available pod would block a node drain outright. Draining the node an edge service happens to be
on therefore **stops that transport** until the pod is rescheduled and takes the lease. Recovery is
automatic, but it is not instant — prefer a deliberate rollout over draining that node.
:::

## What a failover costs

A restart or a leadership handover is routine on both services, and both come back on their own. What
they recover, however, is not the same.

| | Sparkplug ingestion | LwM2M ingestion |
|---|---|---|
| **Presence** | **Reconstructed.** The new leader re-establishes the session, asks edge nodes to re-announce themselves, and reconciles which devices are actually live — so a disconnect that happened during the changeover is not missed. | **Reconstructed** from the stored projection and each device's registration lifetime, rather than by probing, so a sleeping queue-mode device is not false-flagged offline. |
| **Telemetry during the window** | Lost. A broker does not hold DATA for a host that is not connected. | Datagrams sent during the window are lost; confirmable CoAP messages are retransmitted by the device. |
| **Observed resources** | Not applicable. | **Not re-established. See below.** |
| **Recovery time** | However long the replacement pod needs to schedule and start, plus up to the 30-second lease window. | The same, plus binding the socket. |

:::danger LwM2M observations are lost on failover and nothing re-creates them
This is the most surprising operational fact on this page.

DeviceChain asks an LwM2M device to **Observe** its resources so the device pushes readings on its
own. Those observations live only for the life of the process that established them. A restart, a
rollout or a leadership handover **loses every one of them, and nothing re-creates them.**

Presence comes back. Telemetry does not. A device starts reporting again only when it next
**re-registers** — which is the device's own behaviour on its own schedule, bounded by nothing except
its registration lifetime. With the shipped default of `86400` seconds that is **up to a day of
silence from a perfectly healthy device**, with the device online in the console the whole time.

If you cannot tolerate that, lower `maxLifetimeSeconds` (see the LwM2M settings below) — but never
below the longest lifetime your devices actually request, or they are expired as dead on every
handover.
:::

## What presence guarantees

Every device carries a **presence source** — `INFERRED` or `ASSERTED` — and the rules around it are
narrower than they look.

**Only the source can give a device back.** A device becomes asserted the first time an authoritative
transport speaks for it, and no timeout, no data event and no amount of silence moves it back to
inferred. The one thing that does is a **demotion** — a claim by the *source* that it is no longer
speaking for the device, never a claim about the device itself. See [returning a device to inferred
presence](#demoting-a-device). A device that used to arrive over Sparkplug or LwM2M and now arrives
over plain MQTT keeps its asserted source, and keeps being exempt from the inactivity sweep.

**Ordering is by a platform-minted session identity, never by anything the device sends.** Each
connect/disconnect pair is stamped with a session marker the platform generates. A Sparkplug
birth/death sequence number is read only to match a death to the birth it belongs to — it is never
compared by magnitude, because it wraps — and an LwM2M registration id is never used as the session
identity. The consequence for you is that a delayed or replayed message from an older session cannot
tear down a live one, on either transport.

Markers are minted from the clock of the broker node that accepted the connection, so across a
multi-node cluster a new session's marker is **not** guaranteed to sort above the one before it — a
node whose clock trails its peers mints a lower one. The platform reconciles that case rather than
assuming it away: a device found live on a session that sorts below its stored one is re-filed onto
the session it is actually on, so its later disconnect is still recognised.

**A device cannot assert its own presence.** A connect/disconnect event submitted through the ordinary
device-facing payload path is rejected outright, not merely ignored. Only the transports themselves
produce them. This matters because an asserted device is exempt from the inactivity sweep — a device
able to declare itself connected could pin itself online permanently.

**The inferred timeout is ten minutes and is not adjustable.** Devices without an asserting transport
are swept offline after ten minutes of silence, re-checked every minute. There is no per-device
override and no setting for it today, so do not go looking for one. It is also the timeout a demoted
device comes back under, which is most of the point of demoting one.

## A device that reads online and is not

This is the failure mode to understand before you rely on presence for anything that pages a human.

**An asserted device that dies without saying so can read online indefinitely.** The inactivity sweep
deliberately skips asserted devices — the whole point of an asserting transport is that silence is not
evidence of death — and on these two transports **nothing else has a timeout, a watchdog or a
sweeper.** Two things clear it, and neither of them is a timeout.

The first is a new signal from the device's own transport. Devices asserted by DeviceChain's own MQTT
broker get one without the device doing anything: a repair pass there periodically compares the
broker's live connection list against what the platform believes and corrects the difference. See
[Device Presence](../concepts/device-presence.md). On Sparkplug and LwM2M there is no such pass, so
the signal has to come from the device.

The second is a [demotion](#demoting-a-device), and it is the answer when the first will never
arrive — because the source that would have to produce it is gone. It works on all three asserting
transports.

The concrete ways it happens:

- **A lost Sparkplug death certificate.** If the broker never delivers the node's DEATH, the device
  stays online until the next reconciliation — and reconciliation runs **only when the host
  reconnects to the broker**. A host that stays steadily connected and simply never hears from that
  node again never re-runs it.
- **A node re-announcing itself.** When an edge node births a new session, its previous session's
  child devices are replaced along with it. A child device that does not re-announce under the new
  session is left showing connected with nothing to correct it until the next reconciliation.
- **An LwM2M registration that is simply long.** A device that vanishes is marked offline when its
  registration lifetime lapses — with the default that is **86400 seconds**, one full day.
- **A device whose asserting transport is removed.** Decommission the Sparkplug source or the LwM2M
  credential a device arrived on, and nothing will ever produce another signal for it. It is stranded
  at its last asserted state until someone [releases it](#demoting-a-device) — which is precisely
  what that operation is for, since a source that is gone will not be telling the platform anything
  more.

**The one lever on these two transports is `maxLifetimeSeconds`**, and it applies to LwM2M only. Every
registration's lifetime is clamped down to at most that value, so it directly bounds how long a dead
LwM2M device can read online. Setting it to, say, 3600 caps that at an hour. The constraint is the one
above: it must stay above the longest lifetime your fleet actually asks for. The MQTT path has its own
bound — the repair pass's interval, `brokerPresence.reconcileSeconds`, five minutes by default.

There is no equivalent lever on the Sparkplug path. If a Sparkplug device reading online is
operationally load-bearing for you, pair the connectivity signal with a timeout-based
[absence rule](../concepts/event-processing.md), which fires on silence regardless of what presence
says.

### Returning a device to inferred presence {#demoting-a-device}

A **demotion** is the only transition from `ASSERTED` back to `INFERRED`. It is a claim by the
source, not about the device: it says the source is releasing custody, and it asserts nothing
whatever about connectivity. Whether the device reads online, when it last connected, when it last
disconnected and when it last reported are all left exactly as they were.

What changes is who is allowed to correct them. An asserted device suppresses both of the platform's
repair mechanisms; releasing it hands the device back to them, which repairs both directions of the
freeze at once:

- A device frozen **online** becomes visible to the inactivity sweep again, and is marked offline ten
  minutes after its real last activity.
- A device frozen **offline** stops having its commands withheld. The hold is keyed on a device being
  asserted *and* not active, so an inferred device is dispatched to; the periodic pass that re-checks
  the withheld set releases the backlog within a couple of minutes, and the device itself reads online
  again on its next reading. This is the direction worth fixing promptly, because held commands count
  against a [per-tenant ceiling](../concepts/commands.md#held-command-ceiling) — devices wedged
  offline by a departed source can eventually refuse enqueues for the healthy devices beside them.

There are two ways it happens.

#### A source releases its own devices when it is switched off

When broker-asserted MQTT presence declines to start because it was **deliberately disabled** or
because the **NATS system-account credential is missing**, `event-sources` walks the devices it still
has asserted and releases them.

Those are two of the six reasons the tap can fail to start, and the line is deliberate: both are
configuration, so every replica of the instance reads the same values and reaches the same
conclusion — the release is the instance speaking rather than one replica guessing. A failed dial or
a failed subscription is that replica's own bad luck, its peers may be reading advisories perfectly
well, and releasing a fleet on that evidence would undo something that was never broken. All six set
`presence_tap_off{reason}` regardless, which is how you tell which one you have.

Three properties of the automatic release are worth knowing before relying on it:

- **A missing credential waits two minutes first.** A bring-up mints that credential in the same run
  that starts the services, so an absent value can simply be a race with the run that is creating it.
  A written `enabled: false` is unambiguous, and acts immediately.
- **It needs a gateway source and service-to-service configuration of its own** — something to emit
  under, and a way to enumerate tenants and read the projection. Without them it does not run at all.
  It logs that it did not, and points at the manual door, which is then the only one.
- **It is paced and self-emptying.** Releases go out at 25 devices a second, and a released device
  leaves the set being walked, so an interrupted pass resumes for free rather than starting again.
  `presence_still_asserted` is how much is left; a healthy release walks it to zero and leaves it
  there.

Nothing releases Sparkplug or LwM2M devices automatically. Those sources go away because an operator
removed them, not because a flag changed, so an operator is what releases them.

#### An operator releases them by hand

`dcctl presence demote` walks one source's asserted devices in a tenant and releases each:

```
dcctl presence demote --tenant acme --source sparkplug:plant-a \
  --reason "plant-a gateway decommissioned"
```

| Flag | |
|---|---|
| `--tenant` | Required. A demotion acts on one tenant. |
| `--source` | Required, and never inferred — the blast radius is an entire event source. Pass it exactly as the device's state reports it: the source's own configured id for MQTT and HTTP (`mqtt1`, `http1`), `sparkplug:{hostId}` for Sparkplug, `lwm2m` for LwM2M. |
| `--device` | Repeatable. Narrows to named devices *within* the source; omit it to release the whole source. |
| `--reason` | Required. Recorded with every event the run emits — the only record of a fleet-wide presence write. |
| `--page` | Devices per call, `200` by default. |
| `--dry-run` | Reports what would be released, and releases nothing. |

A source nobody uses is not an error — it simply matches nothing. So a first page that matches zero
devices is far more likely to be a mistyped `--source` than a finished job, and the command says so
rather than reporting success.

The same operation is available on the API as `device-state`'s `demoteAssertedPresence` mutation. It
requires the `state:demote` permission, which is a write, is not part of the read-only baseline every
member receives, and is granted to no role by default — give it explicitly to the role that needs it.

#### A release is metered like any other presence event

It passes the same per-tenant [ingest ceiling](../concepts/governance.md) as a connect or a
disconnect, so a tenant already at its ceiling can have its *repair* refused along with the churn
causing the pressure — counted in `presence_events_refused_total`. Nothing is lost: a refused release
leaves the device asserted, so the next pass finds it again. The repair simply arrives no sooner than
the ceiling allows.

## Tenancy on both transports

**Tenancy is fixed by the connection on both transports, and is never read from device-supplied
content.** This is the strongest property in this part of the platform and it holds on both paths.

- **Sparkplug.** Every message is attributed to the tenant configured for the **broker connection it
  arrived on**. The Sparkplug group id in the topic is a customer's own label — not globally unique
  and settable by any publisher — so it never names a tenant. The configuration refuses two tenants on
  one broker endpoint, because the group id would then be the only thing separating them.
- **LwM2M.** Every device is bound to its tenant by the **authenticated DTLS pre-shared-key identity**
  it presented at the handshake. The endpoint name the device asserts in its own registration payload
  is never used for identity. An unprovisioned identity fails the handshake, and the refusal does not
  echo the identity back, so a probe cannot enumerate valid credentials by comparing error responses.

:::caution On Sparkplug, device authentication is broker-level
Both transports mark their traffic as authenticated by the transport, which is what lets the platform
trust a device identity under `deviceAuthMode: required` without a second per-event credential. On
LwM2M that identity is bound to the authenticated PSK, so it is genuinely per-device.

**On Sparkplug it is derived from the topic, so the authentication is only as fine-grained as the
broker connection.** Turning on required device authentication does *not* stop one publisher on a
tenant's broker sending under a different device's identity **within that same tenant**. Cross-tenant
is closed on both paths — a publisher can never reach another tenant — but if intra-tenant device
identity matters to you, enforce it with per-client credentials and topic permissions **on your own
broker**, which is where that boundary actually lives.
:::

## LwM2M: what an operator must know

**Only SenML-JSON telemetry is decoded.** Notifications in any other content format are counted and
discarded. The practical consequence is not obvious from the standard:

:::warning A conformant LwM2M 1.0-only client gets presence and commands but no telemetry
SenML arrived in LwM2M 1.1. A device that only speaks 1.0 will register, hold its session, drive
presence correctly and accept Read/Write/Execute commands — and **never produce a single measurement**.
Nothing fails loudly; the readings simply never appear.

The metric to check is **`observe_establish_refused_total`**. DeviceChain asks for SenML-JSON on the
Observe itself, so a conformant 1.0-only client refuses the Observe with `4.06 Not Acceptable` and
then never sends a notification at all — which is counted here, and is this counter's dominant cause.
`notify_unknown_content_format_total` stays at **zero** for that device, because it counts the *other*
case: a device that does notify, in a content format this adapter cannot decode.
:::

**Observations are bounded and the bounds are not configurable.** DeviceChain establishes one
observation per object *instance*, only for objects inside a fixed IPSO range, and at most **32 per
registration**. The object allowlist is a **fixed property of the build — there is no setting that
adds to it.** If your fleet reports a resource outside that range, that resource will not be observed
and no configuration will change it. Watch `observation_overflow_total` for devices exceeding the
per-registration cap.

**Sessions are not reaped by default.** `idleTimeoutSeconds` defaults to `0`, meaning never — which is
correct for always-connected devices. A **queue-mode** fleet should set it comfortably above the
expected wake interval: too low and a sleeper's session keys are evicted out from under it, forcing
the full re-handshake that DTLS Connection ID exists to avoid.

:::danger Nothing exposes the LwM2M port outside the cluster
The device-facing CoAP/DTLS port is **UDP 5684**, and **neither the chart nor the infrastructure
modules expose it beyond the cluster.** Every service is cluster-internal, no session affinity is
configured anywhere, and the shipped ingress controller handles HTTP only.

External exposure is explicitly an operator decision, and there is **no shipped implementation of it**
— so a real LwM2M fleet cannot reach the service as installed. You must provide the UDP path yourself
(a `LoadBalancer` or `NodePort` service, or an external UDP proxy), and it must be a path that keeps
every datagram of a session going to the one serving pod.
:::

### LwM2M settings

| Setting | Default | What it does |
|---|---|---|
| `listen.port` | `5684` | The UDP port the CoAP/DTLS server binds. |
| `security.connectionIdLength` | `8` | DTLS Connection ID length in bytes. **Keep this non-zero** for cellular or roaming fleets — it is what lets a session survive an address change. `0` disables it, forcing a re-handshake on every rebind. |
| `security.idleTimeoutSeconds` | `0` | Reap a session with no traffic after this long. `0` never reaps. Set it above the wake interval for a queue-mode fleet. |
| `security.handshakeTimeoutSeconds` | `10` | Bounds one DTLS handshake, so a stalled one cannot pin resources. |
| `security.maxSessions` | `100000` | Ceiling on the live session table. A handshake past the ceiling is refused and counted, never silently admitted. |
| `maxLifetimeSeconds` | `86400` | The ceiling every registration lifetime is clamped down to. **This is the lever that bounds how long a dead device reads online.** Must stay above the longest lifetime your devices request. |
| `ingestRateLimit.messagesPerSecond` | `1000` | Per-tenant sustained ingest ceiling. Unset or non-positive falls back to this default, never to unlimited. |
| `ingestRateLimit.burst` | `2000` | Burst allowance for the above. |
| `downlink.timeoutSeconds` | `10` | Bounds one command exchange to a device. On expiry the command is reported failed rather than left hanging. Raise it for slow cellular sleepers. |
| `downlink.concurrency` | `16` | Cross-device command parallelism. A device's own commands always run in order regardless of this value. |

## Sparkplug: what an operator must know

**Each source is an independent outbound connection.** A source names one broker, one tenant, and the
groups to subscribe to. A broker that is unreachable is retried on its own backing-off loop —
**it degrades that one source, not the pod and not any other tenant's source.** Watch
`connect_failures_total` rather than pod health for this.

**Reconnection is deliberately handled by the platform rather than by the MQTT client library.** Every
reconnection opens a genuinely fresh session with a fresh timestamp, because Sparkplug requires the
host's birth and its death certificate to carry the same timestamp so an edge node can reject a
delayed death from a previous session. This is also why every replica shares one client id: the
broker's own duplicate-id takeover is what evicts a zombie host.

:::caution There is no per-tenant rate limit or shedding on the Sparkplug path
Unlike LwM2M and the standard device ingest paths, Sparkplug ingestion applies **no per-tenant ingest
ceiling and sheds nothing**. The reasoning is that its exposure is a broker you deliberately chose to
connect to, rather than an open endpoint — but the consequence is yours: a runaway edge node on a
configured broker is not throttled at the door. Bound it at the broker, or by the groups you subscribe
to.

The [tenant lifecycle gate](./tenant-deletion.md) does still apply — traffic for a deleting tenant is
refused on this path like any other, and counted in `tenant_deleted_dropped_total`.
:::

**Unknown identities are a choice.** With auto-registration on, a Sparkplug identity with no matching
device creates one. With it off, its telemetry is dropped and counted in `unknown_device_dropped_total`
— which is the metric to check when an edge node is publishing and nothing appears.

## The edge agent

The edge agent is **not a fourth ingest path**. It runs at a site, presents an ordinary MQTT endpoint
to local devices, buffers what they publish to local disk, and re-publishes it onto the same device
topics the cloud already ingests. Nothing on the platform side knows an agent was involved, which is
why there is no agent-shaped configuration anywhere in the cloud services.

**It is not a chart functional area.** It appears in no area list and no deployment profile, and it
cannot be enabled the way a service is. It ships as static binaries and a container image, and you
deploy it yourself — a systemd unit on a site gateway, a container, or a hand-written Kubernetes
manifest at the edge.

**The spool is a drop-oldest ring.** The local store is a durable on-disk buffer, `1 GiB` by default.
When it is full it drops the **oldest** un-forwarded events to admit new ones — never the newest. That
direction is deliberate: a device is acknowledged the moment it publishes, from the agent's own
persistence, so dropping the newest would discard exactly what the agent has just promised to keep and
would leave you with a stale buffer at the end of an outage instead of a current one. Every drop is
counted, as the spool's own first sequence minus the count of events this agent has forwarded and
acknowledged — the second operand is delivery bookkeeping, and it is *persisted*, which is what lets
the count survive a restart rather than resetting to zero. One case is not covered: when that
persisted count is missing — a first start, or a store whose progress file was removed — it is
seeded from the spool's current first sequence, so anything already evicted is treated as
accounted-for and a restart in that state does reset the evidence. Keep the store directory intact
across restarts if the drop count matters to you.

:::caution Duplicate collapse on reconnect covers JSON payloads only
When the uplink returns, the agent re-forwards everything it buffered. For **JSON object payloads** it
stamps a replay-stable identity and event time, so a message that was already delivered folds into the
existing one at the cloud's uniqueness check and you see it once.

**Any other payload shape is forwarded verbatim and is at-least-once.** A reconnect after a flaky link
can genuinely deliver those twice. If you use a non-JSON decoder behind an edge agent, make what you
do with the readings tolerant of a repeat.
:::

Two more things worth knowing before you deploy one:

- **The local MQTT listener is open unless you configure a credential.** Set `local.username` and
  `local.passwordEnv` to require one. Leaving it open is a valid trusted-LAN posture and the agent
  announces it with a loud warning at startup so the choice stays visible — but it is a network-access
  control either way, not per-device identity, and over plaintext MQTT the secret crosses the LAN in
  the clear.
- **The metrics and health endpoint binds to loopback only.** The device MQTT port is the agent's only
  LAN-exposed surface, by design. To scrape the agent from elsewhere you need something on the box
  itself to relay it.

### Edge agent settings

| Setting | Default | What it does |
|---|---|---|
| `instanceId` | — | The cloud instance this agent forwards into. Publishes seen for a different instance are not forwarded, and are counted in `instance_mismatched_total`. |
| `agentId` | — | This agent's identity on the uplink. **Must be unique** — two agents sharing it disconnect each other in a loop. |
| `local.listenPort` | `1883` | The MQTT port site devices connect to. |
| `local.storeDir` | — | Required. The directory holding the durable spool. One agent per directory. |
| `local.spoolMaxBytes` | `1 GiB` | Spool budget. Beyond it, oldest events are dropped. The floor is 16 MiB. |
| `local.metricsPort` | `9090` | Loopback-only metrics and health port. An explicit `0` disables the endpoint. |
| `uplink.brokerUrl` | — | Required. The cloud MQTT endpoint to forward to. |
| `uplink.connectTimeoutSeconds` | `30` | Bounds one uplink connection attempt. |
| `uplink.backoffMinSeconds` / `uplink.backoffMaxSeconds` | `1` / `60` | Reconnection backoff bounds while the link is down. |

## What to watch

:::danger No alerts and no dashboards ship for any of these
The shipped alert rules and the shipped Grafana dashboard cover the detection engine, the databases
and replication. **Not one edge metric has an alert or a dashboard panel.** Everything in the tables
below is emitted and scraped; nothing will page you about any of it until you write the rule yourself.

**The first alert to author is a no-leader alert**, on each ingest service:

> `sum(devicechain_lwm2mingest_is_leader) != 1`
>
> `sum(devicechain_sparkplugingest_is_leader) != 1 or absent(devicechain_sparkplugingest_is_leader)`

Zero means nobody is serving that transport and every device on it is silently unreachable. Anything
other than one is worth waking someone. It is the most load-bearing signal on this whole surface and it
is the one nothing tells you about today.

The `absent()` half is not decoration. The Sparkplug gauge is only registered once at least one
source is configured, so a pod running with its sources unset publishes the series **not at all** —
and `!= 1` over an empty result is itself empty, which is a silent alert, not a firing one. That is
exactly the case the alert exists for. LwM2M registers its gauge unconditionally, so only the
Sparkplug expression needs the pairing.
:::

All metrics carry the `devicechain_` prefix and their service's own segment —
`devicechain_sparkplugingest_`, `devicechain_lwm2mingest_`, `devicechain_edge_`. None of them is
labelled per device or per tenant, so none of them is a cardinality risk to scrape.

**Sparkplug ingestion** (`devicechain_sparkplugingest_`):

| Signal | Means |
|---|---|
| `is_leader` | 1 on the serving pod, 0 elsewhere. **Alert on the sum not being 1.** |
| `connect_failures_total` | A configured broker is not reachable. Rising means one source is down while the pod looks healthy. |
| `messages_total` | Inbound Sparkplug traffic. A flat line on a live fleet is the symptom of a lost subscription or a dead source. |
| `presence_emitted_total` | Connect/disconnect signals produced. |
| `rebirth_requests_total` | Nodes being asked to re-announce. Steadily rising means a node is failing to resynchronise. |
| `unknown_device_dropped_total` | Traffic from identities with no device, with auto-registration off. |
| `decode_errors_total` / `ingest_failures_total` | Malformed payloads, and failures publishing onward. |
| `tenant_deleted_dropped_total` | Traffic refused because its tenant is being deleted. |

**LwM2M ingestion** (`devicechain_lwm2mingest_`):

| Signal | Means |
|---|---|
| `is_leader` | As above. **Alert on the sum not being 1.** |
| `active_registrations` / `active_sessions` / `active_observations` | The live fleet as the service sees it. **Watch `active_observations` across a restart** — it is how you see the observation loss described above, and how you see it recover. |
| `registrations_total` / `registration_updates_total` | Devices arriving and keeping their sessions alive. |
| `registration_expiries_total` | Registrations that lapsed rather than deregistering — devices that vanished. |
| `handshake_failures_total` / `auth_errors_total` | Devices failing DTLS, and identities that are not provisioned. |
| `observe_establish_refused_total` | An Observe the device refused or that otherwise failed. **The 1.0-only client symptom** — such a client answers the SenML Observe with `4.06` and never notifies. |
| `notify_unknown_content_format_total` | Telemetry arriving in a format that is not decoded — a device that *does* notify, undecodably. Zero for a 1.0-only client. |
| `notify_decode_failures_total` / `notify_samples_truncated_total` | Malformed or oversized payloads. |
| `observation_overflow_total` | A registration exceeding the 32-observation cap. Some of its resources are not observed. |
| `ingest_messages_shed_total` / `ingest_samples_shed_total` | A tenant over its ingest ceiling. |
| `shadows_reconstructed_total` | Presence rebuilt after a leadership change. A spike is the fingerprint of a failover. |
| `commands_failed_total` / `commands_not_served_total` | Downlink commands that did not land. |

**Edge agent** (`devicechain_edge_`):

| Signal | Means |
|---|---|
| `uplink_connected` | 0 means the site is buffering. |
| `spool_oldest_age_seconds` | **The primary backlog signal.** How far behind the agent is, in wall-clock terms. |
| `spool_used_bytes` / `spool_limit_bytes` | How close the spool is to dropping. |
| `dropped_total` | **Data has been lost.** Oldest events evicted to make room. Any increase is real loss. |
| `forward_errors_total` | Forward attempts that failed; the event stays buffered for redelivery. |
| `received_total` / `forwarded_total` | Throughput in and out. |
| `malformed_total` | Events discarded as unforwardable rather than blocking the queue behind them. |
| `local_auth_enabled` | 0 means the site's MQTT listener requires no credential. |

## What is not validated

Two honest limits, so you can weigh them:

- **No shipped rig exercises a real Sparkplug fleet.** Nothing in the project drives a third-party
  edge node or broker end to end. Sparkplug behaviour is covered by tests against the platform's own
  implementation.
- **The LwM2M path is the better-validated of the two.** It is exercised against an independent
  third-party LwM2M client stack, reading its verdicts from the server side so a client that
  misbehaves cannot manufacture a pass. That suite runs on a schedule and is advisory rather than a
  release gate.

The edge agent is covered by its own tests and has no in-cluster deployment validation. Treat a first
agent rollout as something to pilot at one site before it is a fleet.
