<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# lorawan-uplink-to-alarm: a binary uplink, a decoder, a rule, an alarm

The path from a raw LoRaWAN frame to an alarm, with no hardware and no network server to run:

1. `dc-simulator` plays a **network server** with no codec for the sensor (ChirpStack v4 or The Things
   Stack v3). It posts the raw six-byte application frame, with no decoded object, to a tenant's
   **inbound integration**.
2. The integration finds the device by its **DevEUI alias** and hands the frame to the **payload
   decoder** on the device's profile, which turns six bytes into `temperature`, `humidity`,
   `battery.voltage` and `door`, with the units the profile declares.
3. A **detection rule** on the profile says: *above 25 degrees, raise a warning alarm.* The sensor's
   simulated temperature climbs past 25 and falls back, and the alarm **clears by itself**.

```
 dc-simulator                         DeviceChain (kind cluster)
┌──────────────────────┐  HTTPS POST  ┌─────────────────────────────────────────────────────────────┐
│ plays ChirpStack or  │─────────────▶│ inbound integration ──▶ decoder on the device profile       │
│ The Things Stack     │  raw frame,  │   finds the device        six bytes ──▶ temperature,        │
│                      │  no decoded  │   by its DevEUI           humidity, battery.voltage, door   │
└──────────────────────┘  object      │                                   │                         │
                                      │                                   ▼                         │
                                      │ alarm room-too-warm ◀── detection rule  temperature > 25   │
                                      └─────────────────────────────────────────────────────────────┘
```

The guide that explains each step is [LoRaWAN Uplink to Alarm](https://docs.devicechain.io/next/guides/lorawan-uplink-to-alarm).
This directory is the same walkthrough as scripts.

| File | What it is |
| --- | --- |
| [`decoder.json`](decoder.json) | The decoder document. It is exactly what `dc-simulator --lorawan-print-decoder` prints, and `run.sh` checks that it still is. |
| [`rule.json`](rule.json) | The detection rule, as the console's form saves it. |
| [`provision.sh`](provision.sh) | The tenant, the `room-sensor` profile (four metrics, the decoder, the rule), its publication, a device type, the device and two integrations. |
| [`run-simulator.sh`](run-simulator.sh) | The simulator invocation: `dc-simulator --lorawan-ns chirpstack --lorawan-binary ...`. |
| [`show-state.sh`](show-state.sh) | The device's latest readings and its alarms. |
| [`run.sh`](run.sh) | All of the above in order, then waits for the alarm to be raised and cleared. Exits non-zero if it is not. |

## Requirements

**DeviceChain v1.2 or later**, or an instance built from `main`: live decoding of integration uplinks
and the simulator's `--lorawan-binary` mode arrived in v1.2. On v1.1 an uplink with no decoded object
stores nothing.

`dcctl`, `dc-simulator` (attached to every release from v1.2, beside `dcctl`), `docker`, `kind`,
`kubectl`, `jq` and `curl`.

Run every script from this directory. They talk to `http://localhost` (the ingress of a default
`dcctl install local`). Set `DC_URL` if yours is elsewhere, `KUBECONFIG` or `KUBE_CONTEXT` to pick
the cluster, `INSTANCE` for the instance name (default `devicechain`), `DC_SIMULATOR` for the
simulator binary if it is not on `PATH`, and `DC_INTEGRATION_HOST` when the ingress host does not
resolve from your machine.

## Run it

```bash
# 1. DeviceChain.
dcctl install local --compact
dcctl bootstrap local devicechain --host localhost --no-tls --no-escrow

# 2. Everything: provision, send twelve uplinks, wait for the alarm, show the result.
./run.sh
```

Or step by step:

```bash
./provision.sh        # tenant, profile with decoder and rule (published), device, integrations
./run-simulator.sh    # twelve uplinks, two seconds apart, as a ChirpStack
NS=tts ./run-simulator.sh   # the same, as The Things Stack
./show-state.sh
```

### What provisioning did

`provision.sh` created the tenant `lora-alarm`; a device profile `room-sensor` that declares the
metrics `temperature` (`Cel`), `humidity` (`%`), `battery.voltage` (`V`) and `door` (boolean); saved
`decoder.json` on the profile's draft; created the rule `room-too-warm`; and **published** the
profile. Decoders and rules reach live uplinks only from a published version, and publishing is where
the platform compiles both and runs the decoder's saved vector. It then created a device type, the
device `room-4` carrying the alias `lorawan.deveui` = `70B3D57ED0050004`, and two integrations,
`chirpstack` (`CHIRPSTACK_V4`) and `tts` (`TTS_V3`), both scoped to the device type, with link
metrics on. Each integration's bearer token is shown once by the server; the script keeps it in
`.token-chirpstack` and `.token-tts` (mode 0600, git-ignored).

The frame the simulator sends:

| Byte | Meaning |
| --- | --- |
| 0 | Layout version, always `1` |
| 1 to 2 | Temperature in tenths of a degree, signed, big-endian |
| 3 | Humidity in percent |
| 4 | Battery voltage in tenths of a volt |
| 5 | Bit 0 is the door contact |

The DevEUI `70B3D57ED0050004` is one whose simulated temperature runs 22.4, 22.9, ... 26.9 over ten
uplinks and then starts again, so twelve uplinks cross 25 on the seventh and fall back on the eleventh.

## See it work

```bash
./show-state.sh
```

```
== room-4: latest readings ==
    battery.voltage = 3.1 V
    door = 1 
    humidity = 57 %
    lorawan.gateways = 2 
    lorawan.rssi = -80 
    lorawan.snr = 8.5 
    temperature = 22.9 Cel
== alarms ==
    room-too-warm  WARNING  CLEARED  raised 2026-11-06T20:45:11.609382Z  cleared 2026-11-06T20:45:19.624828Z  last value 25.4
```

The readings carry the units the profile declares, and the three `lorawan.` readings are the link
metrics the integration appends. The alarm was raised by the seventh uplink (25.4 degrees) and cleared
by the eleventh (22.4), eight seconds apart; nothing authors the clear.

The simulator prints the platform's answer as one line of JSON, so you can see the integration took
every uplink:

```
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"runId":"1793997899191088500"}
```

## Results (kind, DeviceChain built from `main` at 439a5accf)

| Check | Result |
| --- | --- |
| Profile with a decoder and a rule publishes | yes; the compiler accepted both and the decoder's vector passed |
| ChirpStack-shaped binary uplinks (`data`, `fPort`, no `object`) | 12 of 12 answered `202`, decoded into four readings |
| The Things Stack-shaped binary uplinks (`frm_payload`, `f_port`, no `decoded_payload`) | 3 of 3 answered `202`, same readings from the same decoder |
| Link metrics appended | `lorawan.rssi`, `lorawan.snr`, `lorawan.gateways` |
| Alarm raised when the reading passes 25 | yes, `WARNING`, at the 25.4 reading |
| Alarm clears without being authored | yes, at the first reading at or below 25 |
| Decode failure (the frame on port 9, which no section accepts) | answered `202`; recorded as `DECODE_FAILED` (`no_section`) under `dcctl events failed`, nothing stored |

## Things worth knowing

1. **A published decoder takes up to a minute to be picked up** by `event-sources`. An uplink sent
   inside that window can still be stored as the network server decoded it, which with no decoded
   object means stored as nothing. If you run `run.sh` the instant the profile is published and
   see no alarm, wait a minute and run `./run-simulator.sh` again.
2. **The network server is told `202` even when the decode fails.** The refusal is a record on your
   side (`dcctl events failed -t <tenant> --email <you> --password <pw> --reason decode-failed`).
3. **A decoder wins over the network server's own decoding.** With a decoder published for the device
   type, the uplink is decoded by it and what the network server decoded is not stored. A real
   deployment needs no codec or payload formatter for a device that has a decoder here.
4. **The simulator's token goes in through the environment** (`DC_SIM_INTEGRATION_TOKEN`), so it
   stays out of the process list.
5. **`run.sh` checks `decoder.json` against the simulator.** If a `dc-simulator` ever changes its
   frame layout, the check fails instead of the decoder quietly reading the wrong bytes.

## For a real deployment

- Put the instance behind a public certificate and give the network server an `https://` URL. A hosted
  network server will not trust a self-signed certificate.
- Replace `decoder.json` with a decoder written from your sensor's data sheet, keep one saved vector
  for every layout it sends, and test it with real frames in the Decoder tab or `testPayloadDecoder`.
- Register real DevEUIs as `lorawan.deveui` aliases (or device ids as `tts.device-id`).
- Keep each integration's device types as narrow as the fleet: they are the scope the sender can reach.
- Point ChirpStack's HTTP integration (JSON encoding, `Authorization: Bearer <token>`) or The Things
  Stack's custom webhook (JSON, `/uplink-message`) at `https://<host>/api/integrations/<tenant>/<integration>`.

## Clean up

```bash
rm -f .token-chirpstack .token-tts
dcctl destroy local devicechain
```

`dcctl destroy` removes the instance, and with it the tenant.
