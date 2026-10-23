<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# dc-edge-agent operator runbook

Deploy, secure, and operate a DeviceChain **Tier-1 durable store-and-forward edge agent**
(ADR-068) at a site. The agent terminates the golden MQTT device path locally, buffers
telemetry durably across a WAN outage, and forwards it exactly-once (for JSON) to a cloud
DeviceChain Instance. See [README.md](README.md) for the mechanism and guarantees.

The agent is a single static binary — no Kubernetes, database, or NATS cluster. It ships as
a container image and as OS binaries.

---

## 1. Before you start — what you need

- **A cloud Instance to forward to**, and its MQTT broker URL. Use TLS (`ssl://…`) for
  anything crossing a WAN.
- **A gateway device on the platform**, with a device credential, and the site's devices
  assigned to it. The agent authenticates as that device (`uplink.gatewayToken`, and
  `uplink.username` = `{tenant}:{credentialId}`); the platform accepts an event only for a
  device assigned to it. See [README.md](README.md#gateway-identity).
- **The platform fully upgraded first.** Finish upgrading the platform (every `event-sources`
  and `device-management` replica and the broker grants) before switching an agent to this
  uplink: an older `event-sources` in fail-closed mode acknowledges and drops gateway
  messages it cannot attribute. See §6.
- **The Instance id** (e.g. `prod1`) — it is the first topic segment on *both* the
  device→agent and agent→cloud hops, so it must match the cloud Instance **exactly** or
  nothing is consumed. A mismatch is counted (`devicechain_edge_instance_mismatched_total`)
  and logged, never a silent success.
- **A distinct gateway device per edge box.** The uplink MQTT client id is the gateway
  device's plain client id; two boxes sharing one gateway identity take each other's session
  over in a loop.
- **A writable store directory** on persistent disk for the durable spool (survives
  reboots). Size the disk for `spoolMaxBytes` + ~128 MiB headroom (see §5).

## 2. Configure

Config is a typed, fail-closed JSON file (unknown/invalid keys are rejected at startup).
Full field reference: [README.md](README.md#configuration).

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

**Credentials are never written in this file.** Both the uplink password and the optional
local password are read from environment variables (`passwordEnv`), which you supply as a
projected Secret / systemd `EnvironmentFile` (`0600`). If `local.username` is set but its
`passwordEnv` variable is empty at startup, the agent **refuses to start** rather than run
an effectively-open listener — so a Secret that failed to mount fails loudly, not silently.

## 3. Deploy

### Option A — container (docker / podman / k3s)

Image: `ghcr.io/devicechain-io/dc-edge-agent:<version>` (distroless, runs as non-root uid
`65532`). The agent writes **only** to `storeDir`, so the rootfs can be read-only; mount
`storeDir` as the one writable volume, **owned by / writable to uid 65532** (a volume left
owned by root or another uid gives `permission denied` on the spool — the most common
first-run failure).

```bash
# -p 1883:1883                    device MQTT (LAN)
# -v .../var/lib/dc-edge-agent    durable spool (writable by uid 65532)
# --read-only                     the agent writes only to the spool volume
docker run -d --name dc-edge-agent \
  --restart unless-stopped \
  --read-only \
  -p 1883:1883 \
  -v /srv/dc-edge-agent:/var/lib/dc-edge-agent \
  -v /etc/dc-edge-agent/config.json:/etc/dc-edge-agent/config.json:ro \
  -e DC_EDGE_UPLINK_PASSWORD="$UPLINK_PASSWORD" \
  -e DC_EDGE_LOCAL_PASSWORD="$LOCAL_PASSWORD" \
  ghcr.io/devicechain-io/dc-edge-agent:<version> \
  --config /etc/dc-edge-agent/config.json
```

`--config` is **required** — the entrypoint is the bare binary, so a container started with
no arguments exits immediately with `required flag(s) "config" not set`. Always pass it.

On Kubernetes/k3s: project the passwords from a `Secret` into the env vars, mount the config
from a `ConfigMap`, and back `storeDir` with a `PersistentVolumeClaim`. Set
`securityContext.fsGroup` so the volume is group-owned by a group the container is a member
of (the agent tolerates a group-shared store — see §4). **Health probes:** `/healthz` binds
`127.0.0.1` only (see §4), so a kubelet `httpGet` probe — which dials the pod IP — cannot
reach it; rely on process liveness (the container exits on a fatal fault and is restarted),
or scrape metrics from a node-local collector. Do **not** point an `httpGet` liveness probe
at the loopback endpoint.

### Option B — systemd (bare metal / VM, no container runtime)

Download the binary for your platform from the
[GitHub Release](https://github.com/devicechain-io/devicechain/releases)
(`dc-edge-agent_<version>_linux_<arch>.tar.gz`; `arm64` and `amd64` are both published),
verify it against `dc-edge-agent_checksums.txt`, and install it to `/usr/local/bin`.

```ini
# /etc/systemd/system/dc-edge-agent.service
[Unit]
Description=DeviceChain Tier-1 edge agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/dc-edge-agent --config /etc/dc-edge-agent/config.json
# Passwords come from a 0600 EnvironmentFile, never the unit or the config.
EnvironmentFile=/etc/dc-edge-agent/secrets.env
# systemd creates and owns this 0700, matching the spool's storeDir.
StateDirectory=dc-edge-agent
DynamicUser=yes
Restart=always
RestartSec=5
# Hardening (the agent needs only its store + loopback + outbound MQTT).
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

Point `storeDir` at the `StateDirectory` (`/var/lib/dc-edge-agent`). `secrets.env` holds
`DC_EDGE_UPLINK_PASSWORD=…` and `DC_EDGE_LOCAL_PASSWORD=…` and must be `chmod 0600`.

## 4. Secure

- **Network is the primary boundary.** Put the device MQTT listener on an isolated device
  VLAN. The plain NATS client port is never bound (only the MQTT gateway is exposed), and
  metrics/health bind `127.0.0.1` only.
- **Local device auth is opt-in** (`local.username` + `local.passwordEnv`). With it unset
  the listener is **open** and the agent logs a loud `UNAUTHENTICATED` warning at every boot
  and exports `devicechain_edge_local_auth_enabled 0` — alert on that gauge being `0` where
  you expect `1`. Set the credential to require it. Note it is a **single shared secret**
  (not per-device identity), and over plaintext MQTT it crosses the LAN in the clear — the
  network is still the real boundary; local MQTT TLS is future work.
  - **Enabling auth on a *live* site is a breaking change** for already-connected devices:
    once the agent requires a credential, a device that does not present it is rejected, and
    golden-MQTT devices generally do not buffer, so telemetry from that window is lost.
    **Sequence it:** configure the devices with the credential first (they are accepted on
    the still-open listener), *then* restart the agent with `local.username`/`passwordEnv`
    set. Doing it in the reverse order drops data until each device is reconfigured.
- **Store-at-rest.** The spool holds buffered telemetry (which can carry in-flight payload
  credentials) and the agent's identity tokens. The agent removes world-access from
  `storeDir` on startup (a `mkdir -p` / container-volume default of `0755` is tightened to
  `0750`, logged) and tolerates a **group-shared** store (`0770`, e.g. a container
  `fsGroup`). Provision it `0700` (or group-shared) to avoid the tightening warning; keep
  the `passwordEnv` Secret / `EnvironmentFile` at `0600`.
- **Cloud event attribution does not depend on the local connection** — the platform
  attributes by the gateway's authenticated session plus the device's gateway assignment —
  so a forged local connection can forge events only for devices assigned to this gateway.
  The local gate is a network-access control, not identity: a device that can connect can
  publish for a sibling, so keep the LAN trusted or enable it.
- **Revoking a site.** Disabling the gateway device, or revoking its credential, ends the
  uplink session; clearing a device's gateway assignment takes effect on its next event,
  within seconds, with no reconnect and without disturbing the gateway's other devices.

## 5. Operate

Metrics + health are on `http://127.0.0.1:<metricsPort>` (default `9090`; set
`local.metricsPort: 0` to disable). Scrape with a node-local Prometheus.

| Signal | What it tells you |
| --- | --- |
| `devicechain_edge_spool_oldest_age_seconds` | **Primary backlog signal** — how far behind the drain is. Rising = the uplink is down or slow. |
| `devicechain_edge_dropped_total` | Ring-buffer evictions (data loss because the spool hit `spoolMaxBytes` during a long outage). Durable across restarts; alert on any increase. |
| `devicechain_edge_uplink_connected` | `1` when the cloud uplink is up, `0` otherwise. |
| `devicechain_edge_spool_used_bytes` / `_limit_bytes` | Spool fill vs. the configured cap. |
| `devicechain_edge_local_auth_enabled` | `1` local auth on, `0` open. |
| `…_received_total` / `…_forwarded_total` / `…_forward_errors_total` | Throughput + forward failures (per-process counters). |
| `…_instance_mismatched_total` | Device publishes on a *different* `instanceId` than configured — usually a config typo. |
| `…_tenant_mismatched_total` | Device publishes naming a *different* tenant than `uplink.tenant`. Not spooled or forwarded. |
| `…_refused_total{reason}` | **Alert on any increase.** `not_gateway_child`: the platform refused an event because the device is not assigned to this gateway (or does not exist). `gateway_self` / `invalid_device_token`: the agent would not forward it. See "Refused events" below. |
| `…_refused_kept_total` / `…_refused_unmatched_total` | Refused events written to the local dead-letter file / refusals whose payload was no longer held (the platform's failed-events record has it). |
| `…_body_credentials_stripped_total` | Credentials removed from event bodies before forwarding; the device should stop sending them. |
| `…_uplink_inflight` | Publishes awaiting a PUBACK (window of 64). Pinned at 64 while draining a backlog is normal. |

`GET /healthz` → `200` when the agent is up and the last stream sample succeeded, `503`
otherwise. It **does not** gate on the uplink — surviving a down uplink is the whole point,
so uplink state is a metric, never a health failure.

**Refused events.** `refused_total{reason="not_gateway_child"}` rising means a device is
publishing through this gateway that the platform does not have assigned to it. The WARN in the
log names the device. The cause is almost always a missing assignment, or a device created in
another tenant. Fix it on the platform (assign the device to the gateway); events refused
before the fix are in the platform's failed-events record and in `refused-events.jsonl` in
`storeDir` (JSON lines: `at`, `reason`, `device`, `altId`, `topic`, `payload`). The file is
bounded (16 MiB, rotated once to `refused-events.jsonl.1`); copy it off the box if you need it.

**Sizing `spoolMaxBytes`:** set it to the longest outage you must ride × your telemetry
byte-rate. Ensure the disk has `spoolMaxBytes` + ~128 MiB free (the agent warns at startup
if not). Beyond the cap the spool is a ring buffer that drops the **oldest** un-forwarded
event to admit the newest (loss favours recent telemetry and is counted; it never fills the
disk or crashes).

**The spool bounds bytes, never age.** The platform refuses a reading dated more than 366 days
before it arrives (`eventtime.MaxAge`), so an event spooled for longer than that is lost on
arrival, uncounted here. The agent does not warn; alert on
`devicechain_edge_spool_oldest_age_seconds > 28944000` (335 days) to leave a month to restore
the uplink.

## 6. Upgrade / rollback

- **Config changes over a populated `storeDir`:** `spoolMaxBytes` and `metricsPort` can be
  changed in place. Changing **`instanceId`** or **`connectTimeoutSeconds`** over an existing
  store **fails closed** (the durable consumer's filter subject and `AckWait` are fixed when
  the consumer is first created) — a loud, repeated startup error, never silent loss. To
  change either, drain the spool, then reprovision the agent onto a **fresh** `storeDir`.
- **Upgrading the platform and the agents: platform first.** An agent on the gateway uplink
  publishes on the gateway namespace. A platform whose `event-sources` predates it, running
  the fail-closed MQTT store, acknowledges those messages at the broker and then drops them,
  and nothing on the agent shows it. Finish the platform upgrade (all replicas, and the broker
  grants) before restarting any agent on this version, and check the agent reports
  `uplink_connected 1` (it will not, if the broker has not been upgraded to grant the notices
  topic).
- **Moving a site from the direct-device uplink.** The old uplink mode is gone: `agentId` is
  no longer a key (the config is fail-closed, so a leftover `agentId` stops the agent at
  start) and `uplink.tenant`, `uplink.gatewayToken` and a `{tenant}:{credentialId}` username
  are required. Create the gateway device and assign the site's devices to it first. Events
  spooled under the old mode are forwarded in the new namespace, so a device must be
  assigned before the agent drains its backlog, or those events are refused.
- **Do not downgrade to a pre-E3 (E2) binary over a bounded store** — the older binary sends
  `MaxBytes=0` and silently strips the spool's size bound.
- **Version:** `dc-edge-agent version` reports the build (the container image tag is the
  authoritative version; a `go build`/image without release ldflags reports `dev`).
