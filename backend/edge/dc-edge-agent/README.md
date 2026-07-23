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
```

The device is unaware it is talking to the agent rather than the cloud (ADR-006:
standard MQTT clients work unchanged). The cloud receives the forward on the same
golden topic and ingests it through its own MQTT-gateway capture (ADR-030) — there
is no bespoke edge↔cloud protocol.

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
evictions) / `uplink_connected`, plus `received_total` / `forwarded_total` /
`forward_errors_total` / `malformed_total` / `instance_mismatched_total`. `dropped_total`
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
  not gracefully.** Both the durable consumer's filter subject (`instanceId`) and its
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
  future work). Cloud event *attribution* does not depend on this connection — it rides on
  the per-event payload credential (ADR-014) — so a forged local connection cannot forge
  attributed events. Enabling auth on a live site is a breaking change for connected
  devices; sequence it (see [RUNBOOK.md](RUNBOOK.md#4-secure)).
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
  "agentId": "site42",
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
    "username": "edge-site-42",
    "passwordEnv": "DC_EDGE_UPLINK_PASSWORD",
    "connectTimeoutSeconds": 30,
    "backoffMinSeconds": 1,
    "backoffMaxSeconds": 60
  }
}
```

- `instanceId` **must** match the target cloud Instance exactly (it is the first
  segment of the golden topic on both planes).
- `agentId` **must** be unique per edge box feeding a given Instance — it is the
  tail of the uplink MQTT client id, and two agents sharing one would kick each
  other off the cloud broker in a loop.
- `uplink.brokerUrl` scheme selects transport: `tcp://` (plaintext) or
  `ssl://`/`tls://` (TLS with system roots).
- `uplink.passwordEnv` names an environment variable holding the uplink password (a
  projected Secret — never cleartext in this file). The edge box has no ADR-059
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
