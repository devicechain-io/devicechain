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

## Slice status — **E2 (durable buffer).**

E2 makes the agent do the thing that makes an edge agent valuable — survive a WAN
outage without losing telemetry — on top of E1's device-transparent bridge:

- **Store-and-forward:** the drain acks a captured event only after the cloud PUBACKs
  it, so an event published while the uplink is down survives the outage **and an
  agent restart during it** (WorkQueue retention) and drains on reconnect.
- **Cloud-side exactly-once (JSON):** replay-stable minted `altId` + stamped
  `occurredTime` (above) let the cloud's partial unique index fold any redelivery
  duplicate. Non-JSON payloads are at-least-once.
- **No startup durability hole:** a two-phase start brings the capture stream up
  before the device MQTT listener ever accepts a publish (closes E1's startup window).

Remaining before the agent is a GA artifact:

- **E3** bounds the buffer (disk budget + fail-safe overflow policy — the spool is
  **currently unbounded**, see below) and adds the full metric set.
- **E4** packages it (container + static binary), settles the local-auth posture, and
  ships an operator runbook — the demoable GA artifact.

### Known gaps deferred past E2 (accepted risks, tracked)

- **The spool is unbounded (E3).** A multi-day outage grows the local JetStream store
  until the disk fills; there is no `MaxBytes`/discard policy yet. The periodic status
  line reports **spool depth** so growth is visible, but an operator must currently
  size `storeDir` for the worst-case outage. Bounding (and choosing a fail-safe
  overflow behaviour — a naive `DiscardNew` would drop an event the gateway already
  PUBACK'd) is E3.
- **Power-loss durability** rides an fsync-on-every-store (`SyncAlways`), so the floor
  is on-disk, not page-cache — an explicit edge-vs-cloud trade (edge boxes lose power).
- **Changing `instanceId` or the uplink timeout over an existing store fails closed,
  not gracefully.** Both the durable consumer's filter subject (`instanceId`) and its
  `AckWait` (derived from `connectTimeoutSeconds`) are fixed when the consumer is first
  created; changing either on an agent that already has a populated `storeDir` makes the
  durable bind reject at startup (a loud, repeated error — no silent loss). Reprovision
  a changed agent onto a fresh `storeDir` after its spool has drained; graceful
  in-place reconfiguration (recreate the durable, migrate/strand old-namespace events)
  is E3.

- **No local device authentication (trusted-LAN only).** The local MQTT listener
  accepts any connection on the site LAN. Cloud event *attribution* does not depend
  on this connection — it rides on the per-event payload credential (ADR-014) — so a
  forged connection cannot forge attributed events. But an unauthenticated LAN client
  can still subscribe to `+/+/devices/+/events` and observe telemetry (including
  credentials in flight), so the listener must be on a trusted network. A local-auth
  posture is decided before E4 declares the agent shippable.
- **The plain NATS client port is unbound** (`DontListen`) so the MQTT gateway is the
  only exposed surface — this is enforced and tested, not incidental.
- **A config `instanceId` typo forwards nothing.** The agent only captures/forwards
  its configured instance namespace. A mismatch is **counted and logged** (not a
  silent success) so it is diagnosable, but the full metric set lands in E3.

## Configuration

Typed and fail-closed: unknown/invalid keys are rejected at startup. JSON:

```json
{
  "instanceId": "prod1",
  "agentId": "site42",
  "local": {
    "listenHost": "0.0.0.0",
    "listenPort": 1883,
    "storeDir": "/var/lib/dc-edge-agent"
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
