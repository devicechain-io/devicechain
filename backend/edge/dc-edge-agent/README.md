# dc-edge-agent

The DeviceChain **durable store-and-forward edge agent** (ADR-068 Tier 1).

A small standalone binary that runs on an edge box (industrial PC, gateway, small
VM) at a site. It terminates the golden MQTT device path **locally** — so the
device→edge link is always up — buffers telemetry, and forwards it to a cloud
DeviceChain Instance. It is **not** the platform: no Kubernetes, no Postgres, no
NATS cluster, no GraphQL, and it runs no rules. Its whole job is: terminate local
device MQTT → durably capture → forward to the cloud.

It is built from `backend/core` plus two libraries already in the platform's dep set
(`nats-server/v2` as the embedded local broker, `paho.mqtt.golang` as the cloud
uplink), so it is mostly assembly of existing investments (ADR-021 device plane,
ADR-030 durable capture) rather than new surface.

## How it works

```
 device ──golden MQTT (unchanged)──▶ [ embedded nats-server MQTT gateway ]
                                              │  writes to a local JetStream
                                              │  capture stream BEFORE PUBACK
                                              ▼
                                     [ durable WorkQueue capture stream ]
                                              │  durable consumer drains FIFO;
                                              │  acks only AFTER the cloud PUBACKs
                                              ▼
                                     [ paho uplink ]──MQTT──▶ cloud Instance broker
                                     (as the GATEWAY device, in its own namespace)
```

The device is unaware it is talking to the agent rather than the cloud (ADR-006:
standard MQTT clients work unchanged). The agent is a **gateway**: it authenticates to the
cloud broker as its own device and forwards each local device's events on the gateway's
namespace, `{instance}/{tenant}/gateways/{gateway}/devices/{device}/events`. The cloud
ingests them through its own MQTT-gateway capture (ADR-030) and accepts an event only for
a device **assigned to that gateway** — there is no bespoke edge↔cloud protocol. See
[Gateway identity](#gateway-identity) below.

Because the drain acks a captured event **only after the cloud confirms it**, an event
published while the uplink (WAN) is down stays in the WorkQueue spool — across the
outage *and* an agent restart during it — and drains when the link returns. The cloud
folds any duplicate a redelivery would create on its device-level
`(tenant_id, alt_id, occurred_time)` partial unique index: when a device omits them,
the agent mints a **replay-stable** `altId` (`edge:<installId>:<streamEpoch>:<seq>`,
all derived from immutable stored-message metadata) and stamps `occurredTime` with the
message's local store-time, so the *same* buffered event carries the *same* key on
every delivery. This exactly-once property is per-decoder: it applies to **JSON**
payloads; any non-JSON payload is forwarded verbatim and is **at-least-once**.

## Slice status — **E4 (shippable GA artifact: local auth, at-rest hardening, packaging).**

E4 makes the agent deployable and securable at a site — the demoable GA artifact — on top
of E3's bounded, observable buffer:

- **Opt-in local device auth.** The device MQTT listener takes an optional shared-secret
  credential (`local.username` + `local.passwordEnv`). Unset, the listener is open (the
  trusted-LAN default) and the agent logs a loud `UNAUTHENTICATED` warning at every boot and
  exports `local_auth_enabled 0`, so "open" is a visible, auditable choice — never a silent
  default. Set, the embedded MQTT gateway rejects any CONNECT without the credential (it
  gates the device surface only; the in-process drain is unaffected). A configured username
  whose password env is empty (a Secret that didn't project) **fails the agent closed**
  rather than degrading to an open listener.
- **Store-at-rest hardening.** The agent removes world-access from `storeDir` on startup
  (the `0755` `mkdir -p`/volume default becomes `0750`, logged) while preserving group bits
  for a deliberate group-shared setup (a container `fsGroup`). The spool carries buffered
  telemetry — which can include in-flight payload credentials — and the identity tokens
  (already `0600`).
- **Packaging.** Published as a distroless container image
  (`ghcr.io/devicechain-io/dc-edge-agent`, non-root `65532`, read-only rootfs) and as static
  `linux`/`darwin` × `amd64`/`arm64` binaries attached to each GitHub Release. `dc-edge-agent
  version` reports the build.
- **Operator runbook.** Deploy (container + systemd), secure, operate, and upgrade — see
  [RUNBOOK.md](RUNBOOK.md).

It retains E1–E3's guarantees (store-and-forward across a WAN outage + agent restart;
cloud-side exactly-once for JSON; the two-phase start; the bounded, observable ring buffer).

## Gateway identity

The edge agent is the platform's **gateway** (the only uplink mode). Its identity is an
ordinary device on the platform, and the platform records which devices it may speak for.

**Set up, on the platform:**

1. Create the gateway device (any device type) and a device credential for it
   (`MQTT_BASIC`). Its token is `uplink.gatewayToken`; the credential's id goes in
   `uplink.username` as `{tenant}:{credentialId}`.
2. Create each device the site fronts, and assign it to the gateway
   (`assignDeviceGateway`, or `gatewayToken` when creating it). A device has at most one
   gateway; a gateway cannot itself have a gateway.
3. Point the agent at the platform broker and give it the credential's secret in the
   environment variable named by `uplink.passwordEnv`.

**What the agent does:**

- It connects with the gateway device's **plain MQTT client id** (the same id the broker
  asserts that device's presence under). It no longer has a per-agent client id: one
  gateway identity is one session, and a second agent using it takes the first one over.
- It builds every uplink topic with **one constructor that accepts only a child device
  token**, so it cannot publish outside its own namespace. (Publishing outside a grant is
  the one failure the broker hides: MQTT 3.1.1 cannot refuse a publish, so the broker
  acknowledges it and drops it.)
- It captures from its local devices **only the configured tenant**
  (`{instance}/{tenant}/devices/+/events`). A local publish naming another tenant is
  counted (`tenant_mismatched_total`) and never spooled.
- It **removes** `credentialType`, `credentialId` and `credentialSecret` from every event
  body before forwarding (`body_credentials_stripped_total`, with a WARN per device). The
  platform refuses a body credential on a broker-authenticated topic, and a secret has no
  business crossing the WAN. Change the device to stop sending it.
- It keeps up to **64 publishes in flight** and acknowledges each spool message on its own
  PUBACK. The old drain waited for each PUBACK before the next publish, so a box topped out
  near `1 / round-trip-time` events per second (about 20/s at 50 ms) and could not catch up
  after an outage. Original event times are preserved on replay (see above), and the cloud
  still deduplicates a redelivery.
- It subscribes to the gateway's **notices** topic (`{instance}/{tenant}/gateways/{gateway}/notices`)
  on every connect. A refused subscription fails the connect, loudly.

**When the platform refuses an event.** An event for a device that is not assigned to this
gateway (or does not exist) is refused by the platform, which records it (failed events,
with its payload), counts it (`resolve_not_gateway_child_total`) and sends this gateway a
**notice**. The broker has already acknowledged the publish, so the agent cannot learn this
from a PUBACK. On a notice the agent:

- logs a `WARN` naming the device (at most once per device per 30 s) and counts it in
  `refused_total{reason="not_gateway_child"}`;
- writes the refused event to the local dead-letter file `refused-events.jsonl` in
  `storeDir` (mode `0600`, one JSON object per line, rotated to `.1` at 16 MiB), so it is
  recoverable from the box.

Three limits to know. A notice is **best effort** (a gateway that was offline, or a broker
restart, misses it), so the record of truth is the platform's failed-events plus its
counter. The agent holds a **bounded** copy of what it sent (16 MiB, 8192 events) to match a
notice against, so a notice that arrives after the copy rolled out is counted as
`refused_unmatched_total` and logged, not kept. And the dead-letter file holds the payload as
forwarded, credentials already removed. Recovering a refused event after fixing the
assignment is a manual replay today.

The agent also refuses, without sending, an event it knows the platform would refuse: a
local device publishing under the gateway's **own** token (`reason="gateway_self"`; a
gateway is not a child of itself) or under a token the platform cannot hold
(`reason="invalid_device_token"`). Both are written to the same dead-letter file and acked.

**Upgrade order.** Finish the platform upgrade (every `event-sources` and
`device-management` replica, and the broker grants) **before** switching an agent to the
gateway uplink. An `event-sources` that predates the gateway namespace, running with the
fail-closed MQTT store, acknowledges and drops gateway messages it cannot attribute, and
the agent would see success. An agent that cannot subscribe to its notices topic (a broker
that has not been upgraded) refuses to report the uplink connected.

**Trust.** A gateway speaks for its children. A compromised gateway can forge events only
for devices assigned to it, and only until it is disabled or its credential is revoked. Its
word about *which local client* sent an event is only as strong as the site LAN's local
authentication.

### E3 (bounded buffer + observability)

E3 makes the agent's behaviour under an arbitrarily long outage **bounded and
observable**, on top of E2's durable store-and-forward buffer:

- **Bounded spool (ring buffer):** the capture stream carries a `MaxBytes` budget
  (`local.spoolMaxBytes`, default 1 GiB) with `DiscardOld`, so a multi-day outage can
  no longer grow the on-disk store until the disk fills. When the budget is reached the
  spool drops the **oldest** un-forwarded event to admit the newest — see the overflow
  policy below. `JetStreamMaxStore` is pinned above the budget, so a near-full spool on
  a small disk still starts (admission does not depend on boot-time free space).
- **Visible loss:** every eviction is counted (`…_dropped_total`) — computed from the
  stream's durable first sequence minus the agent's own persisted acked-count, so the
  count is correct **live during the outage** and **preserved across an agent restart**
  (it is deliberately *not* derived from the durable consumer's ack floor, which
  nats-server drags past limit-evicted messages and would silently erase the drops). A
  loud `WARN` fires on each new drop and near the high-water mark.
- **Local health signal:** a loopback (`127.0.0.1`) Prometheus `/metrics` + `/healthz`
  endpoint (`local.metricsPort`, default 9090). The MQTT gateway stays the only
  LAN-exposed surface (F5); `/healthz` is a live probe (up **and** the last stream
  sample succeeded) and does **not** gate on the uplink — surviving a down uplink is
  the point, so uplink state is a metric, never a readiness failure.

### Overflow policy — why `DiscardOld`, not `DiscardNew`

On a finite disk an unbounded outage must eventually shed data — the only choice is
*which*. The embedded MQTT gateway PUBACKs a QoS-1 device from its **own** internal
persistence, decoupled from this overlay capture stream, so a `DiscardNew` rejection at
the capture layer would silently lose the **newest** event *after* the device was
already told it was durable. `DiscardOld` instead drops the **oldest** un-forwarded
event: the agent stays healthy (the disk never fills), recent telemetry is favoured, and
the loss is bounded, predictable, and **counted** rather than a disk-full crash. Neither
policy avoids loss on a finite disk during an unbounded outage — `DiscardOld` is the
fail-safe because the agent survives and the loss is surfaced.

### Metrics

Served on `127.0.0.1:<metricsPort>` (Prometheus text format), namespace
`devicechain_edge_`: `spool_used_bytes` / `spool_limit_bytes` / `spool_used_messages`
/ `spool_oldest_age_seconds` (the primary backlog signal) / `dropped_total` (overflow
evictions) / `uplink_connected` / `uplink_inflight` (publishes awaiting a PUBACK, at most
64), plus `received_total` / `forwarded_total` / `forward_errors_total` /
`malformed_total` / `instance_mismatched_total` / `tenant_mismatched_total` /
`body_credentials_stripped_total` / `refused_total{reason}` / `refused_kept_total` /
`refused_unmatched_total`. `dropped_total`
does not reset across a restart (both its operands are durable); the other `_total`
counters are per-process. `GET /healthz` → `200` when up and healthy, else `503`.
`local_auth_enabled` (E4) is `1` when the local listener requires a credential, else `0`.

### Known gaps / accepted risks (tracked)

- **Power-loss durability** rides an fsync-on-every-store (`SyncAlways`), so the floor
  is on-disk, not page-cache — an explicit edge-vs-cloud trade (edge boxes lose power).
  Note the internal MQTT session/QoS streams (`$MQTT_sess/_msgs/_qos2in`) carry no
  `MaxBytes` of their own; the `JetStreamMaxStore` headroom accounts for them, but a
  pathological churn of MQTT client ids is a slow leak one hop before the telemetry path.
- **The high-water `WARN` is best-effort** (sampled on a 30 s tick); `dropped_total` is
  the guarantee, the WARN is the early runway signal.
- **Rolling back to an E2 binary over an E3 store silently strips the bound** (the E2
  `UpdateStream` sends `MaxBytes=0`). Pre-GA acceptable; do not downgrade a bounded store.
- **Changing `instanceId` or the uplink timeout over an existing store fails closed,
  not gracefully.** (Changing `uplink.tenant` is the exception: the agent moves the durable
  consumer's filter in place. Events already spooled for the old tenant match no consumer and
  stay until the ring buffer evicts them.) Both the durable consumer's filter subject (`instanceId`) and its
  `AckWait` (derived from `connectTimeoutSeconds`) are fixed when the consumer is first
  created; changing either on an agent that already has a populated `storeDir` makes the
  durable bind reject at startup (a loud, repeated error — no silent loss). Reprovision
  a changed agent onto a fresh `storeDir` after its spool has drained; graceful
  in-place reconfiguration (recreate the durable, migrate/strand old-namespace events)
  is future work. (`spoolMaxBytes` and `metricsPort`, by contrast, *are* changeable over
  an existing store.)

- **Local device auth is opt-in shared-secret (E4), default trusted-LAN.** With
  `local.username`/`passwordEnv` unset the local MQTT listener accepts any LAN connection
  (open) — logged loudly at boot and exported as `local_auth_enabled 0` — so it must be on
  a trusted network. Set the credential to require it (a single shared secret, not
  per-device identity; over plaintext MQTT it is sniffable on the LAN — local MQTT TLS is
  future work). Cloud event *attribution* does not depend on this connection — the platform
  attributes by the gateway's authenticated session plus the device's gateway assignment — so
  a forged local connection can only forge events for devices assigned to this gateway. A
  device on the LAN that can publish for a sibling can still impersonate it to the platform:
  that is exactly what the local gate is for. Enabling auth on a live site is a breaking change for connected
  devices; sequence it (see [RUNBOOK.md](RUNBOOK.md#4-secure)).
- **`responses` and `reports` are not forwarded yet.** The platform grants the gateway the
  `responses` and `reports` leaves of its namespace, but nothing captures them until commands
  to children are routed. The agent publishes **events only**.
- **Commands do not reach children yet.** The agent subscribes to notices only; the
  downlink (`commands`, `desired`) comes with command routing to children.
- **The plain NATS client port is unbound** (`DontListen`) so the MQTT gateway is the
  only exposed surface — this is enforced and tested, not incidental.
- **A config `instanceId` typo forwards nothing.** The agent only captures/forwards
  its configured instance namespace. A mismatch is **counted and logged** (not a
  silent success), and surfaced as `devicechain_edge_instance_mismatched_total`.

## Configuration

Typed and fail-closed: unknown/invalid keys are rejected at startup. JSON:

```json
{
  "instanceId": "prod1",
  "local": {
    "listenHost": "0.0.0.0",
    "listenPort": 1883,
    "storeDir": "/var/lib/dc-edge-agent",
    "spoolMaxBytes": 1073741824,
    "metricsPort": 9090,
    "username": "site42-devices",
    "passwordEnv": "DC_EDGE_LOCAL_PASSWORD"
  },
  "uplink": {
    "brokerUrl": "ssl://cloud.example.com:8883",
    "tenant": "acme",
    "gatewayToken": "site42-gateway",
    "username": "acme:site42-cred",
    "passwordEnv": "DC_EDGE_UPLINK_PASSWORD",
    "connectTimeoutSeconds": 30,
    "backoffMinSeconds": 1,
    "backoffMaxSeconds": 60
  }
}
```

- `instanceId` **must** match the target cloud Instance exactly (it is the first
  segment of the golden topic on both planes).
- `uplink.tenant` is the tenant the gateway device belongs to; it is a segment of every
  uplink topic and the only tenant the agent captures from its local devices.
- `uplink.gatewayToken` is the token of the gateway **device** this agent authenticates as.
  One gateway identity per edge box: two agents sharing one take each other's session over.
  (There is no `agentId`: the uplink MQTT client id is the gateway device's plain client id.)
- `uplink.username` **must** be `{tenant}:{credentialId}` for the tenant above, and
  `uplink.passwordEnv` is required: the broker authenticates the gateway as a device. A bare
  name (the old `edge-site-42`) is refused at load, not at CONNECT.
- `uplink.brokerUrl` scheme selects transport: `tcp://` (plaintext) or
  `ssl://`/`tls://` (TLS with system roots).
- `uplink.passwordEnv` names an environment variable holding the gateway credential's
  secret (a projected Secret — never cleartext in this file). The edge box has no ADR-059
  secret store, so this follows the sparkplug-ingest env/Secret precedent.
- `local.spoolMaxBytes` bounds the durable spool (default 1 GiB; floor 16 MiB). Beyond
  it the spool is a ring buffer (drop oldest; see the overflow policy above). It can be
  raised or lowered over an existing store — shrinking below current usage evicts the
  oldest immediately (surfaced as drops). Changing `instanceId` or `connectTimeoutSeconds`
  over an existing store still **fails closed** (the durable consumer's filter subject and
  `AckWait` are fixed at creation) — reprovision such a change onto a fresh `storeDir`
  after the spool has drained; graceful in-place reconfiguration is future work.
- `local.metricsPort` is the loopback Prometheus/health port (default 9090; `0` disables
  the endpoint). It always binds `127.0.0.1` — never the LAN.
- `local.username` + `local.passwordEnv` (both or neither) require a shared-secret
  credential on the local MQTT listener. Omitted → open (trusted-LAN default, logged loudly).
  `passwordEnv` names an environment variable holding the password (a projected Secret,
  never cleartext); a configured username with an empty resolved password **fails the agent
  closed**. See [RUNBOOK.md](RUNBOOK.md#4-secure) before enabling on a live site.

## Run

```bash
dc-edge-agent --config /etc/dc-edge-agent/config.json
```

## Build / test

```bash
go build ./...
go vet ./...
go test ./...
```
