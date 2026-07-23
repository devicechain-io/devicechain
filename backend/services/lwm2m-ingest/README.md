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

## Slice status — L0 (transport substrate + skeleton)

L0 stands up the **transport only** (the ADR-075 Gate-2 proof: prove the CoAP/DTLS
substrate before building the object model). It is:

- A **DTLS-PSK-authenticated CoAP server** (`server/`) that carries many concurrent
  long-lived sessions and answers a trivial health resource. The LwM2M `/rd`
  registration interface, presence, Observe→measurement decoding, and downlink arrive
  in later slices as handlers on the same CoAP mux.
- **Fail-closed authentication**: a device is authenticated against a provisioned PSK
  credential map (identity → key). An unknown identity is refused (counted). This is
  the floor the L1 tenancy seam binds `(tenant, device)` onto — the tenant is bound to
  the **authenticated DTLS identity**, never parsed from the untrusted registration
  endpoint name (ADR-075 D1: LwM2M devices connect *in* to one shared socket, so
  tenancy cannot be connection-scoped the way Sparkplug's is).
- **DTLS Connection ID (RFC 9146), on by default.** CID lets a session survive a
  client's source-address change (a NAT rebinding, or a queue-mode cellular device
  waking on a new IP) without a fresh handshake. **This updates ADR-075:** the scoping
  spike (2026-07-21) named CID *absent* from pion/dtls and made cellular-sleeper
  session-resumption the one named GA limitation; pion/dtls v3.1.5 ships CID, fully
  plumbed. pion rebinds a session's peer address only *after* a CID record AEAD-decrypts
  and passes anti-replay (RFC 9146 §6), so this does not open a traffic-redirection
  vector. `server_test.go` proves the roaming survival, the negative control (CID off ⇒
  the roamed record is stranded), and the security property (a forged/replayed record
  from a spoofed source does not redirect the session).
- **Bounded**: a `maxSessions` ceiling on the live-session table (a new handshake past
  it is refused, counted) and an optional idle-session reaper — the memory-safety /
  DoS bounds a UDP-facing service owes.

### HA posture (single serving replica)

Sparkplug connects **outbound** to a broker, so a warm standby is safe (only the leader
connects; the broker fences the zombie). LwM2M is an **inbound** bound UDP socket — a
Kubernetes Service would spray device datagrams across replicas, and a warm standby
would silently receive and drop half of them. So GA runs a **single serving replica**
(ADR-070 "one shard"); a fenced ownership lease (rollout fencing + future N-shard) is
wired in a later slice. This L0 skeleton is single-replica with normal readiness.

## The arc (post-L0)

- **L0.5** — extract the shared ingest adapter (`Registrar`/`Emitter`/`Reconciler`)
  out of `sparkplug-ingest` into a shared `event-sources/adapter` package so both
  adapters share one copy.
- **L1** — `/rd` Register/Update/Deregister + lifetime timer → presence `StateChange`;
  tenant + device identity bound to the authenticated PSK identity; endpoint →
  `externalId` → `ALLOW_NEW`. Per-operation authorization against the authenticated
  identity.
- **L2** — Information Reporting (Observe/Notify) → SenML/IPSO decode → measurements.
- **L3** — leader election + failover (fenced lease; recovery via lifetime-timer
  reconstruction, not the Sparkplug rebirth probe).
- **L4** — downlink Read/Write/Execute (command-delivery) + Firmware Update Object 5
  (an OTA seed).

GA-honest scope: **PSK** credentials (X.509/RPK deferred), **SenML-JSON + text/plain**
content formats (TLV/CBOR a fast-follow — note LwM2M 1.0 clients default to TLV), **no
Bootstrap server** (devices pre-provisioned), lenient metric mapping. Interop self-test
target: **Eclipse Leshan** clients.

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
      { "identity": "opaque-handle-1", "pskEnv": "DC_LWM2M_PSK_DEV1" }
    ]
  }
}
```

- **`listen.port`** — CoAPS (5684) by default.
- **`security.connectionIdLength`** — DTLS CID length in bytes; `8` by default, `0`
  disables CID (a roaming device then re-handshakes). It is a pointer field so an
  explicit `0` is distinguishable from an omitted value.
- **`security.maxSessions`** — live-session ceiling (memory bound); a handshake past it
  is refused.
- **`security.idleTimeoutSeconds`** — reap a session idle this long; `0` disables
  reaping. A queue-mode deployment should set it above the expected wake interval so an
  idle sleeper's keys are not evicted (which would re-introduce the re-handshake CID
  avoids).
- **`security.identities[].pskEnv`** — NAMES the environment variable holding the
  **base64-encoded** pre-shared key; never the cleartext (the chart projects a
  Kubernetes Secret into it). Refused at startup if unset. A PSK identity is sent in the
  clear on the wire, so prefer an **opaque handle** over a `tenant:device` string.

## Metrics

`handshakes_total`, `handshake_failures_total` (unknown identity), `active_sessions`
(gauge), `sessions_rejected_total` (ceiling), `coap_requests_total{code}`. None is
labeled by tenant (the ADR-023 cardinality lesson).
