---
title: Send ChirpStack Uplinks to DeviceChain
description: Connect a ChirpStack v4 application to DeviceChain with its HTTP integration, and decode uplinks either with a ChirpStack codec or with a payload decoder in DeviceChain.
---

# Send ChirpStack Uplinks to DeviceChain {#send-chirpstack-uplinks}

ChirpStack v4 forwards what its gateways hear through an **HTTP integration** on an application: it posts every event to a URL you give it, as JSON. DeviceChain receives those posts with an [inbound integration](./inbound-integrations.md) of sender type **ChirpStack v4**, finds the device by its DevEUI, and stores the readings as measurements of that device. From there they drive dashboards, detection rules and alarms like any other measurement.

The ChirpStack v4 sender type reads ChirpStack's own event format. You do not write an adapter, and a decoder never has to unwrap ChirpStack's envelope: the platform takes the DevEUI from `deviceInfo.devEui`, the time from `time`, the decoded object from `object`, the raw frame from `data` and the port and counter from `fPort` and `fCnt`.

What is left to decide is **where the frame is decoded**:

| | Option A: ChirpStack decodes | Option B: DeviceChain decodes |
| --- | --- | --- |
| Where the byte layout lives | A JavaScript codec on the ChirpStack device profile | A [payload decoder](./payload-decoders.md) on the DeviceChain device profile |
| What DeviceChain stores | The numbers and booleans in ChirpStack's `object` | What the decoder produces from `data` |
| Versioning and tests | ChirpStack's | Published with the profile, with saved test vectors that must pass before it can be published |

Both use the same integration and the same ChirpStack settings. The steps below are the same up to [step 5](#5-decoding), where you pick one.

## Before you start {#before-you-start}

- A ChirpStack v4 server with an application, a device profile and devices that have joined. You need each device's DevEUI.
- A DeviceChain instance that ChirpStack can reach over HTTPS. A ChirpStack you run yourself can be given your instance's CA; a hosted one needs a certificate from a public CA on the instance.
- A tenant member with the `device:write` permission. The console steps use the tenant console; every step also has a GraphQL form, sent to `https://<host>/api/device-management/graphql` with a tenant access token in the `Authorization: Bearer` header ([how to get one](./lorawan-uplink-to-alarm.md#1-get-a-tenant-token)).

## 1. A device type for the sensors {#1-device-type}

The integration can reach only devices of the device types you scope it to, so start with a device type and its profile. Declare a [metric](../user-guide/device-types-and-profiles.md#metrics) for each value the sensor reports, under the key the decoded value will have: the metric gives the reading its unit and validation. This guide uses a room sensor that reports a temperature, a humidity, a battery voltage in millivolts and a door contact:

| Metric key | Data type | Unit |
| --- | --- | --- |
| `temperature` | Double | `Cel` |
| `humidity` | Double | `%` |
| `battery` | Integer | `mV` |
| `door` | Boolean | |

In the console, create the profile and its metrics as described in [Device types and profiles](../user-guide/device-types-and-profiles.md), create a device type that uses the profile, and **publish** the profile. Over GraphQL, send it as two requests, because one mutation may select at most [five root fields](../reference/graphql-api.md#request-limits) and a sixth gets the whole request refused:

```graphql
mutation {
  createDeviceProfile(request: { token: "room-sensor", name: "Room sensor", category: "sensor" }) { token }
  temperature: createMetricDefinition(request: { token: "room-sensor-temperature", deviceProfileToken: "room-sensor", metricKey: "temperature", name: "Temperature", dataType: "DOUBLE", unit: "Cel" }) { metricKey }
  humidity: createMetricDefinition(request: { token: "room-sensor-humidity", deviceProfileToken: "room-sensor", metricKey: "humidity", name: "Humidity", dataType: "DOUBLE", unit: "%" }) { metricKey }
  battery: createMetricDefinition(request: { token: "room-sensor-battery", deviceProfileToken: "room-sensor", metricKey: "battery", name: "Battery", dataType: "INT", unit: "mV" }) { metricKey }
  door: createMetricDefinition(request: { token: "room-sensor-door", deviceProfileToken: "room-sensor", metricKey: "door", name: "Door", dataType: "BOOLEAN" }) { metricKey }
}
```

```graphql
mutation {
  createDeviceType(request: { token: "room-sensor", name: "Room sensor", profileToken: "room-sensor" }) { token }
}
```

If you choose [option B](#option-b-decoder), add the decoder before you publish; otherwise publish now:

```graphql
mutation { publishDeviceProfile(token: "room-sensor", label: "metrics") { version } }
```

A decoded key with no metric is still stored, without a unit.

## 2. Create the integration {#2-integration}

In the console, open **Connect → Integrations** and choose **New integration**:

- **Sender type**: **ChirpStack v4**. It cannot be changed later.
- **Token**: the integration's name in the URL, such as `chirpstack-eu868`. It cannot be changed later.
- **Device types**: `Room sensor`.
- **Device identifier**: **DevEUI**.
- **Store link metrics**: on, if you want the signal strength, signal-to-noise ratio and gateway count of each uplink stored beside its readings, as `lorawan.rssi`, `lorawan.snr` and `lorawan.gateways`.

Choose **Create integration**. The **Integration created** dialog shows the **bearer token** once. Copy it now: it is stored only as a digest and cannot be shown again. If you lose it, **Rotate token** issues a new one and the old one stops working at once.

Over GraphQL, `createIntegration` returns the token in `issuedToken`:

```graphql
mutation {
  createIntegration(request: {
    token: "chirpstack-eu868"
    name: "ChirpStack EU868"
    type: "CHIRPSTACK_V4"
    enabled: true
    deviceTypeTokens: ["room-sensor"]
    identity: "DEV_EUI"
    linkMetrics: true
  }) {
    integration { token type identity }
    issuedToken
  }
}
```

The integration's address is:

```text
https://<host>/api/integrations/<tenant>/<integration>
```

`<host>` is the host you open the console on, `<tenant>` is your tenant's token and `<integration>` is the integration's token. The integration's page in the console shows the full URL under **Connect the sender**, with a copy button.

## 3. Add the HTTP integration in ChirpStack {#3-chirpstack-http}

In the ChirpStack web interface, open the application, go to **Integrations**, and add the **HTTP** integration:

| ChirpStack setting | Value |
| --- | --- |
| Payload encoding | **JSON**. DeviceChain refuses the Protobuf encoding with `415`. |
| Event endpoint URL(s) | `https://<host>/api/integrations/<tenant>/<integration>`, with nothing after it. ChirpStack adds `?event=up` (or another event name) to every request itself. |
| Headers | One header: name `Authorization`, value `Bearer <bearer-token>`. |

Save it. From now on ChirpStack posts every event of the application's devices to DeviceChain. Only `up` events are stored. `join`, `status`, `log` and the other events are answered `204` and ignored, and `ack` and `txack` are read only when the operator has switched on [LoRaWAN downlink](./inbound-integrations.md#lorawan-downlink), so you can leave every event enabled.

The token is sent only in the header. Do not put it in the URL, where it would be written to access logs.

## 4. Register the devices by DevEUI {#4-devices}

DeviceChain finds the device an uplink is about by a device **alias** that holds its DevEUI. Create a device of the type from step 1 for each sensor, with the alias `lorawan.deveui`. In the console, use the **Aliases** field of the device form: **Add alias**, **LoRaWAN DevEUI**, and the 16 hexadecimal digits. Upper or lower case is fine. Over GraphQL:

```graphql
mutation {
  createDevice(request: {
    token: "room-4"
    name: "Room 4"
    deviceTypeToken: "room-sensor"
    aliases: [{ scheme: "lorawan.deveui", value: "70B3D57ED0050004" }]
  }) { token aliases { scheme value } }
}
```

You can also let the sensors tell you. An uplink whose DevEUI no device of the integration's device types carries is not stored, and its DevEUI is listed on the integration's page under **Unregistered devices seen**. **Create a device with this alias** opens the device form with the DevEUI filled in. See [Unregistered devices](./inbound-integrations.md#unregistered).

## 5. Choose where the frame is decoded {#5-decoding}

### Option A: ChirpStack decodes with a codec {#option-a-codec}

In ChirpStack, open the device profile your sensors use, go to its **Codec** tab, choose JavaScript functions as the payload codec, and write a `decodeUplink` function. ChirpStack runs it on each uplink and sends what it returns under `data` as the uplink event's `object`. For the room sensor, whose six-byte frame on port 2 holds a signed temperature in tenths of a degree, a humidity in half-percent steps, the battery in millivolts and the door contact in bit 0 of the last byte:

```js
function decodeUplink(input) {
  var b = input.bytes;
  if (input.fPort !== 2 || b.length < 6) {
    throw new Error("unexpected frame on port " + input.fPort);
  }
  var t = (b[0] << 8) | b[1];
  if (t & 0x8000) t -= 0x10000;
  return {
    data: {
      temperature: t / 10,
      humidity: b[2] / 2,
      battery: (b[3] << 8) | b[4],
      door: (b[5] & 1) === 1
    }
  };
}
```

On the DeviceChain side there is nothing more to do: leave the device profile **without** a decoder. DeviceChain flattens `object` into readings:

- every number is a reading, under its key; a nested value is keyed by its dot-joined path (`{"battery": {"mv": 3300}}` is `battery.mv`);
- `true` and `false` are stored as `1` and `0`;
- strings and `null` are skipped, so a codec that returns `"status": "ok"` stores nothing for it.

Name the keys the codec returns after the metrics you declared, or declare the metrics under the keys the codec returns. [What is stored](./inbound-integrations.md#what-is-stored) has the full rules.

An uplink whose event carries no `object`, or one with no number or boolean in it, is acknowledged and stores nothing. The integration counts it as **undecoded** and keeps a sample of its frame: that is what an uplink from a device profile with no codec looks like.

:::tip Reshaping a codec's output
If a vendor codec returns other keys or units than your profile uses (`temp_f` in Fahrenheit, `bat.mv`), you do not have to change the codec. A DeviceChain decoder over the **Decoded object** input can rename and convert them; see [Reshaping a decoded object](./payload-decoders.md#example-tts).
:::

### Option B: DeviceChain decodes the raw frame {#option-b-decoder}

Give the device profile a [payload decoder](./payload-decoders.md) and ChirpStack needs no codec. Every `up` event carries the raw frame in `data`, and the integration hands the decoder:

| Decoder name | From ChirpStack's event |
| --- | --- |
| `frame` | `data`, the frame's bytes |
| `fPort` | `fPort` |
| `fCnt` | `fCnt` |
| `decoded` | `object`, when ChirpStack has a codec |
| `link` | `rxInfo`'s strongest reception and gateway count, when **Store link metrics** is on |

In the console, open the profile's draft, go to the **Decoder** tab, set the input to **Binary frame**, and add a section for **fPort** 2 with four rows. This is the document it saves, and the same document can be set over GraphQL with `setDeviceProfileDecoder(profileToken: "room-sensor", document: ...)`:

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "uplink",
      "when": "fPort == 2",
      "outputs": [
        { "key": "temperature", "expr": "double(i16be(frame, 0)) / 10.0" },
        { "key": "humidity", "expr": "double(u8(frame, 2)) / 2.0" },
        { "key": "battery", "expr": "u16be(frame, 3)" },
        { "key": "door", "expr": "bit(frame, 5, 0)" }
      ]
    },
    { "name": "other ports", "ignore": true }
  ],
  "vectors": [
    {
      "name": "room sensor",
      "input": { "frameHex": "00D75A0CE401", "fPort": 2 },
      "expect": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": 1 }
    }
  ]
}
```

The second section accepts uplinks on any other port and stores nothing for them; without it, they would fail to decode with `no_section`. The vector is a saved test: publishing runs it, and refuses the publish if the decoder does not produce exactly those readings.

Try the decoder before you publish. In the **Test** panel choose **Frame**, paste `00D75A0CE401` and set `fPort` to 2, or choose **Webhook body**, pick the integration and paste a whole event as ChirpStack posts it. Then **publish** the profile. A newly published decoder takes up to about a minute to be picked up.

Once the device type has a decoder, the decoder decides what is stored. If ChirpStack also has a codec, its `object` is not stored; it is only available to the decoder as `decoded`. A decode that fails stores nothing from that uplink and is recorded as a failed event with the reason `DECODE_FAILED`; the profile's **Decoder** tab shows it under **Health** and **Recent failures**. [Decoder refusals](./payload-decoders.md#refusal-reasons) lists the reasons.

A DeviceChain decoder is not JavaScript. It is a list of typed expressions with no loops, so a vendor's JavaScript codec cannot be pasted in; its byte reads are rewritten as decoder rows. [Payload decoders](./payload-decoders.md) and the [function reference](../reference/decoder-functions.md) cover the language.

## 6. Check that uplinks arrive {#6-check}

You can test the integration before ChirpStack is configured, with a request shaped like the one ChirpStack sends. This one has a decoded `object` (option A) and the raw frame in `data` (option B), so it works with either; the `deduplicationId` must be a UUID, and a new one each time, or the uplink is stored once:

```bash
curl -i -X POST "https://<host>/api/integrations/<tenant>/chirpstack-eu868?event=up" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "deduplicationId": "'"$(uuidgen)"'",
    "deviceInfo": { "devEui": "70b3d57ed0050004" },
    "fPort": 2,
    "fCnt": 1,
    "data": "ANdaDOQB",
    "object": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": true }
  }'
```

`202 Accepted` means the request was read and the token is good; it does not mean anything was stored. Then look in DeviceChain:

- The device's page shows the readings: `temperature` 21.5, `humidity` 45, `battery` 3300 and `door` 1.
- The integration's page shows **Rejected token** if a request failed authentication, an **undecoded** count for uplinks with nothing to store, and **Unregistered devices seen** for DevEUIs no device in scope carries.
- With option B, the profile's **Decoder** tab shows how many uplinks decoded and failed.

Once ChirpStack is sending, the real uplinks show up the same way. ChirpStack's own device page shows the `up` events it forwarded.

## Troubleshooting {#troubleshooting}

| What you see | Likely cause |
| --- | --- |
| `401` | The bearer token is wrong or was rotated, or the tenant or integration token in the URL is wrong. The integration shows **Rejected token**. |
| `403` | The integration is switched off. Turn **Accepting data** back on. |
| `415` | The HTTP integration's payload encoding is Protobuf. Set it to **JSON**. |
| `400` | The response body names the field: a DevEUI that is not 16 hexadecimal digits, a `deduplicationId` that is not a UUID, a `time` more than 366 days old, or a request with no `?event=` (an endpoint URL that does not end where ChirpStack can add it). |
| `202`, but nothing is stored | No device in the integration's device types carries the DevEUI (see **Unregistered devices seen**), or the uplink had nothing to store (see the undecoded count), or, with option B, the decode failed (see the **Decoder** tab). |
| Readings arrive with no unit | No metric is declared on the profile for that key, or the profile has not been published since it was. |
| ChirpStack cannot connect at all | ChirpStack does not trust the instance's certificate. |

[Why isn't my data showing up](./why-isnt-my-data-showing-up.md) walks through the platform's record of what it refused, and [What the sender is told](./inbound-integrations.md#responses) lists every answer.

## Next steps {#next-steps}

- Turn readings into alarms: [LoRaWAN Uplink to Alarm](./lorawan-uplink-to-alarm.md) carries the same sensor on to a detection rule, an alarm and a command.
- Send commands back down through ChirpStack: [LoRaWAN downlink](./inbound-integrations.md#lorawan-downlink).
- Understand duplicates and retries: [Duplicates and retries](./inbound-integrations.md#duplicates). ChirpStack's `deduplicationId` is the key, so a retried post is stored once.
- Keep the token safe: [What an integration token can do](./inbound-integrations.md#trust).

## See also {#see-also}

ChirpStack's own documentation for the settings on its side:

- [HTTP integration](https://www.chirpstack.io/docs/chirpstack/integrations/http.html): the endpoint, the `event` query parameter and the JSON encoding.
- [Event types](https://www.chirpstack.io/docs/chirpstack/integrations/events.html): the `up` event and its fields.
- [Device profiles](https://www.chirpstack.io/docs/chirpstack/use/device-profiles.html): the **Codec** tab and JavaScript codec functions.
