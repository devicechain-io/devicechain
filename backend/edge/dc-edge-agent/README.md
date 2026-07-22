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
                                     [ durable capture stream ]
                                              │  durable consumer drains FIFO
                                              ▼
                                     [ paho uplink ]──MQTT──▶ cloud Instance broker
```

The device is unaware it is talking to the agent rather than the cloud (ADR-006:
standard MQTT clients work unchanged). The cloud receives the forward on the same
golden topic and ingests it through its own MQTT-gateway capture (ADR-030) — there
is no bespoke edge↔cloud protocol.

## Slice status — **E1 (bridge skeleton). NOT DEPLOYABLE.**

This is the first slice of the ADR-068 Tier-1 arc. It builds the durable substrate
(embedded broker + capture stream + durable consumer) and proves the
**device-transparent bridge** and the **uplink auth**. It deliberately does **not**
yet do the thing that makes an edge agent valuable — buffer across a WAN outage:

- **E1 forwards through and drops on outage.** The drain acks every captured event
  immediately, whether or not the forward to the cloud succeeded. An event published
  while the uplink is down is **dropped, not buffered**. → Do **not** run E1 at a
  real site; direct-to-cloud a device would at least hold and retry. E1 is for
  proving the bridge, nothing else.
- **E2** turns the spool into a real buffer: it acks a captured event only after the
  cloud confirms durable capture, so an un-acked event survives the outage (and an
  agent restart) and drains on reconnect — with cloud-side exactly-once via a minted
  `altId` + stamped `occurredTime`.
- **E3** bounds the buffer (disk budget + fail-safe overflow policy) and adds the
  full metric set.
- **E4** packages it (container + static binary) with an operator runbook — the
  demoable GA artifact.

### Known gaps deferred past E1 (accepted risks, tracked)

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
