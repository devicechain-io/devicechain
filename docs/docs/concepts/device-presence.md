---
title: Device Presence
---

# Device Presence

DeviceChain keeps a live **presence** signal for every device — whether it is currently online, and when it last connected, disconnected, or reported activity. Presence is part of a device's [last-known state](./architecture.md) (the same projection that holds its most recent measurements), and it surfaces on the device's **Connectivity** tab in the console.

The important thing to understand is *how* DeviceChain decides a device is online, because it depends on the transport.

## Two ways presence is known

Every device carries a **presence source** that says how its online/offline state is determined:

- **Inferred** (the default) — DeviceChain has no explicit connect/disconnect signal from the transport, so it infers presence from **activity**. A device is considered online while it is sending data; if it goes quiet for longer than its **inactivity timeout**, a background sweep marks it offline. This is the right model for connectionless transports (plain HTTP, CoAP).

- **Asserted** — the transport tells DeviceChain *explicitly* when a device connects and disconnects, so presence is **authoritative** rather than guessed. The first time such a signal arrives for a device, DeviceChain switches that device to the asserted source and, from then on:
  - its online/offline state is driven **only** by explicit connect/disconnect signals — a stray data packet can never mark a device online that the platform has been told is offline;
  - the inactivity sweep leaves it alone — an asserted device that goes quiet is *not* assumed dead, because silence is not evidence of death on a transport whose whole job is to report death explicitly. Mixing the two would let a long-interval reporter be marked offline while the platform has been told it is connected.

A device stays **inferred** until an asserting transport produces for it, so nothing changes for existing devices unless they start arriving over a transport that asserts presence. Three transports assert presence today:

- **Plain MQTT**, for devices connected to DeviceChain's own broker. No cooperation is needed from the device — it does not have to publish a birth message, set a last will, or announce itself in any way. The broker already knows the moment a connection opens and closes, and DeviceChain reads that directly. What has to be equipped for it is the *instance*, not the device: reading those connections needs a NATS system-account credential and an event source pointed at this instance's own broker, and until it has both the tap stays off and MQTT devices stay inferred. An instance that *had* a tap and then loses that credential is a different case from one that never had it — the devices it already asserted would otherwise stay frozen at whatever they last reported, so it hands them back to inferred presence instead. See [returning a device to inferred presence](../deployment/edge-services.md#demoting-a-device). `dcctl bootstrap` mints that credential and wires that source, so an instance brought up that way asserts MQTT presence with no further work; a bare `helm install` leaves the credential empty and does not. [Confirming the broker tap is actually running](#confirming-the-tap) below is how you tell which one you have.
- **[Sparkplug-B](./sparkplug.md)**, whose BIRTH and DEATH messages are exactly these explicit connect/disconnect signals.
- **[LwM2M](./lwm2m.md)**, whose registration lifecycle — register, periodic update, and deregister (or a lapsed lifetime) — does the same.

Two details of the MQTT case are worth knowing before you build on it:

- **It follows the device's main connection.** A device may open extra connections by appending its own suffix to its client id (the two-terminal `mosquitto_sub` / `mosquitto_pub` workflow). Those extra connections are deliberately ignored for presence: the device's state follows its primary session, so closing a side connection never makes a connected device read as offline. A device that *only* ever connects with a suffixed client id is not asserted at all and stays inferred.
- **It covers devices on this instance's broker.** A device reaching DeviceChain through a broker you run yourself is not something DeviceChain can observe connections on, so it stays inferred.

The consequence of that skip is deliberate, and worth understanding before you rely on it: **an asserted device has no inactivity backstop.** Its offline signal can only come from the transport, so if that signal never arrives — a Sparkplug death certificate lost along with the connection, or an LwM2M device whose registration lifetime has not yet lapsed (LwM2M's own default is 86400 seconds, a full day) — the device keeps reading online with nothing to correct it. What to watch for, and how to bound the window, is in **[Running the Edge Services](../deployment/edge-services.md)**. If the transport that would have said so is gone for good rather than merely quiet, [releasing the device back to inferred presence](../deployment/edge-services.md#demoting-a-device) is what puts a correction back within reach.

For MQTT devices, DeviceChain closes that gap itself rather than leaving it to you. There is one case where the broker cannot tell you a device has gone: when the broker itself restarts, the connections it was holding simply vanish, and no disconnect is ever announced for them. So DeviceChain periodically compares the broker's live connection list against what it believes, and corrects the difference in both directions — devices it did not know were connected, and devices it thinks are connected that the broker is not holding. Devices that reconnect after a broker restart are corrected by their own reconnect; the rest are corrected by a later comparison — one that can account for the whole cluster, per the paragraph below and the [scale-down caveat](#resizing-the-broker-cluster).

That comparison deliberately declines to mark anything offline unless it can account for **every** node of the broker cluster. If one node is slow or unreachable, its devices are missing from the list and are indistinguishable from devices that have genuinely gone — and wrongly marking a live device offline is the more damaging error, because everything keyed on presence acts on it: the device reads offline on its Connectivity tab, and a [Connectivity rule](./event-processing.md#condition-types) raises a disconnect alarm for a device that was reachable the whole time. In that situation DeviceChain still marks newly-seen devices online and simply waits for the next pass to make the offline call.

The presence *source* is surfaced wherever it changes what a reading means. The console's Connectivity tab names it — *Reported by the transport* or *Inferred from activity* — and distinguishes a device the transport reported **Disconnected** from one that is merely **Offline**, meaning nothing has arrived recently, which is also exactly what a healthy device on a slow reporting interval looks like. The [MCP](./mcp.md) `get_device_state` tool returns `presenceSource` alongside the state and tells the assistant not to report an inferred inactive device as down. And it is readable programmatically: `presenceSource` is a field on `device-state`'s `DeviceState` type, returning `ASSERTED` or `INFERRED`.

## Why the distinction matters

Inferred presence is convenient but laggy and ambiguous: "offline" only means "hasn't spoken recently," which is slow to notice a real disconnect and blind for devices that report on a long interval. Asserted presence is immediate and unambiguous — a disconnect is a disconnect the instant the transport reports it — which is what you want for anything you'll alarm or act on.

Keeping the two modes as an explicit per-device flag means a device on a connectionless transport keeps its familiar timeout behavior, while a device on a presence-aware transport gets the authoritative signal, and the two never interfere.

:::note Status
Device presence — both inferred and asserted — is available, with three presence-asserting transports: plain MQTT on DeviceChain's own broker (which asserts only once the instance has the system-account credential described above), [Sparkplug-B](./sparkplug.md), and [LwM2M](./lwm2m.md). A **detection rule can fire directly on a connect/disconnect edge**: the [Connectivity condition](./event-processing.md#condition-types) raises an alarm the instant an authoritative disconnect arrives and resolves it on reconnect — no timeout to tune. The engine evaluates it today, but neither console authoring surface offers it yet — the form builder and the automation canvas both omit the condition type, so a connectivity rule is defined by sending the rule directly to the API. **Do not open one in the console's form editor**: it does not recognise the type, so it reads the rule back as a threshold rule and saving replaces the original definition, with no warning. (The canvas refuses it properly, naming the unsupported type.) It complements the timeout-based Absence rule (authoritative death vs. inferred silence), and the two are meant to be paired. An authoritative disconnect also updates the device's live state, so the Connectivity tab shows the device offline the moment the transport reports it.
:::

## Running it

Presence is only as good as the signal behind it, and the two asserting edge transports each run as a
single owning replica — which gives presence a few operational properties worth knowing before you
alarm on it: what a changeover costs, why an asserted device can be stuck online, and how to bound
that. Those are covered in **[Running the Edge Services](../deployment/edge-services.md)**.

Three more properties belong to broker-asserted MQTT presence specifically.

### Confirming the broker tap is actually running {#confirming-the-tap}

Reading connections off DeviceChain's own broker needs four things, and if any is missing the tap
**declines to start**. It logs why, sets `presence_tap_off{reason}` to say which one, and MQTT devices
that were never asserted stay inferred — which looks exactly like an instance that never had asserted
presence, because functionally it is one. The four:

- `brokerPresence.enabled` is not set to `false`
- a NATS system-account credential is configured (`dcctl bootstrap` mints one; this is the usual
  reason a hand-assembled instance has no tap)
- at least one event source points at the platform's own broker — with none, there are no
  connection advisories to read
- service-to-service calls are configured. Without them the tap would run with no repair path, so
  it stays off deliberately rather than half-working: a device whose disconnect the broker never
  announced would read as connected forever

That is the whole story on an instance that never had a tap. An instance that **did** has a second
problem: the devices already marked asserted keep whatever presence they last had, because an
asserted device is exempt from the inactivity sweep and a data event cannot flip it. So for the
first two reasons above — a written `enabled: false`, and a missing system-account credential —
`event-sources` releases those devices back to inferred presence itself. Both are configuration
every replica reads identically, which is what makes releasing a whole fleet on the strength of them
safe to automate.

**A broker the tap cannot reach releases them too**, and for a stronger reason than configuration:
the MQTT gateway devices connect through lives in that same broker, so while it is unreachable no
device is connected through it either. The tap gives the connection thirty seconds to come up before
it decides, so this is half a minute with no system-account connection rather than one failed
attempt — long enough that a broker restarting alongside the services is not mistaken for one that
is gone.

**For this one reason the two-minute wait is a re-check rather than a delay**, and the difference is
what stops the release outliving the outage it was a response to. Before each pass — the first one
included — the service re-dials the system account. If the broker answers, nothing is released: the
service **exits, and the pod restarts** with a tap that comes up normally. So a broker that returns
produces a pod restart, not a fleet of released devices. The other two release paths cannot work
this way and do not need to: both are configuration read once at startup, so what their window waits
for is the *replacement pod* a configuration change rolls out.

Nothing is released automatically for the remaining three: no source pointed at the platform broker,
no service-to-service configuration, and a subscription that fails on a connection that *did* reach
the broker. `dcctl presence demote` is the door in those cases. Both are described in
[returning a device to inferred presence](../deployment/edge-services.md#demoting-a-device).

**Two signals tell you the tap is not running, and they cover different failures.**

`presence_tap_off{reason}` is the direct one. It goes to 1 on every path where the tap declines to
start, with the label naming which, and it answers a question a quiet fleet otherwise makes
unanswerable: a long-lived MQTT fleet legitimately emits no connect or disconnect advisories for
days, so nothing about the ordinary flow of presence events distinguishes an instance that is
asserting presence from one that silently never started.

It does not cover a tap that started and then stopped working, because in that case nothing ever
declines to start. **That is what `presence_canary_missed_total` is for, and it is the counter to
alarm on.** The service opens its own MQTT connection once a minute purely so that a working tap has
something to observe: `presence_canary_observed_total` rises on a healthy tap, and
`presence_canary_missed_total` rises when the chain is broken.

Read the presence counters as traffic, not as health. `presence_events_total` is legitimately flat on
a quiet fleet — and legitimately *not* flat on a tap that has just been switched off, because
releasing the devices that tap had asserted emits one event per device under
`presence_events_total{state="demoted"}`. Neither shape says anything about whether presence is being
read.

The canary runs on its own schedule, independently of the repair pass described below. That
separation is what makes the counter trustworthy: an instrument that could only report while the
thing it watches was healthy would go quiet at exactly the moment it mattered.

### Tuning the tap {#broker-presence-settings}

The tap ships with working defaults and most instances never change them. They live under the
`event-sources` area's `brokerPresence` configuration.

| Setting | Default | What it does |
|---|---|---|
| `enabled` | on when unset | Runs the tap. Set it to `false` to turn broker-asserted MQTT presence off deliberately — on an instance whose broker is shared with something that objects to a system-account subscriber, say. MQTT devices then run on inferred presence, and any the tap had already asserted are released back to it, paced, over the following minutes. |
| `reconcileSeconds` | `300` | How often the broker's live connection list is compared against the platform's own, in both directions. **This is not a backstop.** A graceful broker restart announces no disconnects at all, so this pass is the only thing that ever corrects those devices — and an asserted device has no inactivity sweep behind it. Lower it for a faster repair, at the cost of one cluster-wide inventory plus one read per tenant on every pass. |
| `canarySeconds` | `60` | How often the service opens its own MQTT connection to prove the tap is still live. This is the schedule `presence_canary_missed_total` counts against. |
| `canaryDeadlineSeconds` | `15` | Bounds one probe. Too tight and it reports failures the tap does not have. |
| `inventoryGatherSeconds` | `5` | How long a pass collects replies from the broker cluster. Too short and a merely slow node reads as absent, which withholds every disconnect that pass. |

A non-positive value on any of the four intervals falls back to the default above rather than to zero.

### A repair pass that runs out of time says so {#reconcile-pass-timeout}

Each repair pass walks every tenant on the instance and reads that tenant's asserted devices, so a
large instance's pass is a long one. It is bounded: a pass that cannot finish inside its budget
stops, reports `presence_reconcile_runs_total{outcome="timeout"}`, and logs how many tenants it
covered.

**The next pass then resumes at the tenant it did not reach**, rather than starting again from the
beginning. Without that, a fleet whose pass never fit in the budget would repair the same first few
tenants on every attempt and never reach the rest — not late, never. Occasional `timeout` outcomes
mean repairs are lagging and every tenant still gets its turn; a *sustained* run of them means the
instance needs more headroom, and the devices at the back of the rotation are the ones whose
missed disconnects go uncorrected longest.

`presence_reconcile_runs_total` carries one outcome per pass, and they are worth telling apart:

| Outcome | What it means |
| --- | --- |
| `complete` | every tenant was walked against a fully accounted-for broker cluster |
| `partial` | the pass ran, but not every broker node answered — devices were only ever marked **online**, never offline |
| `timeout` | the pass ran out of its budget; the tenants it did not reach go first next time |
| `failed` | the pass could read nothing — no broker inventory, no tenant list, or **no tenant's presence state**. Reconciliation did nothing at all |
| `cancelled` | the service was shutting down mid-pass. Not a fault |

The `failed` outcome is the one to alarm on alongside the canary. A single tenant's state read
failing is tolerated — the other tenants still get their pass — but *every* read failing means
repairs have stopped entirely, which is what a device-state outage looks like from here.

### Presence transitions are metered against the tenant's ingest ceiling {#presence-and-the-ingest-ceiling}

Connect and disconnect transitions pass the same per-tenant [ingest
limit](./governance.md) as telemetry, and are **refused when a tenant is at its ceiling** —
counted in `presence_events_refused_total`.

**A demotion goes through the identical gate**, which is worth planning for on its own terms: a
tenant pressed against its ceiling can have its *repair* refused along with the churn causing the
pressure. Nothing is lost — a refused release leaves the device asserted, so the next pass finds it
again — but the repair arrives no sooner than the ceiling allows.

This is deliberate rather than an oversight. Connection churn is entirely device-controlled and
otherwise free: a device reconnecting in a loop would be an unmetered write amplifier that the
ingest limiter never sees. But the consequence is worth planning for — a tenant pressed against its
ceiling has devices whose online/offline state is wrong, and stays wrong until a later
reconciliation pass repairs it (by default, up to five minutes). Anything keyed on presence is
wrong for that window too, including Connectivity rules and the release of commands held for an
offline device.

It applies to the platform broker's MQTT tap. Sparkplug ingestion applies no per-tenant ceiling and
sheds nothing, and LwM2M runs its own separately configured limit.

### Resizing the broker cluster requires restarting `event-sources` {#resizing-the-broker-cluster}

The comparison above declines to mark anything offline unless it can account for every node of the
broker cluster. It decides "every node" from the largest cluster it has ever seen — a mark that
only ever rises, which is what stops a network partition from causing mass false disconnects (a
route-isolated broker reports itself as the whole cluster and would otherwise satisfy its own
check).

The cost of that design is one case it cannot distinguish: **deliberately scaling the broker
cluster down**. Fewer nodes answer than the remembered maximum, so every subsequent pass is treated
as incomplete and no offline repair is ever made — not until the next pass, but for the life of the
process. Devices orphaned by the removed node read online indefinitely, and because an asserted
device has no inactivity backstop, no timeout corrects them.

**After scaling the NATS cluster down, restart `event-sources`.** The signal that you needed to and
did not is `presence_reconcile_withheld_disconnects_total` rising without settling. Scaling *up*
needs nothing.

If the restart has to wait, the orphaned devices do not have to. `dcctl presence demote` on that
source releases them back to inferred presence, where the ten-minute inactivity sweep can mark them
offline on their own evidence — see [returning a device to inferred
presence](../deployment/edge-services.md#demoting-a-device). It repairs the devices that are already
wrong; the restart is still what stops the next ones going wrong.
