<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# chirpstack-quickstart: LoRaWAN uplinks into DeviceChain

A **ChirpStack v4** network server, a LoRaWAN gateway and two LoRaWAN sensors, all in containers,
forwarding uplinks to DeviceChain's **inbound integration**:

- the **gateway and sensors are simulated**, but the radio frames are real: the simulator builds
  LoRaWAN 1.0.x uplinks (encrypted payload, message integrity code) and hands them to ChirpStack
  Gateway Bridge in the Semtech UDP packet-forwarder format, as a physical gateway does. ChirpStack
  verifies the frames, decodes them with a JavaScript payload codec, and posts the decoded uplink
  to DeviceChain over HTTP;
- DeviceChain authenticates the sender, finds the device by its **DevEUI alias**, stores the
  decoded values (and link quality) as measurements with units, and **lists the DevEUIs it heard
  but does not know**.

```
 Docker (docker compose)                                   kind cluster
┌──────────────────────────────────────────────────┐      ┌────────────────────────────┐
│ simulator ─UDP─▶ gateway-bridge ─MQTT─▶ ChirpStack│─HTTP▶│ /api/integrations/         │
│ (gateway +        (Semtech UDP)    (verify, decode,│ POST │   lora-demo/chirpstack     │
│  2 sensors)                         HTTP integration)     │ alias lorawan.deveui ──▶   │
└──────────────────────────────────────────────────┘      │ measurements + unregistered│
                                                          └────────────────────────────┘
```

| Sensor | DevEUI | In DeviceChain |
| --- | --- | --- |
| Boiler room 1 | `70B3D57ED0012345` | registered as `boiler-room-1` by `provision.sh` |
| Cold store | `70B3D57ED0067890` | **not** registered: it appears under unregistered devices until you add it |

Each uplink carries four bytes: temperature (hundredths of a degree), humidity (%) and battery
(tenths of a volt). The codec in ChirpStack turns them into `temperature`, `humidity` and `battery`.

> **This is an example, not a product.** ChirpStack runs with its stock `admin` / `admin` login and
> a placeholder API secret, its web UI is published on `127.0.0.1:8080` only, and the radio is a UDP
> socket. See [what to change](#for-a-real-deployment).

## Requirements

`docker` (with `compose`), `kind`, `kubectl`, `jq`, `curl` and `dcctl`. About 1.5 GB of images.

The ChirpStack containers reach your instance **through the Docker host**, so the instance's ingress
must be published on a host port: port 80 on a default `dcctl install local`. Set `DC_INGRESS_PORT`
if it is elsewhere. Run every script from this directory; they talk to `http://localhost`
(`DC_URL` to change it) and use `KUBECONFIG` or `KUBE_CONTEXT` to find the cluster.

## Run it

```bash
# 1. DeviceChain. --host localhost matches the URLs the scripts use.
dcctl install local --compact
dcctl bootstrap local lora --host localhost --no-tls --no-escrow

# 2. The DeviceChain side: a tenant, a sensor profile with units, the integration, one device.
./provision.sh

# 3. The ChirpStack side, then the radio.
./connect-chirpstack.sh
docker compose --profile sim up -d simulator
```

### What step 2 did

`provision.sh` created the tenant `lora-demo`, a device profile `lora-sensor` that declares the
metrics `temperature` (`Cel`), `humidity` (`%`) and `battery` (`V`), a device type, the device
`boiler-room-1` carrying the alias `lorawan.deveui` = `70B3D57ED0012345`, and an **integration**
`chirpstack` of sender type `CHIRPSTACK_V4`, scoped to that device type, with link metrics on. The
integration's bearer token is shown once by the server; the script keeps it in `.integration-token`
(mode 0600, git-ignored) for step 3.

### What step 3 did

`connect-chirpstack.sh` ran [`setup/configure.py`](setup/configure.py), which, over ChirpStack's
gRPC API, created the gateway, a device profile with the codec, an application, two ABP devices,
and the application's **HTTP integration**:

| ChirpStack HTTP integration field | Value |
| --- | --- |
| Payload encoding | JSON (a Protobuf integration is refused with `415`) |
| Event endpoint URL | `http://host.docker.internal:80/api/integrations/lora-demo/chirpstack` |
| Headers | `Authorization: Bearer <token>`, plus `Host: localhost` because the instance routes by host name |

These are the fields to fill in by hand against a real instance, with `https://<host>/api/integrations/{tenant}/{integration}`
and no `Host` header.

## See it work

Give it a minute: the simulator sends an uplink from each sensor every 10 seconds.

```bash
./show-state.sh
```

```
== boiler-room-1 ==
alias: lorawan.deveui 70B3D57ED0012345
    battery = 3.3 V
    humidity = 45 %
    lorawan.gateways = 1
    lorawan.rssi = -57
    lorawan.snr = 7.5
    temperature = 23.22 Cel
== unregistered devices seen by the integration ==
    lorawan.deveui 70B3D57ED0067890  seen 1 times, last 2026-10-30T12:20:51.438503Z
```

- The values and **units** come from the metrics the profile declares; the three `lorawan.*`
  readings are link metrics (strongest reception's RSSI and SNR, and how many gateways heard it).
- The Cold store's frames are real and valid, but DeviceChain has no device carrying its DevEUI, so
  nothing is stored. It is **recorded** instead, with a count. A sighting is written at most once
  a minute per identifier, so the count measures activity, not messages.

### Add the unregistered sensor

```bash
./add-device.sh cold-store 70b3d57ed0067890
```

This creates a device with that alias, which is what the console's **Create a device with this
alias** button does on the integration's page. Within about a minute (the alias lookup is cached)
the next uplink is stored on `cold-store`, and the sighting disappears from the list:

```
$ ./show-state.sh   # boiler-room-1 as above, then the list is empty
```

Both sensors then have a live dashboard source like any other device: they have measurements with
units, so they can drive dashboards, detection rules and alarms.

### Watch the pieces

```bash
docker compose logs -f simulator            # the uplinks as sent
docker compose logs chirpstack | grep -i "posting event"   # ChirpStack posting to DeviceChain
```

ChirpStack's own web UI is at http://localhost:8080 (`admin` / `admin`): the gateway shows the
uplink frames on its **LoRaWAN frames** tab and each device its **Events**.

## Results (kind, DeviceChain v0.27.0, ChirpStack 4.20.0)

| Check | Result |
| --- | --- |
| ChirpStack accepts the simulator's frames (MIC valid, DevAddr matched) | yes; 2 uplinks per 10 s, each posted as `event=up` |
| DeviceChain authenticates ChirpStack's bearer token and accepts the body | yes |
| Registered sensor: DevEUI alias resolves, values stored | `temperature`, `humidity`, `battery` with units `Cel`, `%`, `V` |
| Link metrics stored | `lorawan.rssi = -57`, `lorawan.snr = 7.5`, `lorawan.gateways = 1` |
| Unknown DevEUI | not stored; listed under unregistered devices with a count |
| `add-device.sh` for that DevEUI | readings arrive on the new device about a minute later |

## Things worth knowing

1. **A codec in ChirpStack is required.** The platform stores the values the network server decodes;
   it does not decode the raw frame. An uplink with no decoded object is acknowledged and counted
   as undecoded.
2. **The sender's `Host` header.** The instance is routed by host name. A hosted network server
   posts to your real host name and needs no `Host` override; this example reaches a local cluster
   through the Docker host, so it sets one.
3. **The sensors use ABP with fixed keys** and skip the frame-counter check, so restarting the
   simulator is harmless. A real device would use OTAA.
4. **Changing a device's DevEUI is not instant.** A moved or removed identifier takes up to about
   five minutes to propagate; until then uplinks for it are refused and kept in the dead-letter
   store rather than stored on the wrong device.

## For a real deployment

- Put your instance behind a public certificate and give the HTTP integration an `https://` URL.
  A hosted network server will not trust a self-signed certificate.
- Use OTAA, register real DevEUIs, and set the codec on the device profile for your hardware.
- Change ChirpStack's admin password and API secret, and do not publish its UI.
- Keep the integration's **device types** as narrow as the fleet: they are the scope the sender can reach.

## Clean up

```bash
docker compose --profile sim --profile setup down -v
rm -f .integration-token
dcctl destroy local lora
```

## Third-party software

| Component | Licence | How it is used |
| --- | --- | --- |
| [ChirpStack](https://www.chirpstack.io/) 4.20.0 | MIT | Run as the published image, pinned by digest. |
| [ChirpStack Gateway Bridge](https://www.chirpstack.io/docs/chirpstack-gateway-bridge/) 4 | MIT | Run as the published image, pinned by digest. |
| [chirpstack-api](https://pypi.org/project/chirpstack-api/) 4.20.0 | MIT | Installed in the `setup` container to call ChirpStack's gRPC API. |
| [grpcio](https://pypi.org/project/grpcio/) 1.84.0 | Apache-2.0 | Installed in the `setup` container. |
| [cryptography](https://pypi.org/project/cryptography/) 45.0.7 | Apache-2.0 / BSD-3-Clause | Installed in the `simulator` container for AES and CMAC. |
| [PostgreSQL](https://www.postgresql.org/) 14 | PostgreSQL | ChirpStack's database; published image, pinned by digest. |
| [Valkey](https://valkey.io/) 8 | BSD-3-Clause | ChirpStack's cache (it speaks the Redis protocol); published image, pinned by digest. |
| [Eclipse Mosquitto](https://mosquitto.org/) 2.0.22 | EPL-2.0 / EDL-1.0 | The MQTT broker between the bridge and ChirpStack; published image, pinned by digest. |
| [python](https://hub.docker.com/_/python) 3.12-slim | PSF-2.0 | Base image for the `setup` and `simulator` containers, pinned by digest. |

`simulator.py` and `configure.py` are licensed with the rest of this repository (Apache-2.0).
