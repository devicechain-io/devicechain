---
title: Edge Transports and Device Presence
status: draft
audience: engineering reference — see "Publishing this" at the end
adrs: [ADR-067, ADR-068, ADR-069, ADR-070, ADR-075, ADR-014, ADR-016, ADR-023, ADR-030, ADR-042, ADR-044, ADR-048, ADR-051, ADR-057, ADR-077]
---

# Edge Transports and Device Presence

Three components sit at DeviceChain's edge and none of them is a device driver in the usual sense.
Two are **presence-asserting transports** — Sparkplug-B and LwM2M — that know when a device is
actually gone rather than merely quiet. The third is a **standalone agent** that runs on a box at a
site, spools locally, and forwards to the cloud.

What ties them together is one idea worth stating before any of the detail: **presence is a
claim about the world that has to survive the platform restarting.** Neither transport keeps durable
state of its own, so every mechanism below — the host-minted epoch, the fenced lease, the
reconstruct-from-projection dance on failover — exists to stop a restart either resurrecting a dead
device or killing a live one.

Every claim below names the file it came from.

## The shape, end to end

```
   Sparkplug-B                     LwM2M                        a site with no cloud link
   (customer's broker)        (device → our DTLS socket)        (devices → local MQTT)
        │                              │                                  │
  sparkplug-ingest               lwm2m-ingest                      dc-edge-agent
  connects OUT as a Host         binds UDP 5684, PSK               embedded broker + JetStream
  Application; leader-only       terminated; leader-only           spool; forwards over MQTT
        │                              │                                  │
        └────────── presence + measurements ──────────┐                   │
                                                      │                   ▼
                                             inbound-events  ◄──── the cloud's MQTT gateway
                                                      │                (no special path)
                                          device-management: resolution
                                                      │
                                              resolved-events
                                                      │
                              ┌───────────────────────┼──────────────────┐
                        device-state              event-management   event-processing
                        presence projection       state_change_events  connectivity rules
```

The edge agent is deliberately **not** a fourth ingest path. It re-publishes onto the ordinary
device MQTT topics so the cloud's existing gateway capture ingests it, which is why the platform
has no agent-shaped seam anywhere.

## 1. What presence actually is

One row per device in device-state (`backend/services/device-state/model/model.go:26-59`),
tenant-scoped, holding `Active`, the connect/disconnect/activity stamps, and the three fields that
carry the whole design: `PresenceSource` (`INFERRED` or `ASSERTED`), `SessionId`, and
`PresenceTime`.

There is exactly **one writer**: `MergeDeviceState`
(`backend/services/device-state/model/api.go:104-208`), driven for *every* resolved event, plus the
inactivity sweep. It is a row-locked read-modify-write because five decode workers can race on one
device.

**`PresenceSource` is a one-way promotion.** A device becomes `ASSERTED` the first time an
authoritative transport speaks for it, and **nothing demotes it** — there is no code path back to
`INFERRED` anywhere in the tree. That matters more than it looks; see §7.

**Three states, not four.** `Active` × `PresenceSource`. "Unknown" is representable only as *no
row*, before the device's first event.

### The ordering predicate

`backend/core/presence/presence.go:37-76` — `Decide` is the one place ordering is decided, and both
consumers call it, so the projection and the detection engine cannot disagree.

It returns three flags rather than one, and the split is the interesting part
(`presence.go:28-36`): `Ordered` (should this advance the marker at all), `NewSession`, and
`Flipped` (did the state actually change). An earlier fused guard conflated "advance the marker"
with "the state changed", which is precisely the bug that lets a stale re-derivation tear down a
live session.

**A session's BIRTH and its DEATH share one epoch** — a Sparkplug death certificate is built at
connect time and fired later by the broker — so "newer session wins" alone is wrong, and at an
equal `(session, time)` only DISCONNECTED-over-CONNECTED applies.

### The session identity

`backend/services/event-sources/adapter/epoch.go:44-53`: a **host-minted** UnixNano, floored to
`last+1` so it is strictly monotone even across an in-process clock step-back. Shared by both
transports — one implementation, not two.

It is deliberately **not** device-supplied. Sparkplug's `bdSeq` is read only to correlate a death
with its birth and is **never** ordered by magnitude (it rolls 255→0, so a raw compare inverts at
the wrap). LwM2M's registration id is separate randomness and is never the session id.

🔴 **Cross-restart monotonicity is conditional, and two comments overstate it.**
`backend/services/sparkplug-ingest/host/session.go:148-152` and `epoch.go:65-69` both say a restart
"can never mint an epoch the projection rejects as stale". The counter re-seeds to zero on process
start; the only cross-term floor is read back from the device-state projection, and that read is
**skipped** when the reconciler is absent, when the query errors or times out, and when the result
set is empty (`backend/services/sparkplug-ingest/host/host.go:440-456`). The guarantee holds on the
success path only.

## 2. Tenancy — the invariant, and where it stops

**Both transports establish tenancy from the CONNECTION, never from device-supplied content.** This
is the single most important property in the arc, and both hold.

*Sparkplug*: the `Client`'s tenant field is assigned once per broker connection
(`backend/services/sparkplug-ingest/host/host.go:206-208`) and read as a struct field at every
ingest call. The topic's group id reaches only an external id, a local map key, an outbound topic,
and logs. Config forbids two tenants on one broker endpoint, because the only thing that could then
distinguish them is the publisher-controlled group id
(`backend/services/sparkplug-ingest/config/configuration.go:174-177`).

*LwM2M*: the tenant comes from the DTLS PSK `IdentityHint`
(`backend/services/lwm2m-ingest/registry/identity.go:23-36`), and the client-supplied endpoint name
`ep` is read in exactly one place — a debug log. Pinned over a **real handshake with a hostile `ep`**
(`backend/services/lwm2m-ingest/registry/integration_test.go:116-140`) and again against a real
Leshan client.

🔴 **Where it stops.** Both transports mark their events `AuthenticatedTransport`, which lets the
resolver trust a self-asserted device token under `deviceAuthMode: required`
(`backend/services/device-management/processor/event_resolver.go:611-642`). For LwM2M the token is
bound to the authenticated PSK identity, so that is per-device. **For Sparkplug the token is derived
from the topic, so the authentication is broker-level** — `required` does *not* close intra-tenant
device-token spoofing on that path. Cross-tenant stays closed. Documented in three files that agree.

**A device cannot forge its own presence**: the device-facing JSON decoder rejects `StateChange`
outright (`backend/services/event-sources/processor/decoders.go:195-203`), because a device could
otherwise assert itself permanently connected with an unbeatable session id — the sweep skips
asserted devices and no data event flips one back.

## 3. Sparkplug — a state machine with one observable

The service connects **out** to the customer's broker as a Host Application; it is not a broker.
Auto-reconnect is off deliberately (`host/host.go:270-274`): paho would reuse the frozen last-will
timestamp, and Sparkplug 3.0 requires the death certificate and the birth of one connection to carry
the *same* timestamp so an edge node can reject a delayed death from a prior session.

The client id is **identical across every replica** (`host/host.go:79-86`) so the broker's duplicate-id
takeover kicks a zombie leader off. Making it per-pod unique would defeat that.

**Topic parsing fixes the level count by message type** rather than accepting four-or-five
(`host/topic.go:100-106`), because a device-type message at four levels would otherwise be
mis-classified and corrupt the device table.

The session machine (`host/session.go`) has one observable — a rebirth request — and every error
funnels into it. The subtle part is the three-way NBIRTH disambiguation (`:283-302`): a repeat with
the same `bdSeq` is a QoS-1 redelivery (do nothing), *unless* a rebirth is pending, in which case it
is the commanded rebirth (re-adopt the sequence baseline, keep the epoch, emit no CONNECTED).
Without that second case the node's next data reads as a gap and it rebirths forever.

Sequence handling is uint8 arithmetic so 255→0 is a normal advance, and **a value above 255 is
rejected rather than truncated** — `uint8(257) == 1` would otherwise be accepted as a valid successor
after seq 0.

🔴 **A stranding path the code does not comment on.** `onNBirth`'s new-session branch replaces the
whole node session (`session.go:303-316`) and discards the previous session's device table — child
devices holding live epochs and a live CONNECTED in the projection — **with no cascading
DISCONNECTED**, unlike `onNDeath`, which does cascade (`:358-363`). A device born under the old
`bdSeq` that does not re-birth is stranded connected until the next failover reconcile.

**Failover reconciliation** is two-phase (`host/host.go:543-621`) and its fail-safes are the design:
the epoch floor is read **before subscribing** so no birth on this session can mint a stale-rejected
epoch; the probe only force-disconnects a node it actually **reached on the wire**; and a broker drop
mid-window aborts **without emitting**, because a probe gone deaf must never declare a mass death.

## 4. LwM2M — registration as the presence signal

CoAP over DTLS on UDP 5684, one hard-pinned cipher suite, Connection ID on by default
(`backend/services/lwm2m-ingest/server/server.go:199-207`). An unprovisioned identity is refused at
the handshake and the error **does not echo the identity**, so a probe cannot enumerate provisioned
credentials by differential error.

Registration state is **purely in-memory** (`registry/registry.go:136-167`); durability is only the
emitted state change. Registration emits CONNECTED **before** the CoAP reply, so a failed emit
returns 5.03 and installs no entry. Update refreshes the lifetime and emits **nothing**.

`idleTimeoutSeconds: 0` actively installs a **nil** inactivity monitor (`server.go:164-172`), because
go-coap's default would otherwise reap a healthy queue-mode sleeper at 16 seconds and defeat the
point of Connection ID.

**Observations are the gap.** One observe per object *instance*, only for objects in a hard-coded
IPSO range, at most 32 per registration. Across a restart or a leadership handover **every
observation is lost and none is reconstructed** — the package says so
(`observe/manager.go:28-32`): recovery waits on the device's own re-registration behaviour, which is
device-controlled and bounded only by the lifetime, up to the 86400-second default. Presence is
reconstructed; telemetry is not.

🔴 **Two comments claim the object allowlist is configurable** (`decode/links.go:26`, `:45`) and an
operator runbook tells a reader to add Object 5 to it. **There is no such knob** — the allowlist is a
package constant and Object 5 is outside its range. An instruction a reader cannot follow.

**Only SenML-JSON is decoded** on the notify path (`decode/senml.go:75-82`). The consequence is
named as a boundary in the code and nowhere in the docs: a conformant **LwM2M 1.0-only client gets
presence and commands but zero telemetry**, because SenML arrived in 1.1.

## 5. The edge agent

Its own module (`backend/edge/dc-edge-agent`), a single binary, and explicitly not the platform. It
embeds a NATS server as a device-transparent MQTT gateway, captures publishes into a local JetStream
file store, and forwards over an MQTT uplink so the cloud's ordinary gateway capture ingests them.

**Two-phase start** (`agent/agent.go:345-382`): create the capture stream with MQTT **disabled**,
then start with it enabled — closing the window where a device could be acknowledged with nothing
capturing. `DontListen: true` means the plain NATS port is never bound; the MQTT gateway is the only
LAN surface.

**The spool is a drop-oldest ring**, and the direction is argued (`agent.go:480-488`): the embedded
gateway acknowledges a device from its *own* persistence, decoupled from the capture stream, so
drop-newest would lose the newest event *after* the device was told it was durable.

🔑 **The drop count is deliberately not derived from the consumer's ack floor** (`agent.go:19-23`) —
the server drags that floor past limit-evicted messages on restore, which would silently erase the
evidence of loss. It is computed from the stream's first sequence instead, ratcheted monotonic.

**Idempotency is per-decoder.** `stampIdempotency` (`agent/idempotency.go:45-82`) splices a replay-
stable `altId` and `occurredTime` derived from immutable stored-message metadata, so a redelivery
folds at the cloud's uniqueness index — **but only for JSON object payloads**. Anything else is
forwarded verbatim and is at-least-once. A literal JSON `null` counts as absent, because the cloud
decoder would otherwise default the time to *now* on each decode.

Ack discipline is `AckSync`, not fire-and-forget, because a counted-but-unprocessed ack would
permanently inflate the acked count and clamp real evictions into invisibility. A malformed subject
is counted **and acked**, because it is not transient and would otherwise poison the FIFO head
forever.

**It is not a chart functional area** — verified: it appears in no area list, no schema enum, no
profile, and `--enable-area` cannot name it. It ships as static binaries and a ko-built image, and
is deployed by container, systemd unit, or a hand-written Kubernetes manifest.

🔴 **Its release tarballs never reach the download manifest.** `hack/build-release-manifest.sh:59`
filters checksums to `dcctl_` and builds a dcctl-only artifact table, so the website's download menu
offers dcctl alone even though the publish job depends on the edge-agent job for ordering.

## 6. Running one of anything

**All three are single-instance, for three different reasons.** Sparkplug because two Host
Applications would publish duplicate STATE and duplicate telemetry. LwM2M because **DTLS is stateful
over a single bound UDP socket** — a warm standby that also bound it would silently receive and drop
half the datagrams a Service sprays across replicas. The agent because two of them on one store
collide on file locks and two with the same id kick each other off the cloud broker in a loop.

The two ingest services take a fenced lease (30-second TTL, renewed at a third of it) and a standby
connects nothing. The LwM2M transport is **rebuilt per leadership term** because it binds at
construction and its stop is one-shot.

Neither edge service ever gets a PodDisruptionBudget — the template skips any area at one replica.

🔴 **Nothing in the chart or in OpenTofu exposes UDP 5684 outside the cluster.** Every area Service
is ClusterIP, `sessionAffinity` appears nowhere, and the ingress controller is HTTP-only. The values
file calls external exposure "an operator/ingress decision"; there is no implementation here, so a
real LwM2M fleet cannot reach the service as shipped.

**Observability is cardinality-clean and almost entirely unwatched.** Across all three trees there
are exactly three label-supply sites, all bounded to small constant sets, and no per-device or
per-tenant label anywhere. But of roughly 83 emitted edge metrics, **none has an alert and none has a
dashboard** — the only rule group and the only Grafana board are hardcoded to the event-processing
prefix. The agent's own runbook asks for three alerts that do not exist, and there is no
`sum(is_leader) != 1` alert on either leased service, which is the most load-bearing HA signal on the
whole surface.

The rules gate (`hack/check-prometheus-rules.sh`) cannot see this: its positive control lists only
the two database groups and the replication group, so **the total absence of application alerting is
invisible to it by construction.**

🔴 **Sparkplug has no per-tenant ingest rate limit and no shed.** LwM2M has a two-stage limiter;
Sparkplug's only governance touchpoint is the tenant-lifecycle gate. The reason is written down —
its exposure is an opt-in broker an operator deliberately connects to — and it can adopt the same
limiter unchanged.

## 7. The failure that has no backstop

**An asserted device that dies without saying so can read online indefinitely.**

The inactivity sweep marks a quiet device offline after ten minutes — but it **explicitly skips
asserted devices** (`backend/services/device-state/model/api.go:341`), and nothing else on the
asserted side of device-state has a TTL, a sweeper, or a watchdog. Only a new state change from the
device's own source can clear it. So:

- A lost Sparkplug death certificate leaves the device online until the next reconcile probe, which
  runs **only on a client reconnect** — a steadily-connected Host that simply never hears from the
  node again never re-runs it.
- The NBIRTH stranding in §3 leaves child devices online with nothing to correct them.
- An LwM2M device is bounded by its lifetime plus grace, but the default lifetime is **86400
  seconds**, and only the maximum is configurable.
- A device whose asserted source is removed entirely can never return to inferred, so it is stranded
  at its last asserted state permanently.

The published page presents the sweep's skip as safe **because "if it had died the transport would
have said so"** — which is a claim about the transport, not about the code. The skip is correct; the
justification is a dependency on reconcile paths that can each fail.

## 8. Things that exist in name only

Each of these reads as a feature and is not one.

- **`InactivityTimeout` is documented as a per-device override** (`device-state/model/model.go:46`)
  and has **no write path** — device-state's GraphQL schema declares a query and no mutation, so
  every row holds the compile-time default of 600.
- **`InactivityAlarmTime`** is written by the sweep, cleared on reconnect, and exposed over GraphQL —
  and **read by nothing**. Despite the name, no alarm is raised from it.
- **`state_change_events` has no read surface.** The hypertable is written and indexed for
  idempotency, and there is no query field for it anywhere, so the presence timeline its model
  comment advertises as "the history DETECT/audit reads" is write-only.
- **`presenceSource`, `sessionId` and `presenceTime` are selected by no client** — neither the
  console nor the MCP tool asks for them. The asserted-versus-inferred distinction the whole
  presence page is about is invisible in every user interface.

## 9. What is validated, and what is not

The strongest asset is the **Leshan interop suite**
(`backend/services/lwm2m-ingest/registry/leshan_interop_test.go`, behind a build tag). Its design is
the point: the ordinary integration test drives the server with *our own* client, so an
encoder-plus-decoder bug would cancel out; this one drives it with an independent Java stack, wires
the full telemetry path rather than presence alone, and reads its verdicts from **server-side seams**
so a client that lies cannot manufacture a pass. It runs weekly and is explicitly **advisory, not a
gate**.

The best negative control in the arc is `TestCidRoamingSurvivesAddressChange`
(`backend/services/lwm2m-ingest/server/server_test.go:361-393`), which runs both arms — with
Connection ID the session survives an address change, and **without it the roamed record is
stranded** — so the positive result proves the mechanism rather than luck.

What is **not** validated: no rig exercises any Sparkplug or LwM2M functionality. `hack/ha-rig.sh`
deploys lwm2m-ingest only as a lease-holder fixture so its fencing check has something to assert on.
**Nothing drives a real third-party Sparkplug edge node or broker anywhere in the tree.** And nothing
validates the edge agent beyond its own unit tests — no rig, no drill, no in-cluster deployment.

There is also **no end-to-end presence test**: nothing runs an unresolved state change through
resolution, the projection, and the detection engine in one pass. The strongest presence test
(`backend/services/device-state/model/api_test.go:156-239`) is excellent on the merge algebra and
carries a structural limit worth knowing — its harness migrates the **live** model onto SQLite rather
than running the frozen baseline, and SQLite ignores `SELECT … FOR UPDATE`, so the row lock the
concurrency argument rests on is untested by construction.

## Publishing this

`docs/docs/deployment/edge-services.md` is the user-facing page derived from this one, with a
Spanish mirror and links in from the presence, Sparkplug and LwM2M concept pages.

The split is by audience. The published page carries the contract: how many instances run and why,
what a failover costs, what presence guarantees and what it does not, the lifetime and timeout knobs
that actually matter, and what to check when a device reads online and is not. This document carries
the mechanism: the three-way birth disambiguation, why the drop count avoids the ack floor, why the
epoch floor is read before subscribing.

The body carries no decision-record references so that it and anything derived from it are
publishable as-is; the frontmatter holds the pointers.
