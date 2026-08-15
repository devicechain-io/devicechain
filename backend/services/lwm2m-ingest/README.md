# lwm2m-ingest

The DeviceChain **LwM2M ingest adapter** (ADR-075): a stateful service that terminates
**OMA LwM2M over CoAP/UDP+DTLS** from constrained devices and folds their registration
(presence), telemetry, and commands onto the one canonical device-management model —
the second standards-native edge protocol alongside Sparkplug B (ADR-069). LwM2M rides
CoAP over UDP/DTLS (a transport DeviceChain did not have) and targets
constrained/cellular/LPWAN devices; Sparkplug rides MQTT and targets industrial/SCADA
edge. They are **complementary adapters onto the same model**, not competitors.

Like the Sparkplug adapter, ~80% of this is reuse — presence (ADR-067), the
measurement pipeline (ADR-045), command-delivery (ADR-043), and the stateful-adapter +
fenced-lease machinery (ADR-069/070). The genuinely new build is the **CoAP/DTLS
transport + the LwM2M semantic layer** (registration, Observe/Notify, object model,
downlink), built on `plgd-dev/go-coap` (Apache-2.0) + `pion/dtls` (MIT) — both licenses
clean for the pure-Apache wedge; the LwM2M semantic layer is ours (no Go Leshan exists
to adopt).

## What this service does

The full ADR-075 arc has shipped: transport, registration/presence, Observe→measurements,
leader election with failover reconstruction, and downlink commands including the
queue-mode hold-and-drain. The packages, in the order a device meets them:

- **`server/` — the DTLS-PSK CoAP transport.** One shared UDP socket carrying many
  concurrent long-lived sessions, plus a trivial health resource (`/dc/health`) that
  proves the DTLS→CoAP path end to end. **Fail-closed authentication**: a device is
  authenticated against a provisioned PSK credential map (identity → key), and an
  unknown identity is refused (counted). Bounded by a `maxSessions` ceiling on the
  live-session table and an optional idle-session reaper — the memory-safety / DoS
  bounds a UDP-facing service owes.
- **`registry/` — the LwM2M `/rd` registration interface and the presence it drives.**
  Register → `CONNECTED` (ASSERTED, ADR-067), Update refreshes the lifetime, Deregister
  or a lapsed lifetime → `DISCONNECTED`. Every transition is the same `StateChange` the
  Sparkplug adapter feeds, ordered by a shared session epoch. Lifetimes are clamped both
  ways (a floor against `lt=1` re-register churn, a ceiling that bounds the failover
  blackout), and a re-Register storm inside `DefaultReplaceBackoff` returns the existing
  location without minting a new session. `AutoRegister` on a credential creates the
  device row on first registration; without it an unknown device's registration is
  dropped and counted.
- **`observe/` — the telemetry lifecycle.** After a Register, the server issues an
  Observe (a downlink GET with `Observe`, `Accept: SenML-JSON`) against each of the
  device's telemetry object instances **on the same authenticated conn the registration
  arrived on** — CoAP is symmetric. The per-identity slot is a compare-and-swap guarded
  by the session epoch, so a re-handshaked sleeper that sends only an Update
  re-establishes on its new conn (without that heal a device is `CONNECTED` but
  telemetry-dark until its lifetime lapses), a conn close *parks* the slot rather than
  deleting it, and a session that truly ends is tombstoned so a late in-flight Establish
  cannot resurrect a zombie observation.
- **`decode/` — transport-free SenML/IPSO + CoRE-Link decoding.** Bytes in, values out:
  `Samples` turns a Notify payload into measurement samples, `Observations` picks the
  object instances worth observing out of a registration's link-format body. Being
  transport-free is what makes the semantics golden-testable without a live CoAP server.
  The observe allowlist is `DefaultObjectAllowlist` — the IPSO sensor range 3200–3441 —
  and it is **a compile-time constant, not configuration** (`main.go` passes it
  directly).
- **`session/` — the one shared primitive.** `ConnDead` answers "is this connection still
  alive?" identically for `observe/` and `downlink/`. It checks **both** `Done()` and
  `Context().Done()` because go-coap's teardown order leaves a window where only one has
  fired; two copies of that subtlety would drift.
- **`downlink/` — command dispatch.** A CoAP device cannot subscribe to NATS, so this
  package consumes the device-commands stream on its behalf and maps each command to a
  CoAP Read/Write/Execute on the live session. Offline devices are handled by the
  hold-and-drain path (`HELD` / `PARKED` → claim → dispatch on the next wake). See
  **[COMMANDS.md](COMMANDS.md)** — the command keys, the payload rules, the park/claim
  mechanics, the FOTA runbook and the named limitations all live there.
- **The per-tenant ingest limiter (ADR-023, `buildIngestLimiter` in `main.go`).** A
  two-stage token bucket meters device messages *and* decoded samples per tenant. It
  gates the `/rd` path as well as Notify, because a Register or Update does real work —
  mint an epoch, durably emit presence, spawn up to `MaxObservationsPerRegistration`
  downlink Observe exchanges — so an authenticated device could otherwise flood durable
  writes past the ceiling the telemetry path enforces. Per-tenant overrides are resolved
  from user-management and **fail open to the platform default**; the default itself is
  forced positive, so a misconfiguration can never become *unlimited* nor *zero*.
- **The tenant-lifecycle gate (ADR-077).** Actuation is refused for a tenant that has
  been through the delete door. It sits at the shard worker's task union rather than on
  the two callers, so a third task kind cannot be added without one — and the wake drain
  needs it most, since it can fire long after the delete from a `(tenant, token)`
  remembered from an earlier registration.

**Tenancy is bound to the authenticated DTLS identity, never parsed from the untrusted
registration endpoint name** (ADR-075 D1). LwM2M devices connect *in* to one shared
socket, so tenancy cannot be connection-scoped the way Sparkplug's is; the credential
carries `tenant` + `externalId`, and a device asserting someone else's `ep` changes
nothing.

**DTLS Connection ID (RFC 9146) is on by default.** CID lets a session survive a client's
source-address change (a NAT rebinding, or a queue-mode cellular device waking on a new
IP) without a fresh handshake. **This updates ADR-075:** the scoping spike (2026-07-21)
named CID *absent* from pion/dtls and made cellular-sleeper session-resumption the one
named GA limitation; pion/dtls v3.1.5 ships CID, fully plumbed. pion rebinds a session's
peer address only *after* a CID record AEAD-decrypts and passes anti-replay (RFC 9146
§6), so this does not open a traffic-redirection vector. `server_test.go` proves the
roaming survival, the negative control (CID off ⇒ the roamed record is stranded), and the
security property (a forged/replayed record from a spoofed source does not redirect the
session).

GA-honest scope: **PSK** credentials (X.509/RPK deferred), **SenML-JSON + text/plain**
content formats (TLV/CBOR a fast-follow — note LwM2M 1.0 clients default to TLV, so a
conformant 1.0-only client keeps presence and commands but answers the SenML Observe with
4.06 and stays telemetry-dark), **no Bootstrap server** (devices pre-provisioned), lenient
metric mapping, and no `Send` (`/dp`) handler yet.

## HA posture — one serving replica behind a fenced lease

Sparkplug connects **outbound** to a broker, so a warm standby is safe (only the leader
connects; the broker fences the zombie). LwM2M is an **inbound** bound UDP socket — a
Kubernetes Service would spray device datagrams across replicas, and a warm standby would
silently receive and drop half of them. So GA runs a **single serving replica** (ADR-070
"one shard"), and the fenced ownership lease **is wired** (`runLeadership` /
`serveAsLeader` in `main.go`):

- Every replica is **Ready**; only the **leader** builds the transport, binds the socket,
  mounts `/rd` and runs the command dispatcher. A standby binds nothing and serves
  nothing — it retries acquisition every `standbyRetryInterval` and exposes `is_leader 0`.
  Readiness is deliberately *not* gated on leadership: a standby is ready to take over.
- `KeepAlive` runs on its **own** goroutine, started immediately after acquisition and
  before the term build, so no processing stall (or a slow device-state read) can starve
  renewal into a churn loop.
- On acquisition the leader **reconstructs presence from device-state**
  (`reconstructPresence`): it floors the epoch source above the highest stored session so
  a fresh emission always supersedes a stored one, installs a shadow registration per
  asserted-active device (its lifetime timer will `DISCONNECT` a device that stays
  silent), and `DISCONNECT`s devices whose credential has been decommissioned. Tenants
  whose device-state read fails are retried **in-term**, not deferred to the next
  failover — a skipped read leaves a device stuck `CONNECTED`.
- The dispatcher is started and **waited on for `Ready()` before the transport serves**,
  so the first Register cannot land in a window where its wake-drain would silently
  no-op. That window is real precisely at failover, when the whole fleet re-Registers at
  once.
- Failure is loud rather than quiet: a `Serve` return while the term still intends to
  serve is **fatal** (a downed socket on the only serving replica is a total ingest
  outage a Ready pod would hide), and repeated term-build failures terminate the pod
  instead of leaving an invisible Ready-but-not-serving standby.

A restart loses every in-memory registration **and every observation**. Failover
reconstruction restores **presence only** — observations cannot be rebuilt without a
conn, so telemetry recovery waits on device re-Register behaviour (device-controlled, up
to the 86400s default lifetime). The one lever is clamping `maxLifetimeSeconds` down,
trading presence-write volume for a bounded telemetry blackout.

## Configuration

```json
{
  "listen": { "host": "0.0.0.0", "port": 5684 },
  "security": {
    "connectionIdLength": 8,
    "handshakeTimeoutSeconds": 10,
    "idleTimeoutSeconds": 0,
    "maxSessions": 100000,
    "identities": [
      {
        "identity": "opaque-handle-1",
        "pskEnv": "DC_LWM2M_PSK_DEV1",
        "tenant": "acme",
        "externalId": "plant-a/sensor-1",
        "deviceTypeToken": "lwm2m-sensor",
        "autoRegister": true
      }
    ]
  },
  "ingestRateLimit": { "messagesPerSecond": 1000, "burst": 2000 },
  "maxLifetimeSeconds": 86400,
  "downlink": { "timeoutSeconds": 10, "concurrency": 16 }
}
```

- **`listen.port`** — CoAPS (5684) by default.
- **`security.connectionIdLength`** — DTLS CID length in bytes; `8` by default, `0`
  disables CID (a roaming device then re-handshakes). It is a pointer field so an
  explicit `0` is distinguishable from an omitted value. Refused above 20 bytes — RFC
  9146 permits 255, but that is pure overhead on every record of a constrained radio.
- **`security.maxSessions`** — live-session ceiling (memory bound); a handshake past it
  is refused.
- **`security.idleTimeoutSeconds`** — reap a session idle this long; `0` disables
  reaping. A queue-mode deployment should set it above the expected wake interval so an
  idle sleeper's keys are not evicted (which would re-introduce the re-handshake CID
  avoids).
- **`security.identities[]`** — the fail-closed PSK credential map *and* the tenancy
  binding (ADR-075 D1). Each entry requires **`identity`**, **`pskEnv`**, **`tenant`**
  and **`externalId`**; **`deviceTypeToken`** is required whenever `autoRegister` is set.
  An entry missing any of them is refused at startup. `identity` must be globally unique
  across the adapter (there is no tenant context at PSK-callback time to disambiguate),
  but a **duplicate `(tenant, externalId)` across two identities is deliberately
  allowed** — that is the credential-rotation overlap, two live PSKs for one device.
- **`security.identities[].pskEnv`** — NAMES the environment variable holding the
  **base64-encoded** pre-shared key; never the cleartext (the chart projects a
  Kubernetes Secret into it). Refused at startup if the variable is unset, empty, not
  valid base64, or decodes to **fewer than 16 bytes** — a PSK is the device's sole
  authenticator *and* its tenancy anchor, so a weak key fails startup rather than
  silently weakening every session. A PSK identity is sent in the clear on the wire, so
  prefer an **opaque handle** over a `tenant:device` string.
- **`ingestRateLimit`** — the platform-default per-tenant ingest ceiling (ADR-023):
  `messagesPerSecond` (default 1000) is the pre-decode stage-1 rate and `burst` (default
  2000) the instantaneous allowance; the sample budget is derived from it. Non-positive
  values take the platform default, never *unlimited* and never *zero*.
- **`maxLifetimeSeconds`** — the ceiling every registration lifetime is clamped down to,
  and *identically* the lifetime a failover-reconstruction shadow timer is armed for.
  Equal by construction so a shadow expiry is only ever late-true, never a false
  `DISCONNECT` of a healthy long-lived sleeper. Default 86400 (LwM2M's own default `lt`),
  floor 60. 🔴 **A fleet using a longer `lt` MUST raise this above that `lt`**, or those
  devices are expired on every handover.
- **`downlink`** — `timeoutSeconds` (default 10) bounds one CoAP command exchange;
  `concurrency` (default 16) is the device-sharded worker count, which sets cross-device
  parallelism only — a single device's commands always run in stream order.

Service coordinates come from the shared infrastructure config, and they are **not**
uniformly fail-open. `deviceManagement` and `deviceState` are required (without them the
adapter would refuse every registration, or could mint stale-rejected epochs across a
clock step-back). `commandDelivery` is fail-**open**: absent, the wake drain is simply
disabled and offline commands ride their TTL.

## Denial-of-service posture

- The DTLS **cookie exchange** (HelloVerifyRequest) is on by default in pion's server
  flow, so the server does not do handshake crypto for a spoofed source address — the
  amplification defense.
- **`maxSessions`** bounds the *post-handshake* live-session table.
- Each in-flight ClientHello holds a listener entry + handshake goroutine until the
  cookie/handshake completes or the **handshake timeout** (default 10s) expires, so the
  timeout — not `maxSessions` — is what bounds a sustained ClientHello spray. This is
  inherent to the pion listener; keep the handshake timeout short.
- Past the handshake, an **authenticated** device is bounded by the per-tenant ingest
  limiter on both `/rd` and Notify, by `MaxSamplesPerNotify` on a single payload, by
  `MaxObservationsPerRegistration` on how many Observes one registration can spawn, and
  by the min-lifetime clamp + re-register backoff on how fast it can churn durable
  presence writes.

## Metrics

Roughly fifty Prometheus instruments, all registered in one place (`buildMetrics` in
`main.go`) so they are created exactly once and survive a leadership-term rebuild.
Enumerating them here would only drift; read `buildMetrics` — every one carries a Help
string. The families are:

| Family | Covers |
|---|---|
| `handshakes_*`, `active_sessions`, `sessions_rejected_total`, `coap_requests_total` | the DTLS/CoAP transport |
| `is_leader` | 1 on the serving leader, 0 on a warm standby |
| `registrations_*`, `registration_updates_total`, `deregistrations_total`, `registration_expiries_total`, `active_registrations`, `shadows_reconstructed_total`, `presence_*`, `auth_errors_total`, `bad_requests_total` | `/rd` and presence, including failover reconstruction |
| `notifies_received_total`, `notify_*`, `observe_*`, `active_observations`, `measurements_emitted_total`, `telemetry_*` | Observe/Notify decode and the measurement ingest path |
| `ingest_messages_shed_total`, `ingest_samples_shed_total` | the per-tenant ingest ceiling |
| `commands_*`, `command_park_*`, `command_drain_*`, `command_response_publish_failures_total` | downlink dispatch, park and wake-drain (see [COMMANDS.md](COMMANDS.md)) |

Labels are deliberately sparse (the ADR-023 cardinality lesson): **none is labeled by
tenant or device.** `coap_requests_total{code}` is narrower than its name suggests — it
meters the **L0 health probe only**; `/rd` outcomes have their own counters above. The
three `commands_{attempted,succeeded,failed}_total` counters carry an `op` label, and it
is a **bounded** set (`read`/`write`/`execute`/`other`) — never the raw operator-authored
command name.

## Interop

`registry/leshan_interop_test.go` drives the real server with an independent Java
implementation (Eclipse Leshan), not our own client, so a bug shared by our encoder and
our decoder cannot cancel out. It is tag-gated (`//go:build interop`) — plain
`go test ./...` never compiles it and never needs a JVM — and runs in the periodic
`lwm2m-interop` workflow, which builds the client jar from `interop/leshan-client`. With
the jar absent it *skips* locally but *fails* in CI, so the workflow can never pass by
silently running nothing.

Four scenarios: `lifecycle_and_tenancy` (Register/Update/Deregister with a **hostile**
`ep` asserting another tenant's device — the D1 red line), `observe_notify`,
`observe_block2`, and `lifetime_lapse`. Assertions read server-side seams (a fake presence
emitter, a recording ingester), never the client's self-report, so a client that lies or
no-ops cannot manufacture a pass.

🔴 **Commands are not covered by this harness.** The manual Leshan procedure in
[COMMANDS.md](COMMANDS.md#interop) is the only downlink coverage there is.
