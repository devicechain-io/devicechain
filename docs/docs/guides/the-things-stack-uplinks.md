---
title: Send The Things Network / The Things Stack Uplinks to DeviceChain
description: Connect a The Things Stack application, on The Things Network or on your own deployment, to DeviceChain with a custom webhook, and decode uplinks either with a payload formatter or with a payload decoder in DeviceChain.
---

# Send The Things Network / The Things Stack Uplinks to DeviceChain {#send-the-things-stack-uplinks}

The Things Stack is the network server behind The Things Network, The Things Stack Cloud and self-hosted deployments, and all of them forward uplinks the same way: through a **webhook** on an application, which posts each message as JSON to a URL you give it. DeviceChain receives those posts with an [inbound integration](./inbound-integrations.md) of sender type **The Things Stack v3**, finds the device by its DevEUI or its device id, and stores the readings as measurements of that device. From there they drive dashboards, detection rules and alarms like any other measurement.

The The Things Stack v3 sender type reads The Things Stack's own message format. You do not write an adapter, and a decoder never has to unwrap The Things Stack's envelope: the platform takes the device from `end_device_ids`, the time from `received_at`, the decoded object from `uplink_message.decoded_payload`, the raw frame from `uplink_message.frm_payload` and the port and counter from `f_port` and `f_cnt`.

What is left to decide is **where the frame is decoded**:

| | Option A: The Things Stack decodes | Option B: DeviceChain decodes |
| --- | --- | --- |
| Where the byte layout lives | A payload formatter on the application or the end device | A [payload decoder](./payload-decoders.md) on the DeviceChain device profile |
| What DeviceChain stores | The numbers and booleans in `decoded_payload` | What the decoder produces from `frm_payload` |
| Versioning and tests | The Things Stack's | Published with the profile, with saved test vectors that must pass before it can be published |

Both use the same integration and the same webhook. The steps below are the same up to [step 5](#5-decoding), where you pick one.

## Before you start {#before-you-start}

- A The Things Stack application with end devices that have joined. You need each device's DevEUI, or its end device id if you would rather match on that.
- A DeviceChain instance with a certificate from a public CA. The Things Network and The Things Stack Cloud will not post to an instance with a self-signed certificate; a deployment you run yourself can be given your instance's CA instead.
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

- **Sender type**: **The Things Stack v3**. It cannot be changed later.
- **Token**: the integration's name in the URL, such as `ttn-eu1`. It cannot be changed later.
- **Device types**: `Room sensor`.
- **Device identifier**: **DevEUI**, or **Device id** to match the end device id from The Things Stack instead. Choose **Device id** for devices registered without a DevEUI: under **DevEUI**, a message that carries none is refused with `400`.
- **Store link metrics**: on, if you want the signal strength, signal-to-noise ratio and gateway count of each uplink stored beside its readings, as `lorawan.rssi`, `lorawan.snr` and `lorawan.gateways`.

Choose **Create integration**. The **Integration created** dialog shows the **bearer token** once. Copy it now: it is stored only as a digest and cannot be shown again. If you lose it, **Rotate token** issues a new one and the old one stops working at once.

Over GraphQL, `createIntegration` returns the token in `issuedToken`:

```graphql
mutation {
  createIntegration(request: {
    token: "ttn-eu1"
    name: "The Things Network EU1"
    type: "TTS_V3"
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

For device ids, set `identity: "DEVICE_ID"`. The integration's address is:

```text
https://<host>/api/integrations/<tenant>/<integration>
```

`<host>` is the host you open the console on, `<tenant>` is your tenant's token and `<integration>` is the integration's token. The integration's page in the console shows the full URL under **Connect the sender**, with a copy button.

## 3. Add a webhook in The Things Stack {#3-webhook}

In The Things Stack console, open the application, choose **Integrations → Webhooks**, then **Add webhook**, and pick **Custom webhook**:

| The Things Stack setting | Value |
| --- | --- |
| Webhook ID | Any name, such as `devicechain`. |
| Webhook format | **JSON**. |
| Base URL | `https://<host>/api/integrations/<tenant>/<integration>` |
| Additional headers | One header: name `Authorization`, value `Bearer <bearer-token>`. |
| Enabled event types | **Uplink message**, enabled, with the path `/uplink-message`. |

The Things Stack appends each event type's path to the base URL, so uplinks are posted to `.../<integration>/uplink-message`. DeviceChain reads an uplink from the paths `/uplink-message`, `/uplink`, `/uplinks` and `/up`, and from the base URL itself when the path is left empty. Every other path, such as a join accept or a location solve, is answered `204` and ignored, unless it carries a report on a downlink DeviceChain sent (see [The Things Stack downlink](./inbound-integrations.md#lorawan-downlink-tts)). There is no need to enable them.

Leave **Downlink API key** empty; DeviceChain does not use it. The token goes only in the header. Do not put it in the URL, where it would be written to access logs.

## 4. Register the devices {#4-devices}

DeviceChain finds the device an uplink is about by a device **alias**. Create a device of the type from step 1 for each sensor, with the alias the integration's **Device identifier** reads:

| Device identifier | Alias scheme | Value |
| --- | --- | --- |
| DevEUI | `lorawan.deveui` (**LoRaWAN DevEUI** in the console) | The 16 hexadecimal digits. Upper or lower case is fine. |
| Device id | `tts.device-id` (**Network server device id** in the console) | The end device id, such as `room-4-sensor`. |

In the console, use the **Aliases** field of the device form: **Add alias**, pick the scheme, and type the value. Over GraphQL:

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

You can also let the sensors tell you. An uplink whose identifier no device of the integration's device types carries is not stored, and the identifier is listed on the integration's page under **Unregistered devices seen**. **Create a device with this alias** opens the device form with the alias filled in. See [Unregistered devices](./inbound-integrations.md#unregistered).

## 5. Choose where the frame is decoded {#5-decoding}

### Option A: The Things Stack decodes with a payload formatter {#option-a-formatter}

In The Things Stack, set an uplink **payload formatter** on the application, or on an end device, which overrides the application's. Many devices have a ready-made formatter in the LoRaWAN Device Repository. For a custom JavaScript formatter, write a `decodeUplink` function; The Things Stack sends what it returns under `data` as the uplink's `decoded_payload`. For the room sensor, whose six-byte frame on port 2 holds a signed temperature in tenths of a degree, a humidity in half-percent steps, the battery in millivolts and the door contact in bit 0 of the last byte:

```js
function decodeUplink(input) {
  var b = input.bytes;
  if (input.fPort !== 2 || b.length < 6) {
    return { errors: ["unexpected frame on port " + input.fPort] };
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

On the DeviceChain side there is nothing more to do: leave the device profile **without** a decoder. DeviceChain flattens `decoded_payload` into readings:

- every number is a reading, under its key; a nested value is keyed by its dot-joined path (`{"battery": {"mv": 3300}}` is `battery.mv`);
- `true` and `false` are stored as `1` and `0`;
- strings and `null` are skipped, so a formatter that returns `"label": "north"` stores nothing for it.

Name the keys the formatter returns after the metrics you declared, or declare the metrics under the keys the formatter returns. A Device Repository formatter decides its own keys; the uplink's **Live data** in The Things Stack console shows them. [What is stored](./inbound-integrations.md#what-is-stored) has the full rules.

An uplink with no `decoded_payload`, or one with no number or boolean in it, is acknowledged and stores nothing. The integration counts it as **undecoded** and keeps a sample of its frame: that is what an uplink with no payload formatter looks like.

:::tip Reshaping a formatter's output
If a formatter returns other keys or units than your profile uses (`temp_f` in Fahrenheit, `bat.mv`), you do not have to change the formatter. A DeviceChain decoder over the **Decoded object** input can rename and convert them; [Reshaping a decoded object](./payload-decoders.md#example-tts) is that exact case.
:::

### Option B: DeviceChain decodes the raw frame {#option-b-decoder}

Give the device profile a [payload decoder](./payload-decoders.md), and The Things Stack needs no payload formatter. Every uplink carries the raw frame in `frm_payload`, and the integration hands the decoder:

| Decoder name | From The Things Stack's message |
| --- | --- |
| `frame` | `uplink_message.frm_payload`, the frame's bytes |
| `fPort` | `uplink_message.f_port` |
| `fCnt` | `uplink_message.f_cnt` |
| `decoded` | `uplink_message.decoded_payload`, when a payload formatter ran |
| `link` | `rx_metadata`'s strongest reception and gateway count, when **Store link metrics** is on |

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

Try the decoder before you publish. In the **Test** panel choose **Frame**, paste `00D75A0CE401` and set `fPort` to 2, or choose **Webhook body**, pick the integration, paste a whole message as The Things Stack posts it and set the message type to `uplink-message`. Then **publish** the profile. A newly published decoder takes up to about a minute to be picked up.

Once the device type has a decoder, the decoder decides what is stored. If a payload formatter also ran, its `decoded_payload` is not stored; it is only available to the decoder as `decoded`. A decode that fails stores nothing from that uplink and is recorded as a failed event with the reason `DECODE_FAILED`; the profile's **Decoder** tab shows it under **Health** and **Recent failures**. [Decoder refusals](./payload-decoders.md#refusal-reasons) lists the reasons.

A DeviceChain decoder is not JavaScript. It is a list of typed expressions with no loops, so a JavaScript formatter cannot be pasted in; its byte reads are rewritten as decoder rows. [Payload decoders](./payload-decoders.md) and the [function reference](../reference/decoder-functions.md) cover the language.

## 6. Check that uplinks arrive {#6-check}

You can test the integration before the webhook is configured, with a request shaped like the one The Things Stack sends. This one has a `decoded_payload` (option A) and the raw frame in `frm_payload` (option B), so it works with either. `received_at` is required and must be no more than 366 days old, so the command fills in the current time:

```bash
curl -i -X POST "https://<host>/api/integrations/<tenant>/ttn-eu1/uplink-message" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "end_device_ids": {
      "device_id": "room-4-sensor",
      "application_ids": { "application_id": "my-application" },
      "dev_eui": "70B3D57ED0050004"
    },
    "received_at": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
    "uplink_message": {
      "f_port": 2,
      "f_cnt": 1,
      "frm_payload": "ANdaDOQB",
      "decoded_payload": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": true }
    }
  }'
```

`202 Accepted` means the request was read and the token is good; it does not mean anything was stored. Then look in DeviceChain:

- The device's page shows the readings: `temperature` 21.5, `humidity` 45, `battery` 3300 and `door` 1.
- The integration's page shows **Rejected token** if a request failed authentication, an **undecoded** count for uplinks with nothing to store, and **Unregistered devices seen** for identifiers no device in scope carries.
- With option B, the profile's **Decoder** tab shows how many uplinks decoded and failed.

Once the webhook is enabled, the real uplinks show up the same way. The application's **Live data** in The Things Stack console shows the uplinks it forwarded.

## Troubleshooting {#troubleshooting}

| What you see | Likely cause |
| --- | --- |
| `401` | The bearer token is wrong or was rotated, or the tenant or integration token in the base URL is wrong. The integration shows **Rejected token**. |
| `403` | The integration is switched off. Turn **Accepting data** back on. |
| `400` | The response body names the field: no DevEUI under **DevEUI** (choose **Device id**), a device id or DevEUI in the wrong form, a missing `received_at` or one more than 366 days old, or a body that is not JSON (set the webhook format to **JSON**). |
| `204`, and nothing is stored | The uplink was posted to a path DeviceChain does not read as an uplink. Use `/uplink-message`. |
| `202`, but nothing is stored | No device in the integration's device types carries the identifier (see **Unregistered devices seen**), or the uplink had nothing to store (see the undecoded count), or, with option B, the decode failed (see the **Decoder** tab). |
| Readings arrive with no unit | No metric is declared on the profile for that key, or the profile has not been published since it was. |
| The webhook cannot connect at all | The Things Stack does not trust the instance's certificate. |

[Why isn't my data showing up](./why-isnt-my-data-showing-up.md) walks through the platform's record of what it refused, and [What the sender is told](./inbound-integrations.md#responses) lists every answer.

## Next steps {#next-steps}

- Turn readings into alarms: [LoRaWAN Uplink to Alarm](./lorawan-uplink-to-alarm.md) carries the same sensor on to a detection rule, an alarm and a command.
- Send commands back down through The Things Stack: [The Things Stack downlink](./inbound-integrations.md#lorawan-downlink-tts).
- Understand duplicates and retries: [Duplicates and retries](./inbound-integrations.md#duplicates). A retried post carries the same DevEUI, frame counter and `received_at`, so it is stored once.
- Keep the token safe: [What an integration token can do](./inbound-integrations.md#trust).

## See also {#see-also}

The Things Stack's own documentation for the settings on its side:

- [Creating webhooks](https://www.thethingsindustries.com/docs/integrations/webhooks/creating-webhooks/): the custom webhook form, the base URL and the event type paths.
- [Data formats](https://www.thethingsindustries.com/docs/integrations/data-formats/): the uplink message and its fields.
- [Payload formatters](https://www.thethingsindustries.com/docs/integrations/payload-formatters/): application and end device formatters, and the JavaScript `decodeUplink` function.
