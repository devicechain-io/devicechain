<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# sparkplug-commands: commanding a Sparkplug B edge node

A Sparkplug B plant in two containers, and DeviceChain joining it as a **Host Application**:

- an **Eclipse Tahu** edge node (`pump-station`) with one device (`pump-1`) that publishes
  `NBIRTH`, `DBIRTH` and `DDATA`, obeys `NCMD` and `DCMD`, and reports the values it was told to set;
- an authenticated **Mosquitto** broker between them;
- **DeviceChain** in a kind cluster, ingesting the births and data, then writing `Outputs/Run` and
  asking for a rebirth with `DCMD` and `NCMD`.

Sparkplug has no command acknowledgement. DeviceChain marks a write `SUCCESSFUL` only when the node
**reports** the new value afterwards, so the edge node here re-publishes every value it is told to
set. That is the whole interoperability contract, and the part to copy into a real node.

```
 kind cluster                                 Docker network "kind"
┌───────────────────────────┐  tcp 1883   ┌────────────┐  tcp 1883   ┌───────────────────┐
│ DeviceChain               │────────────▶│ Mosquitto  │◀────────────│ Tahu edge node    │
│  sparkplug-ingest (Host)  │             │ spb-       │             │  group  plant-a   │
│  command-delivery         │◀────────────│ mosquitto  │────────────▶│  node   pump-station
└───────────────────────────┘ births/data └────────────┘ NCMD / DCMD │  device pump-1    │
                                                                      └───────────────────┘
```

| Metric (device `pump-1`) | Type | What it is |
| --- | --- | --- |
| `Temperature`, `Pressure` | Double | published every 5 s in `DDATA` |
| `Outputs/Run` | Boolean | writable; changes what `Temperature` and `Pressure` read |
| `Outputs/Setpoint` | Double | writable |
| `Outputs/Interlock` | Boolean | declared writable but never applied: the command that ends unconfirmed |

> **This is an example, not a product.** The edge node is a teaching device. The broker uses a
> plain listener, and the credentials are random per checkout. See [what to change](#for-a-real-plant).

## Requirements

`docker` (with `compose`), `kind`, `kubectl`, `jq`, `curl`, `dcctl` (with `tofu` on the path, which
`dcctl upgrade` needs), and a DeviceChain release that accepts dotted command keys. **Releases up to v0.26.0 refuse the key `sparkplug.write`** when a
profile declares it (`invalid command key ... must be letters, digits, hyphens and underscores`), so
`provision.sh` stops at its first command there. Use a release that contains the change that allows
`namespace.name` keys, or bootstrap from this source tree (`dcctl bootstrap --dev`).

Run every script from this directory. They talk to `http://localhost` (the ingress of a default
`dcctl install local`); set `DC_URL` if yours is elsewhere, and `KUBECONFIG` or `KUBE_CONTEXT` to pick
the cluster.

## Run it

```bash
# 1. DeviceChain, with the Sparkplug area. Create the cluster first: the plant joins its Docker network.
dcctl install local --compact
dcctl bootstrap local spb --enable-area sparkplug-ingest --host localhost --no-tls --no-escrow

# 2. The plant: Mosquitto and the Tahu edge node.
./setup.sh                        # writes .env with two random broker passwords
docker compose up -d --build

# 3. Introduce them: a tenant, a profile declaring the two commands, then the Sparkplug source.
./provision.sh
./connect-devicechain.sh
```

Order matters in step 3, but a mistake recovers by itself. A node is registered the first time it is
seen, with the device type the source names. If its births arrive before that type exists they are
dropped, and DeviceChain then asks the node to rebirth every few seconds, up to once a minute, until
it registers (`birth_recovery_pending` counts the nodes waiting). Create the type and the node
appears within a minute; there is nothing to restart.

### What step 3 did

- `provision.sh` created the tenant `sparkplug-demo`, a profile `pump` that declares the commands
  `sparkplug.write` and `sparkplug.rebirth`, published it, and a device type `sparkplug-pump`. The
  console's **Add Sparkplug standard commands** button on a profile declares the same keys (plus
  `sparkplug.reboot`, which this example leaves off).
- `connect-devicechain.sh` ran `dcctl upgrade local spb --sparkplug-source <file>` with one Sparkplug
  **source**: broker `tcp://spb-mosquitto:1883`, group `plant-a`, host id `devicechain`,
  `autoRegister` with that device type, and `commands` enabled with `hostRole: primary` and
  `writableMetrics: ["Outputs/*"]`. The file holds the broker password only for the length of the
  command; `dcctl` writes it into a Secret of its own, outside the Helm release, so it is in no release record or
  configuration.

The source is part of the instance now: **every later `dcctl upgrade` keeps it**, with no flag. Give
the flag again to change it (a new password rolls the pod), and `dcctl upgrade --withdraw-sparkplug-sources`
to remove it. The area has to be running, which step 1 did with `--enable-area sparkplug-ingest`; a
bootstrap given `--sparkplug-source` enables it for you. The broker is reached by its container name
because every kind node, and so every pod, resolves names on the `kind` Docker network; no
`host.docker.internal` or published port is involved.

## See it work

```bash
./show-state.sh
```

```
plant-a/pump-station  online=true  connected=...
plant-a/pump-station/pump-1  online=true  connected=...
    Outputs/Setpoint = 60
    Pressure = 1.037
    Temperature = 21.26
```

Both identities appeared on their own (`group/node` and `group/node/device` are the devices'
external ids), and each **numeric** metric has a measurement. `Outputs/Run` and `Outputs/Interlock`
are Booleans, which become no measurement: only numeric metrics do. To watch the wire:

```bash
docker exec spb-mosquitto sh -c 'mosquitto_sub -u devicechain -P "$HOST_PASSWORD" -t "spBv1.0/#" -v' | cat -v | head
docker compose logs -f edge-node
```

### Write a metric (`DCMD`)

```bash
./send-command.sh pump-station/pump-1 sparkplug.write '{"metric":"Outputs/Run","value":true}'
```

```
01:22:39 SUCCESSFUL
queued:   2026-10-29T05:22:38.5Z   sent:  ...   answered: ...
outcome:  SUCCEEDED
evidence: {"via": "DDATA", "kind": "write", "metric": "Outputs/Run", "value": true, "reason": "confirmed", "hostRole": "primary", ...}
```

The command went `QUEUED`, then `SENT` as a `DCMD` on `spBv1.0/plant-a/DCMD/pump-station/pump-1`
(addressed by the alias the `DBIRTH` gave the metric), and became `SUCCESSFUL` when the node's `DDATA`
reported `Outputs/Run = true` (`via: DDATA`, `reason: confirmed`). The edge node's log shows the same
sequence from its side (`docker compose logs edge-node`). Try `Outputs/Setpoint` with `72.5`, which also
lands as a measurement.

### Rebirth (`NCMD`)

```bash
./send-command.sh pump-station sparkplug.rebirth
```

It ends `SUCCESSFUL` with `via: NBIRTH`, `kind: rebirth`: a rebirth is confirmed by the node's next
birth. Sent to the device (`pump-station/pump-1`) it resolves to the node.

### Commands that do not succeed

| Send | Ends | Why |
| --- | --- | --- |
| `{"metric":"Temperature","value":5}` | `FAILED`, outcome `NOT_DELIVERED`, `reason: metric_not_writable` | not on the source's `writableMetrics`; nothing was sent |
| `{"metric":"Outputs/Nope","value":true}` | `FAILED`, `NOT_DELIVERED`, `reason: metric_not_declared` | the node's birth does not declare it |
| `{"metric":"Outputs/Run","value":"banana"}` | `FAILED`, `NOT_DELIVERED`, `reason: value_not_accepted` | the birth declared a Boolean; refused before the wire, with the datatype in the error text |
| `{"metric":"Outputs/Interlock","value":true}` | `SENT`, then after 2 minutes `TIMEOUT`, outcome `UNKNOWN`, `reason: unconfirmed_timeout` | the node got it and never reported it: `TIMEOUT` is "not confirmed", not "failed" |

`TIMEOUT` is the case to understand. The Interlock command *was* published, and the platform cannot
tell a node that ignored it from one that obeyed and stayed quiet; that is why the outcome is
`UNKNOWN` and why a retry may act twice.

### The node dies, then comes back

```bash
docker kill "$(docker ps -q --filter name=spb-edge-node)"
./show-state.sh                                   # both online=false
./send-command.sh pump-station/pump-1 sparkplug.write '{"metric":"Outputs/Run","value":false}'   # HELD
docker start spb-edge-node
```

The broker publishes the node's will (`NDEATH`), DeviceChain marks the node **and** its device
offline, and a command sent meanwhile is `HELD` rather than lost. When the node connects and births
again both read `online=true`, the held command is sent on that birth, and it is confirmed by the
`DDATA` that follows.

## Tahu quirks this example works around

Read these before porting the node to your own code.

1. **`addMetric()` stamps every metric with the time the library was imported.** Its default
   `timestamp=` argument is evaluated once, at import. A node that does not pass its own timestamp
   publishes values that look as old as the process. DeviceChain only counts a report that is not
   older than the command's publish time minus 30 seconds (`clockSkewSeconds`), so a write the node
   did apply ends `TIMEOUT` once the node has been up for more than 30 seconds. `edge_node.py` passes
   `timestamp=` every time. Reproduce the problem with `STALE_TIMESTAMPS=1 docker compose up -d edge-node`,
   wait a minute, and write `Outputs/Setpoint`.
2. **`bdSeq` restarts at 0 in every process.** A restarted node reuses the `bdSeq` of the session
   that just ended, so a host cannot tell the new session from the old one. The node here starts from a
   random value. Separately, the will is built once per process, so a *reconnect* inside one process
   reuses the same `bdSeq`; this example does not reconnect inside a process (the container restarts).
3. **The generated protobuf code is old.** `sparkplug_b_pb2.py` needs the pure-Python protobuf implementation (`protobuf==5.29.6`) on a current interpreter and the examples use the paho-mqtt 1.x API, so both are pinned in
   `edge-node/requirements.txt`.
4. **Tahu ships no container image, and the Python library no package.** The `Dockerfile` fetches
   `python/core` from the `v1.0.22` tag by commit id at build time.

Also: the node publishes births as soon as it connects. A node that names a primary host should wait
for that host's `STATE`; this one does not, and relies on DeviceChain asking it to rebirth.

## For a real plant

- Put TLS on the broker listener and use `ssl://` in the source.
- Keep `writableMetrics` as narrow as the list of outputs you mean to expose, and keep the ACL in
  `mosquitto/acl`: the `devicechain` login needs `write` on `NCMD` and `DCMD`, or every command ends
  unconfirmed. Anyone who can publish to a node's topics can make a metric *read* any value, so the
  ACL is what stands behind a confirmation.
- Declare `hostRole` truthfully: `primary` only if your nodes name this host id as their primary host.

## Clean up

```bash
docker compose down
dcctl destroy local spb
```

## Third-party software

| Component | Licence | How it is used |
| --- | --- | --- |
| [Eclipse Tahu](https://github.com/eclipse-tahu/tahu) v1.0.22 (`python/core`) | EPL-2.0 | Fetched into the edge-node image at build time, unmodified. Not copied into this repository. |
| [Eclipse Mosquitto](https://mosquitto.org/) 2.0.22 | EPL-2.0 / EDL-1.0 | Run as the published image, pinned by digest. |
| [paho-mqtt](https://github.com/eclipse/paho.mqtt.python) 1.6.1 | EPL-2.0 / EDL-1.0 | Installed in the edge-node image. |
| [protobuf](https://github.com/protocolbuffers/protobuf) 5.29.6 | BSD-3-Clause | Installed in the edge-node image. |
| [python](https://hub.docker.com/_/python) 3.12-slim | PSF-2.0 | Base image, pinned by digest. |

`edge_node.py` follows the structure of Tahu's `python/examples/example.py` and is licensed with the
rest of this repository (Apache-2.0).
