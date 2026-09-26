---
title: Device Presence
---

# Device Presence

DeviceChain keeps a live **presence** signal for every device: whether it is online now, and when it last connected, disconnected or reported activity. Presence is part of a device's [last-known state](./architecture.md), the same projection that holds its most recent measurements. You see it on the device's **Connectivity** tab in the console.

How DeviceChain decides a device is online depends on the transport, so that is where this page starts.

## Two ways presence is known {#two-ways-presence-is-known}

Every device carries a **presence source** that says how its online/offline state is determined.

**Inferred** is the default. The transport gives DeviceChain no explicit connect or disconnect signal, so presence is inferred from activity. A device counts as online while it is sending data. If it goes quiet for longer than its **inactivity timeout**, a background sweep marks it offline. This is the right model for connectionless transports such as plain HTTP and CoAP.

**Asserted** means the transport tells DeviceChain explicitly when a device connects and disconnects, so presence is authoritative rather than guessed. The first time such a signal arrives for a device, DeviceChain switches that device to the asserted source. From then on:

- Its online/offline state is driven only by explicit connect/disconnect signals. A stray data packet can never mark a device online after the platform has been told it is offline.
- The inactivity sweep leaves it alone. An asserted device that goes quiet is not assumed dead, because on a transport whose job is to report death explicitly, silence is not evidence of death. Mixing the two would let a long-interval reporter be marked offline while the platform has been told it is connected.

A device stays inferred until an asserting transport produces for it. Existing devices are unaffected unless they start arriving over a transport that asserts presence.

### Transports that assert presence {#transports-that-assert-presence}

Three transports assert presence today:

- **Plain MQTT**, for devices connected to DeviceChain's own broker. The broker already knows the moment a connection opens and closes, and DeviceChain reads that directly. See [MQTT on the platform broker](#mqtt-on-the-platform-broker).
- **[Sparkplug-B](./sparkplug.md)**, whose BIRTH and DEATH messages are exactly these explicit connect/disconnect signals.
- **[LwM2M](./lwm2m.md)**, whose registration lifecycle does the same: register, periodic update, and deregister (or a lapsed lifetime).

### An asserted device has no inactivity backstop {#no-inactivity-backstop}

Skipping the inactivity sweep is deliberate, and it has a consequence you need to plan for: **an asserted device has no inactivity backstop.** Its offline signal can only come from the transport. If that signal never arrives, the device keeps reading online with nothing to correct it. Two examples:

- A Sparkplug death certificate lost along with the connection.
- An LwM2M device whose registration lifetime has not yet lapsed. LwM2M's own default lifetime is 86400 seconds, a full day.

What to watch for, and how to bound the window, is in [Running the Edge Services](../deployment/edge-services.md). If the transport that would have reported the disconnect is gone for good rather than merely quiet, [release the device back to inferred presence](../deployment/edge-services.md#demoting-a-device) so a correction is possible again.

### MQTT on the platform broker {#mqtt-on-the-platform-broker}

MQTT presence needs no cooperation from the device. It does not have to publish a birth message, set a last will, or announce itself in any way.

What has to be equipped is the instance, not the device. The part of `event-sources` that reads the broker's connections, the **broker tap** (or just "the tap" below), needs two things:

- a NATS system-account credential
- an event source pointed at this instance's own broker

Until the instance has both, the tap stays off and MQTT devices stay inferred. `dcctl bootstrap` mints that credential and wires that source, so an instance brought up that way asserts MQTT presence with no further work. A bare `helm install` leaves the credential empty and does not. [Confirming the broker tap is running](#confirming-the-tap) tells you which one you have.

An instance that *had* a tap and then loses that credential is a different case from one that never had it. The devices it already asserted would otherwise stay frozen at whatever they last reported, so the instance hands them back to inferred presence instead. See [returning a device to inferred presence](../deployment/edge-services.md#demoting-a-device).

Two more details matter before you build on MQTT presence:

- **It follows the device's main connection.** A device may open extra connections by appending its own suffix to its client id, as in the two-terminal `mosquitto_sub` / `mosquitto_pub` workflow. Presence deliberately ignores those extra connections and follows the primary session, so closing a side connection never makes a connected device read as offline. A device that only ever connects with a suffixed client id is not asserted at all and stays inferred.
- **It covers devices on this instance's broker.** DeviceChain cannot observe connections on a broker you run yourself, so a device reaching DeviceChain through one stays inferred.

### Repairing missed MQTT disconnects {#repairing-missed-mqtt-disconnects}

For MQTT devices, DeviceChain closes the no-backstop gap itself. There is one case where the broker cannot tell you a device has gone: when the broker restarts, the connections it held vanish and no disconnect is announced for them.

So DeviceChain periodically compares the broker's live connection list with what it believes, and corrects the difference in both directions:

- devices it did not know were connected
- devices it thinks are connected that the broker is not holding

Devices that reconnect after a broker restart are corrected by their own reconnect. The rest are corrected by a later comparison, one that can account for the whole cluster, as described next and in the [scale-down caveat](#resizing-the-broker-cluster).

The comparison declines to mark anything offline unless it can account for **every** node of the broker cluster. If one node is slow or unreachable, its devices are missing from the list and look the same as devices that have genuinely gone. Wrongly marking a live device offline is the more damaging error, because everything keyed on presence acts on it: the device reads offline on its Connectivity tab, and a [Connectivity rule](./event-processing.md#condition-types) raises a disconnect alarm for a device that was reachable the whole time. In that situation DeviceChain still marks newly seen devices online, and waits for the next pass to make the offline call.

### Where the presence source shows {#where-the-presence-source-shows}

The presence source is shown wherever it changes what a reading means:

- **Console.** The Connectivity tab names the source, *Reported by the transport* or *Inferred from activity*. It also distinguishes a device the transport reported **Disconnected** from one that is merely **Offline**. Offline means nothing has arrived recently, which is also exactly what a healthy device on a slow reporting interval looks like.
- **[MCP](./mcp.md).** The `get_device_state` tool returns `presenceSource` alongside the state, and tells the assistant not to report an inferred inactive device as down.
- **API.** `presenceSource` is a field on `device-state`'s `DeviceState` type, returning `ASSERTED` or `INFERRED`.

## Why the distinction matters {#why-the-distinction-matters}

Inferred presence is convenient but laggy and ambiguous. "Offline" only means "hasn't spoken recently", which is slow to notice a real disconnect and blind for devices that report on a long interval. Asserted presence is immediate and unambiguous: a disconnect is a disconnect the instant the transport reports it. That is what you want for anything you will alarm or act on.

Because the mode is an explicit per-device flag, a device on a connectionless transport keeps its familiar timeout behavior, a device on a presence-aware transport gets the authoritative signal, and the two never interfere.

:::note Status
Device presence, both inferred and asserted, is available. Three transports assert presence: plain MQTT on DeviceChain's own broker (only once the instance has the system-account credential described above), [Sparkplug-B](./sparkplug.md) and [LwM2M](./lwm2m.md). Detection rules can fire on connect/disconnect edges; see [Rules on presence](#rules-on-presence).
:::

## Rules on presence {#rules-on-presence}

A detection rule can fire directly on a connect/disconnect edge. The [Connectivity condition](./event-processing.md#condition-types) raises an alarm the instant an authoritative disconnect arrives, and resolves it on reconnect. There is no timeout to tune.

- **Rule form.** The console's rule form offers it as the **Connectivity** type. There is no condition to author, because the presence edge itself is the signal. The form opens an existing Connectivity rule as its own type. If a stored definition carries anything the form cannot model, the form says so before you save rather than silently replacing it.
- **Automation canvas.** The canvas offers a **Connectivity** node. It has nothing to configure: wire the source into it and its signal into actions. Like the form, the canvas never silently rewrites a stored rule it cannot show in full. It says so and turns saving off.

The Connectivity condition complements the timeout-based Absence rule (authoritative death versus inferred silence), and the two are meant to be paired.

An authoritative disconnect also updates the device's live state, so the Connectivity tab shows the device offline the moment the transport reports it.

## Operating presence {#running-it}

Presence is only as good as the signal behind it. The two asserting edge transports each run as a single owning replica, which gives presence a few operational properties to understand before you alarm on it: what a changeover costs, why an asserted device can be stuck online, and how to bound that. [Running the Edge Services](../deployment/edge-services.md) covers them.

The sections below cover properties specific to broker-asserted MQTT presence.

### Confirming the broker tap is running {#confirming-the-tap}

Reading connections off DeviceChain's own broker needs four things. If any is missing, the tap **declines to start**. It logs why and sets `presence_tap_off{reason}` to say which one. MQTT devices that were never asserted stay inferred, which looks exactly like an instance that never had asserted presence, because functionally it is one. The four requirements:

- `brokerPresence.enabled` is not set to `false`.
- A NATS system-account credential is configured. `dcctl bootstrap` mints one; a missing one is the usual reason a hand-assembled instance has no tap.
- At least one event source points at the platform's own broker. With none, there are no connection advisories to read.
- Service-to-service calls are configured. Without them the tap would run with no repair path, so it stays off deliberately rather than half-working: a device whose disconnect the broker never announced would read as connected forever.

#### When a tap that was running turns off {#when-a-running-tap-turns-off}

On an instance that never had a tap, that is the whole story. An instance that did have one has a second problem: devices already marked asserted keep whatever presence they last had, because an asserted device is exempt from the inactivity sweep and a data event cannot flip it.

For the first two reasons above, a written `enabled: false` and a missing system-account credential, `event-sources` releases those devices back to inferred presence itself. Both are configuration that every replica reads identically, which is what makes releasing a whole fleet on their strength safe to automate.

**A broker the tap cannot reach releases them too**, for a stronger reason than configuration. The MQTT gateway that devices connect through lives in that same broker, so while the broker is unreachable no device is connected through it either. The tap gives the connection thirty seconds to come up before it decides. The trigger is therefore half a minute with no system-account connection, not one failed attempt, which is long enough that a broker restarting alongside the services is not mistaken for one that is gone.

**Losing that connection after the tap has started restarts `event-sources`.** If the broker closes the tap's connection for good once the tap is running (for example, because it no longer accepts the system-account credential), `event-sources` fails its liveness check and Kubernetes restarts the pod. A refused credential reaches every replica at once, so every `event-sources` pod restarts. HTTP ingest is unavailable while they do; MQTT telemetry is still stored by the broker and processed when they return. If the restarted pod still cannot sign in, the tap turns off with reason `broker_unreachable`, releases the devices it asserted as described here, and keeps checking. That reason therefore also covers a broker that is running but refuses the credential.

For this one reason, the two-minute wait before a release (see [returning a device to inferred presence](../deployment/edge-services.md#demoting-a-device)) is a re-check rather than a delay, and that difference stops the release outliving the outage that caused it. Before each pass, the first one included, the service re-dials the system account. If the broker answers, nothing is released: the service exits, and the pod restarts with a tap that comes up normally. A broker that returns therefore produces a pod restart, not a fleet of released devices. The other two release paths cannot work this way and do not need to, because both are configuration read once at startup. A written `enabled: false` gets no two-minute wait and starts releasing within about thirty seconds, a random delay that keeps replicas from starting together. For a missing credential, what the two-minute wait waits for is the replacement pod that a configuration change rolls out.

Nothing is released automatically in the remaining three cases:

- no source pointed at the platform broker
- no service-to-service configuration
- a subscription that fails on a connection that *did* reach the broker

`dcctl presence demote` is the way out in those cases. The automatic release and the manual command are both described in [returning a device to inferred presence](../deployment/edge-services.md#demoting-a-device).

#### Signals that the tap is not running {#signals-that-the-tap-is-not-running}

Two signals tell you the tap is not running, and they cover different failures.

`presence_tap_off{reason}` is the direct one. It goes to 1 on every path where the tap declines to start, with the label naming which. It answers a question a quiet fleet otherwise makes unanswerable: a long-lived MQTT fleet legitimately emits no connect or disconnect advisories for days, so nothing in the ordinary flow of presence events distinguishes an instance that is asserting presence from one that silently never started.

It does not cover a tap that started and then stopped working, because then nothing declines to start. **`presence_canary_missed_total` covers that case, and it is the counter to alarm on.** The service opens its own MQTT connection once a minute purely so that a working tap has something to observe. `presence_canary_observed_total` rises on a healthy tap, and `presence_canary_missed_total` rises when the chain is broken.

The canary runs on its own schedule, independently of the repair pass described below. That separation is what makes the counter trustworthy: an instrument that could only report while the thing it watches was healthy would go quiet at exactly the moment it mattered.

Read the other presence counters as traffic, not health. `presence_events_total` is legitimately flat on a quiet fleet. It is also legitimately *not* flat on a tap that has just been switched off, because releasing the devices that tap had asserted emits one event per device under `presence_events_total{state="demoted"}`. Neither shape says anything about whether presence is being read.

### Tuning the tap {#broker-presence-settings}

The tap ships with working defaults, and most instances never change them. The settings live under the `event-sources` area's `brokerPresence` configuration.

| Setting | Default | What it does |
|---|---|---|
| `enabled` | on when unset | Runs the tap. Set it to `false` to turn broker-asserted MQTT presence off deliberately, for example on an instance whose broker is shared with something that objects to a system-account subscriber. MQTT devices then run on inferred presence, and any the tap had already asserted are released back to it, paced, over the following minutes. |
| `reconcileSeconds` | `300` | How often the broker's live connection list is compared against the platform's own, in both directions. **This is not a backstop.** A graceful broker restart announces no disconnects at all, so this pass is the only thing that ever corrects those devices, and an asserted device has no inactivity sweep behind it. Lower it for a faster repair, at the cost of one cluster-wide inventory plus one read per tenant on every pass. |
| `canarySeconds` | `60` | How often the service opens its own MQTT connection to prove the tap is still live. This is the schedule `presence_canary_missed_total` counts against. |
| `canaryDeadlineSeconds` | `15` | Bounds one probe. Too tight and it reports failures the tap does not have. |
| `inventoryGatherSeconds` | `5` | How long a pass collects replies from the broker cluster. Too short and a merely slow node reads as absent, which withholds every disconnect that pass. |

A non-positive value on any of the four intervals falls back to the default above, not to zero.

### A repair pass that runs out of time says so {#reconcile-pass-timeout}

Each repair pass walks every tenant on the instance and reads that tenant's asserted devices, so a large instance's pass is long. It is bounded: a pass that cannot finish inside its budget stops, reports `presence_reconcile_runs_total{outcome="timeout"}`, and logs how many tenants it covered.

The next pass then resumes at the first tenant it did not reach, rather than starting again from the beginning. Without that, a fleet whose pass never fit in the budget would repair the same first few tenants on every attempt and never reach the rest: not late, never.

- Occasional `timeout` outcomes mean repairs are lagging, and every tenant still gets its turn.
- A sustained run of them means the instance needs more headroom. The devices at the back of the rotation are the ones whose missed disconnects go uncorrected longest.

`presence_reconcile_runs_total` carries one outcome per pass:

| Outcome | What it means |
| --- | --- |
| `complete` | every tenant was walked against a fully accounted-for broker cluster |
| `partial` | the pass ran, but not every broker node answered — devices were only ever marked **online**, never offline |
| `timeout` | the pass ran out of its budget; the tenants it did not reach go first next time |
| `failed` | the pass could read nothing — no broker inventory, no tenant list, or **no tenant's presence state**. Reconciliation did nothing at all |
| `cancelled` | the service was shutting down mid-pass. Not a fault |

Alarm on `failed` alongside the canary. A single tenant's state read failing is tolerated, and the other tenants still get their pass. Every read failing means repairs have stopped entirely, which is what a device-state outage looks like from here.

### Presence transitions are metered against the tenant's ingest ceiling {#presence-and-the-ingest-ceiling}

Connect and disconnect transitions pass the same per-tenant [ingest limit](./governance.md) as telemetry. They are **refused when a tenant is at its ceiling**, and counted in `presence_events_refused_total`.

This is deliberate. Connection churn is entirely device-controlled and otherwise free: a device reconnecting in a loop would be an unmetered write amplifier that the ingest limiter never sees.

The consequence is worth planning for. A tenant pressed against its ceiling has devices whose online/offline state is wrong, and stays wrong until a later reconciliation pass repairs it (by default, up to five minutes). Anything keyed on presence is wrong for that window too, including Connectivity rules and the release of commands held for an offline device.

**A demotion goes through the same gate.** A tenant pressed against its ceiling can have its repair refused along with the churn causing the pressure. Nothing is lost, because a refused release leaves the device asserted and the next pass finds it again, but the repair arrives no sooner than the ceiling allows.

This applies to the platform broker's MQTT tap. Sparkplug ingestion applies no per-tenant ceiling and sheds nothing, and LwM2M runs its own separately configured limit.

### Resizing the broker cluster requires restarting `event-sources` {#resizing-the-broker-cluster}

The repair comparison declines to mark anything offline unless it can account for every node of the broker cluster. It decides what "every node" means from the largest cluster it has ever seen. That mark only ever rises, which is what stops a network partition from causing mass false disconnects: a route-isolated broker reports itself as the whole cluster and would otherwise satisfy its own check.

The cost is one case the design cannot distinguish: **deliberately scaling the broker cluster down**. Fewer nodes answer than the remembered maximum, so every later pass is treated as incomplete and no offline repair is ever made, not just until the next pass but for the life of the process. Devices orphaned by the removed node read online indefinitely, and because an asserted device has no inactivity backstop, no timeout corrects them.

**After scaling the NATS cluster down, restart `event-sources`.** If you needed to and did not, `presence_reconcile_withheld_disconnects_total` rises without settling. Scaling up needs nothing.

If the restart has to wait, the orphaned devices do not have to. Run `dcctl presence demote` on that source to release them back to inferred presence, where the ten-minute inactivity sweep can mark them offline on their own evidence. See [returning a device to inferred presence](../deployment/edge-services.md#demoting-a-device). The demotion repairs the devices that are already wrong; the restart is still what stops the next ones going wrong.
