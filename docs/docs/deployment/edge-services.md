---
sidebar_position: 9
title: Running the Edge Services
---

# Running the Edge Services

Three DeviceChain components sit at the edge of the platform, and none of them behaves like the
stateless services around them:

- [Sparkplug-B ingestion](../concepts/sparkplug.md) and [LwM2M ingestion](../concepts/lwm2m.md) are
  **presence-asserting transports**. They are *told* when a device connects and disconnects, rather
  than guessing it from silence.
- The **edge agent** is a separate binary that runs on a box at a site. It buffers locally when the
  link to the cloud is down, and forwards when the link comes back.

All three run as a single instance, and all three hold live state that no database holds. What a
restart costs differs for each. This page covers how many run and why, what a failover loses, what
presence does and does not guarantee, and what to watch.

For what each protocol does or how a device maps onto the platform, start at
[Sparkplug-B](../concepts/sparkplug.md), [LwM2M](../concepts/lwm2m.md) or
[Device Presence](../concepts/device-presence.md) instead.

Both ingest services are **opt-in**. Neither is in the default set of functional areas, so you enable
them deliberately. Each hard-depends on device management, which resolves what they produce.

## Why each runs as one instance {#one-instance-each-and-the-reasons-differ}

Each component runs as one instance for its own reason; this is not one policy applied three times.

| Service | Why exactly one | What a second one would do |
|---|---|---|
| **Sparkplug ingestion** | It joins your broker as a Sparkplug **Host Application**, and a Sparkplug environment has one of those. | Publish conflicting host STATE and ingest every message twice. |
| **LwM2M ingestion** | DTLS is a **stateful session over one bound UDP socket**. | A standby that also bound the socket would silently receive — and drop — the share of datagrams sent its way. Traffic disappears rather than failing loudly. |
| **Edge agent** | It owns a local spool directory and one identity on the cloud uplink. | Two on one directory collide on file locks; two sharing an identity kick each other off the uplink in a loop. |

For the two ingest services, the platform enforces this rather than only documenting it:

- **Ownership lease.** Each service takes a lease with a 30-second window. A replacement pod connects
  nothing and binds nothing until it holds the lease.
- **Bounded overlap.** The window in which two pods serve is bounded by the lease window plus one
  renewal interval (about ten seconds). A leader that has lost the lease evicts itself only when its
  next renewal notices, and then still has to unwind its broker or DTLS state. The overlap is
  bounded, not eliminated.
- **No write fencing.** Nothing fences a stale leader's writes on these two paths. The lease's epoch
  is carried, but no ingest path rejects on it.
- **Chart refusal.** The chart refuses to render either area at more than one replica. The refusal
  is keyed on the area itself, not on its rollout strategy, so overriding `strategy` to
  `RollingUpdate` does not get past it.

A pod that ends *itself* releases the lease on its way out, so the replacement acquires it as soon as
it starts. A pod ends itself when it decides it can no longer serve; on LwM2M ingestion that means a
leadership term it could not build, or a CoAP/DTLS transport that stopped reading. The 30-second wait
applies only after an **abrupt** loss, where nothing had the chance to release: a node failure, a
`SIGKILL`, an out-of-memory kill.

:::warning Neither ingest service gets a pod disruption budget
The chart skips a disruption budget for any single-replica area, because a budget demanding one
available pod would block a node drain outright. Draining the node an edge service is on therefore
**stops that transport** until the pod is rescheduled and takes the lease. Recovery is automatic but
not instant, so prefer a deliberate rollout over draining that node.
:::

## What a failover costs

A restart or a leadership handover is routine on both services, and both come back on their own. They
do not recover the same things.

| | Sparkplug ingestion | LwM2M ingestion |
|---|---|---|
| **Presence** | **Reconstructed.** The new leader re-establishes the session, asks edge nodes to re-announce themselves, and reconciles which devices are actually live — so a disconnect that happened during the changeover is not missed. | **Reconstructed** from the stored projection and each device's registration lifetime, rather than by probing, so a sleeping queue-mode device is not false-flagged offline. |
| **Telemetry during the window** | Lost. A broker does not hold DATA for a host that is not connected. | Datagrams sent during the window are lost; confirmable CoAP messages are retransmitted by the device. |
| **Observed resources** | Not applicable. | **Not re-established. See below.** |
| **Recovery time** | However long the replacement pod needs to schedule and start, plus up to the 30-second lease window. | The same, plus binding the socket. |

:::danger LwM2M observations are lost on failover and nothing re-creates them
A restart, a rollout or a leadership handover loses every LwM2M observation. Presence comes back;
telemetry does not, until each device re-registers. With the shipped default that is **up to a day
of silence from a healthy device** that reads online the whole time. See
[Lost observations](#lost-observations).
:::

### Lost observations {#lost-observations}

This is the most surprising operational fact on this page.

DeviceChain asks an LwM2M device to **Observe** its resources, so the device pushes readings on its
own. Those observations live only for the life of the process that established them. A restart, a
rollout or a leadership handover **loses every one of them, and nothing re-creates them.**

Presence comes back. Telemetry does not. A device starts reporting again only when it next
**re-registers**. That is the device's own behaviour on its own schedule, bounded by nothing except
its registration lifetime. With the shipped default of `86400` seconds, that is **up to a day of
silence from a perfectly healthy device**, with the device online in the console the whole time.

If you cannot tolerate that, shorten the registration lifetime your devices request, at the cost of
more frequent registration updates. Lowering `maxLifetimeSeconds` does **not** shorten this
blackout: the device is never told about the clamp and keeps updating on its own schedule. That
setting bounds how long a dead device reads online (see [LwM2M settings](#lwm2m-settings)), and it
must stay above the longest lifetime your devices actually request, or they are expired as dead on
every handover.

## What presence guarantees

Every device carries a **presence source**, `INFERRED` or `ASSERTED`. The rules around it are
narrower than they look.

**Only the source can give a device back.** A device becomes asserted the first time an authoritative
transport speaks for it. No timeout, no data event and no amount of silence moves it back to inferred.
The one thing that does is a **demotion**: a claim by the *source* that it is no longer speaking for
the device, never a claim about the device itself. See [returning a device to inferred
presence](#demoting-a-device). A device that used to arrive over Sparkplug or LwM2M and now arrives
over plain MQTT keeps its asserted source, and stays exempt from the inactivity sweep.

**Ordering is by a platform-minted session identity, never by anything the device sends.** The
platform stamps each connect/disconnect pair with a session marker it generates. A Sparkplug
birth/death sequence number is read only to match a death to the birth it belongs to. It is never
compared by magnitude, because it wraps. An LwM2M registration id is never used as the session
identity. As a result, a delayed or replayed message from an older session cannot tear down a live
one, on either transport.

Markers are minted from the clock of the broker node that accepted the connection. Across a
multi-node cluster, a new session's marker is therefore **not** guaranteed to sort above the one
before it: a node whose clock trails its peers mints a lower one. The platform reconciles that case
rather than assuming it away. A device found live on a session that sorts below its stored one is
re-filed onto the session it is actually on, so its later disconnect is still recognised.

**A device cannot assert its own presence.** A connect/disconnect event submitted through the
ordinary device-facing payload path is rejected outright, not merely ignored. Only the transports
themselves produce them. An asserted device is exempt from the inactivity sweep, so a device able to
declare itself connected could pin itself online permanently.

**The inferred timeout is ten minutes and is not adjustable.** Devices without an asserting transport
are swept offline after ten minutes of silence, re-checked every minute. There is no per-device
override and no setting for it today. It is also the timeout a demoted device comes back under, which
is most of the point of demoting one.

## Devices stuck online {#a-device-that-reads-online-and-is-not}

Understand this failure mode before you rely on presence for anything that pages a human.

**An asserted device that dies without saying so is not caught by a timeout.** The inactivity sweep
deliberately skips asserted devices, because on an asserting transport silence is not evidence of
death. What catches such a device instead differs by transport, and each has a bound you can set:

1. **A new signal from the device's own transport, or a question it fails to answer.** Devices
   asserted by DeviceChain's own MQTT broker get one without the device doing anything: a repair pass
   periodically compares the broker's live connection list with what the platform believes, and
   corrects the difference. See [Device Presence](../concepts/device-presence.md). Sparkplug devices
   get one too: a connected host periodically asks every node with an asserted device that has gone
   quiet to re-announce itself, and marks a device that does not answer offline. LwM2M devices are
   marked offline when their registration lifetime lapses without a refresh.
2. **A [demotion](#demoting-a-device).** This is the answer when the first will never arrive, because
   the source that would have to produce it is gone. It works on all three asserting transports.

How each transport bounds a death it was never told about:

- **A lost Sparkplug death certificate.** If the broker never delivers the node's DEATH, the device
  stays online until the next liveness probe. A probe runs when the host connects to the broker, and
  then every `presence.probeSeconds` (thirty minutes by default) for as long as the session lasts. Each
  periodic probe asks the node of every asserted device that has produced nothing within the last
  interval to rebirth, waits `presence.probeWindowSeconds` (ten seconds by default), and marks a
  device that still produced nothing offline. A
  device heard from inside the interval is never asked and never judged, and any message from a
  device also counts for its node, so devices that report more often than the interval cost nothing.
  A silent death is therefore noticed within about two intervals (about an hour at the default), on
  evidence (asked, no answer) rather than on silence. Watch
  `presence_probes_total{trigger="periodic"}`: on a connected host it must rise; flat means the probe
  is not running.

  **The probe has a cost on a quiet fleet.** Sparkplug reports by exception, so a healthy device can
  legitimately say nothing for hours. Its node is then rebirthed every other interval: the node
  re-sends every metric value it has, for itself and all its devices, which is stored as readings
  (births are not charged against the tenant's ingest limit), and each device records a fresh connect.
  The nodes are not all asked at once: the interval is cut into slots at least two answer windows
  long, and each node is always asked in the same slot, picked from its group and node id, so the
  rebirths of a quiet fleet arrive spread across the interval. Each slot still waits its own full
  window for its own nodes before the next slot starts. If the cost is still too much for your fleet,
  raise `presence.probeSeconds`; the detection bound grows with it. A node that takes longer than the
  answer window to re-announce a device has that device marked offline until its birth arrives, and
  it comes back online on its own when it does; raise `presence.probeWindowSeconds` for large nodes
  on slow links.
- **A node re-announcing itself.** When an edge node births a new session, its previous session's
  child devices are replaced along with it. A child device that does not re-announce under the new
  session keeps showing connected until the next periodic probe, which asks its node to rebirth and
  marks it offline when it still does not appear.
- **A long LwM2M registration.** A device that vanishes is marked offline when its registration
  lifetime lapses. With the default, that is **86400 seconds**, one full day. `maxLifetimeSeconds`
  clamps every registration's lifetime down to at most that value, so it directly bounds how long a
  dead LwM2M device can read online; setting it to, say, 3600 caps that at an hour. It must stay above
  the longest lifetime your fleet actually asks for.
- **A removed asserting transport.** Decommission the Sparkplug source or the LwM2M credential a
  device arrived on, and nothing will ever produce another signal for it, and no probe runs for a
  source that is no longer configured. It is stranded at its last asserted state until someone
  [releases it](#demoting-a-device). That is what the operation is for, since a source that is gone
  will not tell the platform anything more.

The three bounds, side by side: `brokerPresence.reconcileSeconds` for the platform broker,
`presence.probeSeconds` for Sparkplug, and `maxLifetimeSeconds` for LwM2M. The first defaults to five
minutes, the second to thirty, because asking a Sparkplug node costs a full rebirth; the third to a
day, because it is the device's own registration lifetime, not a
platform setting. If a device reading online is operationally load-bearing for you, you can still pair
the connectivity signal with a timeout-based [absence rule](../concepts/event-processing.md), which
fires on silence regardless of what presence says.

### Returning a device to inferred presence {#demoting-a-device}

A **demotion** is the only transition from `ASSERTED` back to `INFERRED`. It is a claim by the
source, not about the device: the source is releasing custody. It asserts nothing about connectivity.
Whether the device reads online, when it last connected, when it last disconnected and when it last
reported are all left exactly as they were.

What changes is who may correct them. An asserted device suppresses both of the platform's repair
mechanisms. Releasing it hands the device back to them, which repairs both directions of the freeze
at once:

- **Frozen online.** The device becomes visible to the inactivity sweep again, and is marked offline
  ten minutes after its real last activity.
- **Frozen offline.** The device stops having its commands withheld. The hold is keyed on a device
  being asserted *and* not active, so an inferred device is dispatched to. The periodic pass that
  re-checks the withheld set releases the backlog within a couple of minutes, and the device reads
  online again on its next reading. Fix this direction promptly: held commands count against a
  [per-tenant ceiling](../concepts/commands.md#held-command-ceiling), so devices wedged offline by a
  departed source can eventually refuse enqueues for the healthy devices beside them.

A demotion happens in one of two ways: a source releases its own devices, or an operator releases them
by hand.

#### Automatic release when a source is switched off {#a-source-releases-its-own-devices-when-it-is-switched-off}

Broker-asserted MQTT presence depends on a *presence tap*: a connection `event-sources` makes to the
broker's system account to hear its connect and disconnect notices. When the tap declines to start
for one of these reasons, `event-sources` walks the devices it still has asserted and releases them:

- it was **deliberately disabled**;
- the **NATS system-account credential is missing**;
- **the broker cannot be reached**.

These are three of the six reasons the tap can fail to start, and the line is deliberate. The first
two are configuration. Every replica of the instance reads the same values and reaches the same
conclusion, so the release is the instance speaking rather than one replica guessing.

The third is evidence of a different kind. The tap gives its connection thirty seconds to reach the
broker before reporting it unreachable, so this reason means half a minute with no system-account
connection, not one failed attempt: a broker that is down, or a credential it refuses. The MQTT
gateway lives in that same broker, so while it is unreachable no device is connected through it
either. The release is not a guess about the fleet; it is the only reading consistent with the broker
being gone.

It is also the only one of the three whose truth can change while the pod runs, so it is the only one
that keeps asking. Every release pass re-dials the system account first, the first pass included.
**If the broker answers, nothing is released**: the service exits and the pod restarts. The
replacement dials the broker normally and runs its tap. A returning broker therefore shows up as a pod
restart, not as a fleet of released devices. Without that re-check, the release would continue: the
pass walks whatever is asserted *now* on the reconcile interval. With peers still asserting, the two
would take turns on every row indefinitely, and in the gap between them the inactivity sweep would
mark connected-but-quiet devices offline.

These do **not** release:

- a failed subscription on a connection that did reach the broker. That is this replica's own bad
  luck; its peers may be reading advisories perfectly well.
- the two reasons that mean this instance has no tap to run at all: no source pointed at the platform
  broker, and no service-to-service configuration.

All six reasons set `presence_tap_off{reason}` regardless, which is how you tell which one you have.

Once the tap is running, a connection the broker closes for good is a reason to restart, not to turn
the tap off. If the broker stops accepting the system-account credential, every `event-sources` pod
fails its liveness check at about the same time and Kubernetes restarts them. HTTP ingest is
unavailable while they restart, and MQTT telemetry waits in the broker. Each restarted pod dials with
its mounted credential. If the broker still refuses it, the tap turns off with reason
`broker_unreachable` and the release above applies.

Know three properties of the automatic release before relying on it:

- **A missing credential and an unreachable broker wait two minutes first.** A bring-up mints that
  credential and rolls the broker in the same run that starts the services, so either can be a race
  with the run rather than a standing condition. A written `enabled: false` is unambiguous, and acts
  immediately. For the broker, the wait is also a **re-check** (see above). For the credential it
  cannot be, because configuration is read once at startup and a change rolls the pod.
- **It needs a gateway source and service-to-service configuration of its own**: something to emit
  under, and a way to enumerate tenants and read the projection. Without them it does not run at all.
  It logs that it did not, and points at the manual release, which is then the only way.
- **It is paced and self-emptying.** Releases go out at 25 devices a second, and a released device
  leaves the set being walked, so an interrupted pass resumes for free rather than starting again.
  `presence_still_asserted` is how much is left; a healthy release walks it to zero and leaves it
  there.

Nothing releases Sparkplug or LwM2M devices automatically. Those sources go away because an operator
removed them, not because a flag changed, so an operator releases them.

#### Manual release {#an-operator-releases-them-by-hand}

`dcctl presence demote` walks one source's asserted devices in a tenant and releases each:

```bash
dcctl presence demote --tenant acme --source sparkplug:plant-a \
  --email ops@acme.example --password "$DC_PASSWORD" \
  --reason "plant-a gateway decommissioned"
```

| Flag | |
|---|---|
| `--tenant` | Required. A demotion acts on one tenant. |
| `--email` / `--password` | Required. The identity the demotion is authorized as. It must be a member of the tenant, or a superuser. |
| `--server` | The instance host for API calls, `localhost` by default. Add `--tls` for HTTPS. |
| `--source` | Required, and never inferred — the blast radius is an entire event source. Pass it exactly as the device's state reports it: the source's own configured id for MQTT and HTTP (`mqtt1`, `http1`), `sparkplug:{hostId}` for Sparkplug, `lwm2m` for LwM2M. |
| `--device` | Repeatable. Narrows to named devices *within* the source; omit it to release the whole source. |
| `--reason` | Required. Recorded with every event the run emits — the only record of a fleet-wide presence write. |
| `--page` | Devices per call, `200` by default. |
| `--dry-run` | Reports what would be released, and releases nothing. |
| `--yes` | Skips the confirmation prompt a real run asks for. Without a terminal to confirm on, a real run is refused unless you pass it. |

A source nobody uses is not an error; it matches nothing. A first page that matches zero devices is
therefore far more likely to be a mistyped `--source` than a finished job, and the command says so
rather than reporting success.

The same operation is available on the API as `device-state`'s `demoteAssertedPresence` mutation. It
requires the `state:demote` permission. That permission is a write, and is not part of the read-only
baseline every member receives. The seeded `tenant-admin` role holds every tenant permission, so it
includes this one; give it explicitly to any other role that needs it.

#### Release metering {#a-release-is-metered-like-any-other-presence-event}

A release passes the same per-tenant [ingest ceiling](../concepts/governance.md) as a connect or a
disconnect. A tenant already at its ceiling can have its *repair* refused along with the churn causing
the pressure, counted in `presence_events_refused_total`. Nothing is lost: a refused release leaves
the device asserted, so the next pass finds it again. The repair arrives no sooner than the ceiling
allows.

## Tenancy on both transports

**Tenancy is fixed by the connection on both transports, and is never read from device-supplied
content.** This is the strongest property in this part of the platform.

- **Sparkplug.** Every message is attributed to the tenant configured for the **broker connection it
  arrived on**. The Sparkplug group id in the topic is a customer's own label, not globally unique and
  settable by any publisher, so it never names a tenant. The configuration refuses two tenants on one
  broker endpoint, because the group id would then be the only thing separating them.
- **LwM2M.** Every device is bound to its tenant by the **authenticated DTLS pre-shared-key identity**
  it presented at the handshake. The endpoint name the device asserts in its own registration payload
  is never used for identity. An unprovisioned identity fails the handshake, and the refusal does not
  echo the identity back, so a probe cannot enumerate valid credentials by comparing error responses.

:::caution On Sparkplug, device authentication is broker-level
Under `deviceAuthMode: required`, both transports are trusted for device identity without a second
per-event credential. On LwM2M that identity is bound to the PSK, so it is per-device. On Sparkplug
it comes from the topic, so required device authentication does *not* stop one publisher sending as
another device **within the same tenant**. See [Device authentication on
Sparkplug](#device-authentication-on-sparkplug).
:::

### Device authentication on Sparkplug {#device-authentication-on-sparkplug}

Both transports mark their traffic as authenticated by the transport. That is what lets the platform
trust a device identity under `deviceAuthMode: required` without a second per-event credential. On
LwM2M that identity is bound to the authenticated PSK, so it is genuinely per-device.

On Sparkplug the identity is derived from the topic, so the authentication is only as fine-grained
as **the broker connection**. Turning on required device authentication does *not* stop one publisher
on a tenant's broker sending under a different device's identity within that same tenant.
Cross-tenant is closed on both paths: a publisher can never reach another tenant. If intra-tenant
device identity matters to you, enforce it with per-client credentials and topic permissions **on your
own broker**, which is where that boundary actually lives.

### Device identifiers on edge transports {#device-identifiers}

:::danger None of the three identifiers is the one you typed
Every device on an edge transport carries three identifiers with three different jobs, and the
failures from confusing them are silent or misdirected. An auto-provisioned device arrives with **no
name**, and the console cannot search for it. Read this section before provisioning.
:::

Two of the identifiers look like names and the third is generated, which is why they are easy to
confuse.

| Identifier | Its job | Sparkplug | LwM2M |
|---|---|---|---|
| **1. Tenancy anchor** | Decides which tenant the data belongs to. Never anything in the message; covered above. | The **broker connection**. | The **authenticated PSK identity**. |
| **2. Device-resolution key** | Decides *which device*. This is the device's **external id**, and the device does not choose it. | The topic's `group/node[/device]` string, e.g. `plant-a/line-3/press-1`. | The external id you wrote **beside the PSK identity in the service configuration** — not the endpoint name (`ep`) the device sends. |
| **3. Device token** | What the console, the API and every event actually use. It is **generated**, not chosen. | `sp-…`, e.g. `sp-plant-a-line-3-press-1-9f2c1a8b4d3e`. | `lw-…`. |

On LwM2M, `ep` is logged and otherwise ignored. A device whose firmware sends `ep=urn:imei:35…` will
never be matched by it, and nothing will say so.

The token is generated because the external id routinely contains `/`, `.`, spaces or non-ASCII, none
of which a token may hold. So `plant-a/line-3/press-1` becomes something like
`sp-plant-a-line-3-press-1-9f2c1a8b4d3e`. The suffix disambiguates two external ids that would
otherwise reduce to the same string.

The practical consequence is worse than it sounds: an auto-provisioned device arrives with **no name
at all**. The registration carries only the token, the external id and the device type. The console's
device list therefore shows the device under its generated `sp-…` / `lw-…` token, with `—` in the Name
column. There is nothing there to recognise it by.

You also cannot search for it. The console's device list has **no search box**. It is a plain paged
listing of Status, Token, Name, Type, Description and Created, with no external-id column. The API's
device search takes only a page number, a page size and a device type. To find the device:

- **In the console**, find it by its token. The generated token embeds the external id
  (`plant-a/line-3/press-1` → `sp-plant-a-line-3-press-1-…`), so paging the list and reading the
  token column is the whole technique.
- **Over the API**, use `devicesByExternalId`, which takes exact external ids and returns the
  devices. It is an exact-match lookup, not a search: no prefixes, no substrings. Nothing in the
  console calls it, so this is an API-only route.

When a device does not appear at all, check identifier 2 before you suspect the transport. On LwM2M in
particular, a wrong PSK identity fails at the DTLS handshake, before registration, and the refusal
deliberately tells you nothing.

## LwM2M operations {#lwm2m-what-an-operator-must-know}

**Only SenML-JSON telemetry is decoded.** Notifications in any other content format are counted and
discarded. The practical consequence is not obvious from the standard:

:::warning A conformant LwM2M 1.0-only client gets presence and commands but no telemetry
SenML arrived in LwM2M 1.1. A 1.0-only device registers, holds its session, drives presence and
accepts Read/Write/Execute commands — and **never produces a single measurement**. Nothing fails
loudly. Check **`observe_establish_refused_total`**, not
`notify_unknown_content_format_total`. See [LwM2M 1.0-only clients](#lwm2m-10-only-clients).
:::

### LwM2M 1.0-only clients {#lwm2m-10-only-clients}

A device that only speaks LwM2M 1.0 will register, hold its session, drive presence correctly and
accept Read/Write/Execute commands, and never produce a single measurement. Nothing fails loudly; the
readings never appear.

The metric to check is **`observe_establish_refused_total`**. DeviceChain asks for SenML-JSON on the
Observe itself, so a conformant 1.0-only client refuses the Observe with `4.06 Not Acceptable` and
then never sends a notification at all. That refusal is counted here, and is this counter's dominant
cause. `notify_unknown_content_format_total` stays at **zero** for that device, because it counts the
*other* case: a device that does notify, in a content format this adapter cannot decode.

### Observation limits {#observation-limits}

**Observations are bounded, and the bounds are not configurable.** DeviceChain establishes one
observation per object *instance*, only for objects inside a fixed IPSO range, and at most **32 per
registration**. The object allowlist is a **fixed property of the build; no setting adds to it.** If
your fleet reports a resource outside that range, that resource will not be observed and no
configuration will change it. Watch `observation_overflow_total` for devices exceeding the
per-registration cap.

### Session reaping {#session-reaping}

**Sessions are not reaped by default.** `idleTimeoutSeconds` defaults to `0`, meaning never, which is
correct for always-connected devices. For a **queue-mode** fleet, set it comfortably above the
expected wake interval. Too low, and a sleeper's session keys are evicted out from under it, forcing
the full re-handshake that DTLS Connection ID exists to avoid.

### Exposing the LwM2M port {#exposing-the-lwm2m-port}

:::danger Nothing exposes the LwM2M port outside the cluster
The device-facing CoAP/DTLS port is **UDP 5684**, and **neither the chart nor the infrastructure
modules expose it beyond the cluster.** As installed, a real LwM2M fleet cannot reach the service.
You must provide the UDP path yourself.
:::

Every service is cluster-internal, no session affinity is configured anywhere, and the shipped
ingress controller handles HTTP only.

External exposure is explicitly an operator decision, and there is **no shipped implementation of
it**. Provide the UDP path yourself (a `LoadBalancer` or `NodePort` service, or an external UDP
proxy). It must be a path that keeps every datagram of a session going to the one serving pod.

### LwM2M settings {#lwm2m-settings}

| Setting | Default | What it does |
|---|---|---|
| `listen.port` | `5684` | The UDP port the CoAP/DTLS server binds. |
| `security.connectionIdLength` | `8` | DTLS Connection ID length in bytes. **Keep this non-zero** for cellular or roaming fleets — it is what lets a session survive an address change. `0` disables it, forcing a re-handshake on every rebind. |
| `security.idleTimeoutSeconds` | `0` | Reap a session with no traffic after this long. `0` never reaps. Set it above the wake interval for a queue-mode fleet. |
| `security.handshakeTimeoutSeconds` | `10` | Bounds one DTLS handshake, so a stalled one cannot pin resources. |
| `security.maxSessions` | `100000` | Ceiling on the live session table. A handshake past the ceiling is refused and counted, never silently admitted. |
| `maxLifetimeSeconds` | `86400` | The ceiling every registration lifetime is clamped down to. **This is the lever that bounds how long a dead device reads online.** Must stay above the longest lifetime your devices request. |
| `ingestRateLimit.readingsPerSecond` | `1000` | Per-tenant sustained ingest ceiling, counted in readings (decoded samples), and in Notify messages before decode. Unset or non-positive falls back to this default, never to unlimited. |
| `ingestRateLimit.burst` | `2000` | Burst allowance for the above. |
| `downlink.timeoutSeconds` | `10` | Bounds one command exchange to a device. On expiry the command is reported failed rather than left hanging. Raise it for slow cellular sleepers. |
| `downlink.concurrency` | `16` | Cross-device command parallelism. A device's own commands always run in order regardless of this value. |

## Sparkplug operations {#sparkplug-what-an-operator-must-know}

**Each source is an independent outbound connection.** A source names one broker, one tenant, and the
groups to subscribe to. An unreachable broker is retried on its own backing-off loop. It degrades
**that one source**, not the pod and not any other tenant's source. Watch `connect_failures_total`
rather than pod health for this.

**One refused group stops that whole source until the broker's ACL is fixed.** A source announces a
single online/offline state for all of its groups, so it cannot be online for some and offline for
others. The broker may accept the connection but refuse the subscription to one group, most often
because the source's credential may not read it. The source then:

1. does not announce itself online;
2. ingests none of its groups;
3. disconnects, and retries on the same backing-off loop, up to 30 seconds apart.

Announcing online with a group missing would be worse. That group's edge nodes would flush their
buffered data into a subscription that does not exist, and the source would then mark their devices
disconnected for staying silent. Before it disconnects, the source publishes its offline state itself,
because a clean disconnect does not trigger its Last Will. An online announcement the broker stored
but never acknowledged is therefore replaced rather than left standing. Watch
**`subscribe_failures_total`**: any increase means a source is down, and the log line names the group
that was refused.

**The platform handles reconnection, not the MQTT client library.** Every reconnection opens a
genuinely fresh session with a fresh timestamp. Sparkplug requires the host's birth and its death
certificate to carry the same timestamp, so an edge node can reject a delayed death from a previous
session. This is also why every replica shares one client id: the broker's own duplicate-id takeover
is what evicts a zombie host.

:::caution The Sparkplug path does not bound the message rate or a message's total size
Sparkplug charges each DATA message's readings against the tenant's ingest ceiling, and drops what is
over it. It does not meter the message rate, births or deaths, and a message's total size is bounded
only by the broker. See [What Sparkplug ingest bounds](#unbounded-sparkplug-ingest).
:::

### What Sparkplug ingest bounds {#unbounded-sparkplug-ingest}

**Readings are charged against the tenant's ingest ceiling.** Every numeric metric in an NDATA or
DDATA message is one reading, charged against the same per-tenant ceiling, counted in readings, that
every other device transport uses (see [what the default allows](../concepts/governance.md#ingest-default)).
It is charged once per message, before the message is stored, and an in-handler retry is not charged
again. Readings over the ceiling are dropped and counted in `ingest_samples_shed_total`. A
clean-session Host gets no broker redelivery, so a dropped reading is lost, as a reading over its
ceiling is on every transport.

| Setting | Default | What it does |
|---|---|---|
| `ingestRateLimit.readingsPerSecond` | `1000` | Per-tenant sustained ingest ceiling, counted in readings. A tenant's tier overrides it. Unset or non-positive falls back to this default, never to unlimited. |
| `ingestRateLimit.burst` | `2000` | Burst allowance for the above, in readings. |

**What is not metered.** The message rate is not metered, because the Host must observe every
message to keep its sessions correct. A birth's metric values are not charged: Sparkplug reports by
exception, so a birth is the only place a slow-changing metric appears, and a rebirth after a
reconnect or a failover re-sends every node's whole metric set at once. Births are bounded by the
session instead, once per birth sequence and with requested rebirths backed off. The presence
transitions births and deaths assert are not metered either. A runaway edge node can still send
messages as fast as the broker delivers them. Bound it at the broker, or by the groups you subscribe
to.

**Readings are also dropped while the platform applies backpressure.** While the platform is
refusing new events (see [Backpressure on the ingest path](./observability.md#ingest-backpressure)),
the readings of a message that are not yet stored are dropped at once, without the in-handler retry,
and counted in `ingest_failures_total`. That includes a birth's metric values, which the ceiling
never sheds: a slow-changing metric lost this way is missing until it next changes or the node is
reborn. The connect and disconnect transitions that births and deaths assert are still accepted.

**Publishing is windowed, and the window is also a loss bound.** Each connection reads its messages
one at a time, in order, because the Host must see every message to keep its sessions correct. It
does not wait for the platform to store a message before it reads the next: it keeps a window of
messages waiting for the platform broker's acknowledgement. Before, it waited for each one, so a
connection could not exceed one message per acknowledgement, about 88 a second on a replicated
broker, where an acknowledgement takes about 11 ms, and every Sparkplug source runs on the one
leader pod. A publish that fails is tried again, up to three attempts in all, half a second apart;
after that the message is dropped and counted in `ingest_failures_total`, as it always was. A
stop, or a handover to another leader, waits (up to 8 seconds) for what is in flight to be stored
before the lease is released.

| Setting | Default | What it does |
|---|---|---|
| `ingest.publishWindow` | `32` | How many messages one Sparkplug connection keeps waiting for the platform broker's acknowledgement at once, from 1 to 1024. A value outside that range, or an explicit 0, stops the service at startup; it is never adjusted. Publishing can go no faster than this number divided by how long an acknowledgement takes (about 2,800 messages a second per connection at 32). A fixed setting, read at startup, the same for every source. **It is also how much more a pod can lose.** The Host is a clean-session client that acknowledges each message to your broker as soon as it has taken it, and a broker does not redeliver to a host that restarted, so a pod that stops suddenly loses what it had taken and not yet stored: up to this many messages per connection, where it used to be one, plus, only while publishes are failing, those waiting to be tried again (about one more window at most). A new leader restores each node's presence and current metric values through rebirths, not the readings lost in between. An edge node's store-and-forward covers the time the Host is offline, not messages already delivered to a Host that then died. Watch `devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="sparkplug"}`: a rate near 1 means the window is what limits publishing. |
| `presence.probeSeconds` | `1800` | How often a connected Host re-checks its asserted devices for liveness. Once per interval, the node of each device the platform holds connected that has produced nothing within the last interval is asked to rebirth; a device still silent after the answer window (`presence.probeWindowSeconds`) is marked offline. The nodes are spread across the interval, each asked at its own fixed point in it, rather than all at once. A device heard from inside the interval is never asked and never judged, and a device's messages count for its node too, so only a node with a quiet device is asked, at most every other interval. Each ask makes that node re-send every metric value it has and records a fresh connect for each of its devices; see the cost note under [devices stuck online](#a-device-that-reads-online-and-is-not). This is what catches a death certificate the broker never delivered on a Host that stays connected; see [devices stuck online](#a-device-that-reads-online-and-is-not). Unset or 0 means 1800. A written value below 60 stops the service at startup; it is never adjusted. There is no off switch: a fleet for which a periodic rebirth is expensive sets it long. A fixed setting, read at startup, the same for every source. |
| `presence.probeWindowSeconds` | `10` | How long a probe waits for a node it asked to rebirth to answer, with its own birth and every device's, before marking the devices it has not heard from offline. Applies to the probe run at connect too. A node slower than this has those devices marked offline until their births arrive, when they come back online on their own, so raise it for large nodes on slow links. Unset or 0 means 10. A written value below 5, or not below `presence.probeSeconds`, stops the service at startup; it is never adjusted. A fixed setting, read at startup, the same for every source. |

**The reading limit is applied by splitting.** The
[per-event reading limit](../guides/connecting-a-device.md#how-much-one-message-may-carry) is applied
on this path by splitting a message, not refusing it. A DDATA carrying thousands of metrics becomes
several events, and still one stored reading per metric: each is its own row, state update and rule
evaluation on the detection engine every tenant shares. Splitting bounds each event, and the ceiling
bounds readings per second. One message is bounded too, at 16,384 metrics: a message carrying more
has none of its readings stored, its metrics are counted in `samples_skipped_total{reason="oversize"}`,
and the service logs a warning naming the topic. The message still counts for the session, so a
birth over the limit still connects the node and declares its aliases, and the node's later DATA is
read as usual. Bound the metric count per publish at the edge node.

The [tenant lifecycle gate](./tenant-deletion.md) still applies. Traffic for a deleting tenant is
refused on this path like any other, and counted in `tenant_deleted_dropped_total`.

### Unknown identities {#unknown-identities}

**Unknown identities are a choice.** With auto-registration on, a Sparkplug identity with no matching
device creates one. With it off, its telemetry is dropped and counted in
`unknown_device_dropped_total`. Check that metric when an edge node is publishing and nothing appears.

## The edge agent

The edge agent is **not a fourth ingest path**. It runs at a site, presents an ordinary MQTT endpoint
to local devices, buffers what they publish to local disk, and re-publishes it to the cloud as a [gateway](#gateway-identity): an ordinary device of its own, which the platform lets speak only for the devices you have assigned to it. There is no agent-shaped service on the platform side, and no agent-shaped configuration in the cloud services.

**It is not a chart functional area.** It appears in no area list and no deployment profile, and it
cannot be enabled the way a service is. It ships as static binaries and a container image, and you
deploy it yourself: a systemd unit on a site gateway, a container, or a hand-written Kubernetes
manifest at the edge.

**The binaries are attached to every [GitHub release](https://github.com/devicechain-io/devicechain/releases)**
as `dc-edge-agent_<version>_<os>_<arch>.tar.gz`, for Linux and macOS on `amd64` and `arm64`. Their
SHA-256 digests are in `dc-edge-agent_checksums.txt` on the same release, so on Linux
`sha256sum --ignore-missing -c dc-edge-agent_checksums.txt` checks what you downloaded. Linux is the
edge target; the macOS builds are for trying the agent on a workstation. There is no Windows build.

### Gateway identity {#gateway-identity}

The agent is a **gateway**: an ordinary device on the platform that fronts other devices. It signs in as itself, with its own device credential, and publishes each local device's events in its own namespace, `{instanceId}/{tenant}/gateways/{gateway}/devices/{device}/events` (see [the gateway namespace](../reference/device-protocol.md#gateway-namespace)). The platform accepts an event only for a device **assigned to that gateway**. This is the only uplink mode.

**Set one up:**

1. Create the gateway device, with any device type, and give it an `MQTT_BASIC` [credential](../guides/device-credentials.md). The device's token is the agent's `uplink.gatewayToken`. The credential's id goes in `uplink.username` as `{tenant}:{credentialId}`, and its secret in the environment variable named by `uplink.passwordEnv`.
2. Create the devices the site fronts and assign each one to the gateway, with `assignDeviceGateway` or with `gatewayToken` when you create it. A device has at most one gateway, and a gateway cannot itself have a gateway. Assigning needs the same permission as managing device credentials.
3. Start the agent. Local devices keep publishing their own topics to it, unchanged.

An assignment takes effect on the device's next event, within seconds, with no reconnect. Clearing one does the same, and the gateway's other devices keep flowing. Disabling the gateway, or revoking its credential, ends its session.

**What the agent does on the uplink:**

- It connects with the gateway device's plain MQTT client id, so the broker asserts the gateway's own presence as it does for any device. There is no per-agent client id: two agents using one gateway device take each other's session over.
- It builds every uplink topic from a child device token alone, so it cannot publish outside its own namespace. The broker cannot refuse a publish outside a grant (MQTT 3.1.1 has no way to), so it would acknowledge it and drop it. Being unable to build such a topic is what removes that failure.
- It captures only the configured tenant from its devices. A publish naming another tenant is counted in `tenant_mismatched_total` and never spooled.
- It removes `credentialType`, `credentialId` and `credentialSecret` from event bodies before forwarding, and counts them in `body_credentials_stripped_total`. The platform refuses an event that carries one on a broker-authenticated topic. Change the device so it stops sending them.
- It keeps up to **64 publishes in flight** and acknowledges each spooled event on its own acknowledgement. An agent that waited for each one before sending the next could not exceed one event per round trip, about 20 a second at 50 ms, and a busy site could never catch up after an outage. Replayed events keep the time they were first stored, and the cloud still folds a duplicate.
- It subscribes to the gateway's notices topic on every connect. If the broker refuses that subscription, the uplink stays down and the log says why.

**When an event is refused.** The platform refuses an event for a device that is not assigned to the gateway, or that does not exist. It records the event with its payload on the `failed-events` stream with the reason `NotGatewayChild`, counts it in `resolve_not_gateway_child_total`, and sends the gateway a *notice*. The broker has already acknowledged the publish, so nothing else would tell the agent. On a notice the agent logs a warning naming the device (at most once per device every 30 seconds), counts it in `refused_total{reason="not_gateway_child"}`, and writes the event to `refused-events.jsonl` in the store directory. That file is private to the agent's user, holds one JSON object per line, and is rotated once at 16 MiB.

Three limits apply. A notice is best effort: a gateway that was offline when it was sent does not get it, so the platform's record and counter are what you can rely on. The agent holds a bounded copy of what it forwarded (about 16 MiB) to match a notice against, so a notice that arrives after the copy has rolled out is counted in `refused_unmatched_total` and logged, not kept. And the file holds the payload as forwarded, with credentials already removed. Recovering a refused event after you fix the assignment is a manual replay.

The agent also refuses, without sending, an event the platform could only refuse: one from a local device that uses the gateway's own token (`reason="gateway_self"`), or a token the platform cannot hold (`reason="invalid_device_token"`). Those are written to the same file.

:::warning Upgrade the platform before you switch an agent to the gateway uplink
Finish the platform upgrade first: every `event-sources` and `device-management` replica, and the broker's grants. An older `event-sources` with `infrastructure.nats.mqttFailClosed` on acknowledges gateway messages it cannot attribute and drops them, so the agent sees success while nothing is stored. An agent whose broker has not been upgraded cannot subscribe to its notices and does not report the uplink connected (`uplink_connected` stays 0).
:::

**What a gateway is trusted for.** A gateway speaks for its assigned devices. A compromised gateway can forge events only for devices assigned to it, and only until it is disabled or its credential is revoked. Its word about *which local client* sent an event is only as strong as the site's local authentication.

Commands to devices behind a gateway are not routed yet, and the agent forwards events only.

### The spool {#the-spool}

**The spool is a drop-oldest ring.** The local store is a durable on-disk buffer, `1 GiB` by default.
When it is full, it drops the **oldest** un-forwarded events to admit new ones, never the newest.

That direction is deliberate. A device is acknowledged the moment it publishes, from the agent's own
persistence. Dropping the newest would discard exactly what the agent has just promised to keep, and
would leave you with a stale buffer at the end of an outage instead of a current one.

Every drop is counted, as the spool's own first sequence minus the count of events this agent has
forwarded and acknowledged. The second operand is delivery bookkeeping, and it is *persisted*, which
is what lets the count survive a restart rather than resetting to zero.

One case is not covered. When that persisted count is missing — a first start, or a store whose
progress file was removed — it is seeded from the spool's current first sequence. Anything already
evicted is then treated as accounted-for, and a restart in that state does reset the evidence. Keep
the store directory intact across restarts if the drop count matters to you.

**The spool bounds bytes, not age.** An event that waits in it keeps the time the device reported
(a JSON event that reported none is stamped with the time the agent stored it), so it reaches the
platform as old as the outage it sat through, and older if the device had buffered it first. The platform refuses a reading
more than 366 days older than the moment it arrives, so an event held longer than that is lost on
arrival. The agent does not warn about it; `spool_oldest_age_seconds` is the signal. An alert on it
above 335 days (28,944,000 seconds) leaves a month to restore the uplink.

:::caution Duplicate collapse on reconnect covers JSON payloads only
When the uplink returns, the agent re-forwards everything it buffered. For **JSON object payloads** it
stamps a replay-stable identity and event time, so a message already delivered folds into the
existing one at the cloud's uniqueness check and you see it once.

**Any other payload shape is forwarded verbatim and is at-least-once**, and a reconnect after a flaky
link can deliver it twice. If you use a non-JSON decoder behind an edge agent, make your handling of
the readings tolerant of a repeat.
:::

### Before you deploy an agent {#before-you-deploy-an-agent}

- **The local MQTT listener is open unless you configure a credential.** Set `local.username` and
  `local.passwordEnv` to require one. Leaving it open is a valid trusted-LAN posture, and the agent
  announces it with a loud warning at startup so the choice stays visible. Either way it is a
  network-access control, not per-device identity, and over plaintext MQTT the secret crosses the LAN
  in the clear.
- **The metrics and health endpoint binds to loopback only.** By design, the device MQTT port is the
  agent's only LAN-exposed surface. To scrape the agent from elsewhere, you need something on the box
  itself to relay it.

### Edge agent settings

| Setting | Default | What it does |
|---|---|---|
| `instanceId` | — | Required. The cloud instance this agent forwards into. Publishes seen for a different instance are not forwarded, and are counted in `instance_mismatched_total`. |
| `local.listenPort` | `1883` | The MQTT port site devices connect to. |
| `local.storeDir` | — | Required. The directory holding the durable spool. One agent per directory. |
| `local.spoolMaxBytes` | `1 GiB` | Spool budget. Beyond it, oldest events are dropped. The floor is 16 MiB. |
| `local.metricsPort` | `9090` | Loopback-only metrics and health port. An explicit `0` disables the endpoint. |
| `uplink.brokerUrl` | — | Required. The cloud MQTT endpoint to forward to. |
| `uplink.tenant` | — | Required. The tenant the gateway device belongs to. It is part of every uplink topic, and the only tenant the agent takes from its devices. |
| `uplink.gatewayToken` | — | Required. The token of the gateway device this agent signs in as. **One agent per gateway device**: two sharing one take each other's session over in a loop. |
| `uplink.username` | — | Required. `{tenant}:{credentialId}`, the gateway device's credential. A name without the tenant prefix is refused when the agent starts. |
| `uplink.passwordEnv` | — | Required. The environment variable holding the credential's secret. |
| `uplink.connectTimeoutSeconds` | `30` | Bounds one uplink connection attempt. |
| `uplink.backoffMinSeconds` / `uplink.backoffMaxSeconds` | `1` / `60` | Reconnection backoff bounds while the link is down. |

## What to watch {#what-to-watch}

:::note Seven alerts ship for these two services
The chart ships seven alert rules for sparkplug-ingest and lwm2m-ingest, described under
[Alerts that ship](#edge-alerts): whether each transport is being read at all, a LwM2M leader that is
stuck, and the ways the two services drop data. **Every other metric in the tables below has no alert
and no dashboard panel.** It is emitted and scraped, and nothing will page you about it until you write
the rule yourself. The edge agent has no shipped alert at all.
:::

All metrics carry the `devicechain_` prefix and their service's own segment:
`devicechain_sparkplugingest_`, `devicechain_lwm2mingest_`, `devicechain_edge_`. None of them is
labelled per device or per tenant, so none of them is a cardinality risk to scrape.

### Alerts that ship {#edge-alerts}

| Alert | Severity | What it means | What to do |
| --- | --- | --- | --- |
| `Lwm2mIngestLeaderCountNotOne` | critical | For 2 minutes, the number of lwm2m-ingest replicas reporting that they hold the leadership lease has not been exactly 1. At 0, nobody is reading the CoAP/DTLS transport and every LwM2M device is unreachable, although every pod looks healthy. Above 1, two replicas are serving at once. | Read the lwm2m-ingest logs for why no replica acquired the lease; typically the platform's message broker is unreachable. |
| `Lwm2mIngestLeaderNotServing` | critical | For 5 minutes, a pod has held the LwM2M lease while its CoAP/DTLS read loop is not running (`is_leader` is 1 and `is_serving` is 0). It is stuck building its term, typically reading the asserted-active devices from device-state, and because it keeps the lease no standby can take over. No LwM2M device is being served. | Read that pod's log around `Acquired LwM2M leadership`, then restart the pod so a standby can acquire the lease. |
| `Lwm2mIngestDroppingNotifications` | warning | In the last 10 minutes, for 10 minutes, lwm2m-ingest has dropped the readings of Notify messages because ingest failed or refused them, backpressure included. There is no retry in the Notify path, so a device that reports rarely loses its reading. | Check the message broker, the backlog of the inbound-events stream (see [Backpressure on the ingest path](./observability.md#ingest-backpressure)) and the lwm2m-ingest log. Counted in `notify_ingest_dropped_total`. |
| `SparkplugIngestLeaderCountNotOne` | critical | For 2 minutes, the number of sparkplug-ingest replicas reporting that they hold the lease has not been exactly 1. At 0, no Sparkplug source is being read. Above 1, two replicas connect the same sources. A pod deployed with no sources configured never takes the lease and reads 0 permanently. | Configure the pod's sources or leave the service out of the deployment. Otherwise read the sparkplug-ingest logs for why no replica acquired the lease. |
| `SparkplugIngestBrokerUnreachable` | warning | In the last 10 minutes, for 10 minutes, sparkplug-ingest has failed to connect to a configured Sparkplug broker. Readiness is deliberately not gated on broker reachability, so the pod looks healthy while that source ingests nothing. | A bad host, a rejected credential or a broker outage all raise it. The sparkplug-ingest log names the source and the reason. Counted in `connect_failures_total`. |
| `SparkplugIngestSubscriptionRefused` | warning | In the last 15 minutes, for 5 minutes, a Sparkplug source has been abandoned and retried because its broker accepted the connection but refused, or never acknowledged, a group subscription. While any group is refused, the source ingests none of its groups and never announces ONLINE. | Compare the broker's access rules for the source's credential with the groups configured for it; the credential most likely may not read one of them. Counted in `subscribe_failures_total`. |
| `SparkplugIngestDroppingData` | warning | In the last 10 minutes, for 10 minutes, sparkplug-ingest has abandoned messages it accepted: their readings or presence transitions were dropped after the retry budget was spent, at once under backpressure, or at shutdown. A Sparkplug Host gets no broker redelivery, so each one is lost. | Check device-management and the message broker, and the sparkplug-ingest log for `Dropping Sparkplug samples`. Counted in `ingest_failures_total`. |

### The leader alerts {#no-leader-alert}

The two `LeaderCountNotOne` alerts are the most load-bearing signal on this whole surface: zero means
nobody is serving that transport, and every device on it is silently unreachable. They fire on a
sum other than one, and they deliberately carry **no `absent()` arm**. A service that is not deployed
exports nothing, and the chart's rules are not told which services an install runs. A deployed service
with no running pod exports nothing either; that is a pod failing loudly in its own right, through
restarts and readiness. If you want to be paged when the series does not exist at all, add the pairing
yourself:

> `sum(devicechain_lwm2mingest_is_leader) != 1 or absent(devicechain_lwm2mingest_is_leader)`
>
> `sum(devicechain_sparkplugingest_is_leader) != 1 or absent(devicechain_sparkplugingest_is_leader)`

A Sparkplug pod running with its sources unset registers its `is_leader` gauge at initialization,
before it checks whether it has anything to serve, and then never acquires the lease. It publishes the
series reading **0** for the life of the pod, and `SparkplugIngestLeaderCountNotOne` fires on it by
itself. That is deliberate: a transport service with nothing to serve is a configuration to fix, or a
service to leave out of the deployment.

Both services' `is_leader` gauges go up when the replica **acquires** the lease, not when it finishes
building its leadership term. A normal takeover therefore does not read as leaderless while the new leader
rebuilds its state. What hides in that window instead is a leader **stuck** in a build, and on LwM2M
a second gauge names it: see `is_serving` below, and `Lwm2mIngestLeaderNotServing` above.

### Sparkplug ingestion metrics {#sparkplug-ingestion-metrics}

Prefix: `devicechain_sparkplugingest_`.

| Signal | Means |
|---|---|
| `is_leader` | 1 on the serving pod, 0 elsewhere. `SparkplugIngestLeaderCountNotOne` fires when the sum is not 1. |
| `connect_failures_total` | A configured broker is not reachable. Rising means one source is down while the pod looks healthy. Feeds `SparkplugIngestBrokerUnreachable`. |
| `subscribe_failures_total` | The broker accepted the connection but refused (or never acknowledged) a group subscription, most likely because the source's credential may not read that group. The source stays offline, ingests none of its groups, and retries. `SparkplugIngestSubscriptionRefused` fires on an increase. |
| `messages_total` | Inbound Sparkplug traffic. A flat line on a live fleet is the symptom of a lost subscription or a dead source. |
| `presence_emitted_total` | Connect/disconnect signals produced. |
| `rebirth_requests_total` | Nodes being asked to re-announce. Steadily rising means a node is failing to resynchronise. The liveness probe's rebirths count here too, so on a fleet with quiet devices expect a steady trickle spread across each `presence.probeSeconds`. |
| `presence_probes_total` | Liveness probes that reached a verdict, by `trigger`: `connect` (the reconciliation run once per broker session) or `periodic` (one per slot of the rotation that visits every node once per `presence.probeSeconds` on a steady session). A probe asks the nodes of the quiet asserted devices it covers to rebirth and marks the devices that do not answer offline; a slot that finds nothing quiet still counts. **On a connected Host the periodic series must rise.** Flat means the probe is not running, and a quiet fleet and a probe that never runs are otherwise indistinguishable. |
| `presence_probe_disconnects_total` | Asserted devices a liveness probe marked offline for not answering a rebirth inside the window, by the same `trigger`. Near zero in a healthy fleet. A standing `periodic` rate means death certificates are being lost between the nodes and the Host. |
| `rebirth_enqueued_total` / `rebirth_dropped_total` | Rebirths the session machine asked for, and the ones its publish queue was too full to take. A drop is a latency signal rather than a failure — the request is re-made on the node's next window — but a standing drop rate means rebirths are going out slower than they are being asked for. Read it against `rebirth_requests_total`, which counts only what reached the wire and is therefore capped by the publisher rather than by demand: **drops while `rebirth_requests_total` climbs to a steady ceiling** is fan-out outrunning a publisher that is otherwise healthy; **drops while it is flat** is the publishes themselves stalling, which points at the broker connection. |
| `unknown_device_dropped_total` | Traffic from identities with no device, with auto-registration off. |
| `decode_errors_total` | Malformed payloads. |
| `ingest_failures_total` | Accepted messages whose readings, or the connect and disconnect transitions their births and deaths assert, were dropped, plus batches of disconnects that presence reconciliation declared and could not store: readings at once while the platform applies backpressure, and either one after the retry ran out (device-management or the broker unreachable, or the platform broker not acknowledging the publish), when the queue of publishes waiting to be retried was full, or while the connection was shutting down. A message split across several events may have stored some of its events. A clean-session Host gets no redelivery, so each one is lost. Feeds `SparkplugIngestDroppingData`. |
| `ingest_samples_shed_total` | DATA readings dropped because a tenant is over its ingest ceiling. A tenant shedding here sends more readings than its tier allows. |
| `tenant_deleted_dropped_total` | Traffic refused because its tenant is being deleted. |
| `samples_too_old_dropped_total` | Readings dropped because their timestamp is more than 366 days before the host received them; the rest of their message is stored. A metric whose value has not changed for that long is dropped from every birth, and a node with no clock set (times near 1970) has all its readings dropped. The service logs a warning naming the device. |

### LwM2M ingestion metrics {#lwm2m-ingestion-metrics}

Prefix: `devicechain_lwm2mingest_`.

| Signal | Means |
|---|---|
| `is_leader` | 1 on the pod holding the lease, from the moment it acquires it. `Lwm2mIngestLeaderCountNotOne` fires when the sum is not 1. |
| `is_serving` | 1 once that pod's CoAP/DTLS read loop is actually running. Read it **with** `is_leader`: the pair is the only thing that separates a leader still rebuilding its registration table from a leader wedged in that rebuild, and every other signal on the pod — readiness, liveness, `is_leader` — is green for both. `is_leader == 1 and is_serving == 0` sustained for longer than a takeover takes is what `Lwm2mIngestLeaderNotServing` alerts on. |
| `active_registrations` / `active_sessions` / `active_observations` | The live fleet as the service sees it. **Watch `active_observations` across a restart** — it is how you see the observation loss described above, and how you see it recover. |
| `registrations_total` / `registration_updates_total` | Devices arriving and keeping their sessions alive. |
| `registration_expiries_total` | Registrations that lapsed rather than deregistering — devices that vanished. |
| `handshake_failures_total` / `auth_errors_total` | Devices failing DTLS, and identities that are not provisioned. |
| `observe_establish_refused_total` | An Observe the device refused or that otherwise failed. **The 1.0-only client symptom** — such a client answers the SenML Observe with `4.06` and never notifies. |
| `notify_unknown_content_format_total` | Telemetry arriving in a format that is not decoded — a device that *does* notify, undecodably. Zero for a 1.0-only client. |
| `notify_decode_failures_total` | Malformed payloads. |
| `notify_records_non_numeric_total` / `notify_records_non_finite_total` / `notify_records_unnamed_total` | Readings a Notify carried that produced no measurement. **Non-numeric is normal** — a boolean or string IPSO reading is a device working correctly, and this counter is what tells that apart from a device that has gone quiet, which otherwise looks identical from here. The other two are firmware faults: a value that resolved to infinity or NaN, and a reading with no resource path. |
| `observation_overflow_total` | A registration exceeding the 32-observation cap. Some of its resources are not observed. |
| `telemetry_too_old_dropped_total` | Readings dropped because their time is more than 366 days before the adapter received them; the rest of their Notify is stored. The service logs a warning naming the device. |
| `ingest_messages_shed_total` / `ingest_samples_shed_total` | A tenant over its ingest ceiling, in Notify messages and in readings. |
| `notify_ingest_dropped_total` | Notify messages whose readings were dropped because the platform refused them or could not take them, including while it applies backpressure. There is no retry; the next Notify supersedes what was lost. A Notify split across several events may have stored its leading events. Feeds `Lwm2mIngestDroppingNotifications`. |
| `shadows_reconstructed_total` | Presence rebuilt after a leadership change. A spike is the fingerprint of a failover. |
| `commands_failed_total` / `commands_not_served_total` | Downlink commands that did not land. |
| `command_live_claim_errors_total` | Commands **not carried out** because command-delivery could not confirm them. Each command is confirmed with command-delivery immediately before it reaches the device, and without that confirmation it is never sent. A sustained rate means no LwM2M command is reaching its device: **this is the one to alert on.** The commands are retried, not lost. |
| `commands_stale_dispatch_total` | Deliveries discarded because the platform had already re-armed or re-sent the command. **Not a fault**: each one is a duplicate actuation that did not happen. Expect it to rise after an outage or a failover. |
| `commands_overflow_parked_total{reason}` | Commands set aside in command-delivery instead of being sent straight away, and delivered in order moments later. `full`: the device's queue was full, so the device is slow to answer. `offline`: it had no live connection. `bind`: it had just connected and its waiting commands had not been delivered yet; **expect a spike after a failover**, when every device reconnects at once. `unconfirmed`: a command ahead of it could not be confirmed. |
| `command_overflow_blocked_total` | Times the adapter had to wait because it could not set commands aside fast enough. It rises only while command-delivery is slow or unreachable, and then every LwM2M command waits. |
| `command_drain_turns_total` | Drain turns run, each delivering at most 4 of one device's set-aside commands. A rising rate while `commands_overflow_parked_total` stays flat means backlogs are being worked off. |

### Edge agent metrics {#edge-agent-metrics}

Prefix: `devicechain_edge_`.

| Signal | Means |
|---|---|
| `uplink_connected` | 0 means the site is buffering. |
| `spool_oldest_age_seconds` | **The primary backlog signal.** How far behind the agent is, in wall-clock terms. |
| `spool_used_bytes` / `spool_limit_bytes` | How close the spool is to dropping. |
| `dropped_total` | **Data has been lost.** Oldest events evicted to make room. Any increase is real loss. |
| `forward_errors_total` | Forward attempts that failed; the event stays buffered for redelivery. |
| `received_total` / `forwarded_total` | Throughput in and out. |
| `malformed_total` | Events discarded as unforwardable rather than blocking the queue behind them. |
| `tenant_mismatched_total` | Device publishes naming a tenant other than `uplink.tenant`. They are not spooled or forwarded. |
| `body_credentials_stripped_total` | Credential members removed from event bodies before forwarding. The device should stop sending them. |
| `refused_total{reason}` | **Events refused for this gateway.** `not_gateway_child`: the platform refused an event because its device is not assigned to the gateway or does not exist. `gateway_self` and `invalid_device_token`: the agent would not send it. Any increase needs attention; see [gateway identity](#gateway-identity). |
| `refused_kept_total` / `refused_unmatched_total` | Refused events written to `refused-events.jsonl` / refusals whose event was no longer held on the box (the platform's `failed-events` record has it). |
| `uplink_inflight` | Publishes waiting for an acknowledgement, at most 64. Staying at 64 while a backlog drains is normal. |
| `local_auth_enabled` | 0 means the site's MQTT listener requires no credential. |

## Validation limits {#what-is-not-validated}

Two limits on how these services are validated:

- **No shipped rig exercises a real Sparkplug fleet.** Nothing in the project drives a third-party
  edge node or broker end to end. Sparkplug behaviour is covered by tests against the platform's own
  implementation.
- **The LwM2M path is the better-validated of the two.** It is exercised against an independent
  third-party LwM2M client stack, reading its verdicts from the server side so a client that
  misbehaves cannot manufacture a pass. That suite runs on a schedule and is advisory rather than a
  release gate.

The edge agent is covered by its own tests and has no in-cluster deployment validation. Pilot a first
agent rollout at one site before it becomes a fleet.
