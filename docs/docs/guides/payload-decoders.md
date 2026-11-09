---
sidebar_position: 3.8
title: Payload Decoders
---

# Payload Decoders {#payload-decoders}

A **payload decoder** turns what a device or a network server sent into measurements. You write it once, on a [device profile](../user-guide/device-types-and-profiles.md), as a short list of rules: read two bytes at offset 1 as a signed number, divide by ten, store it as `temperature`. It reads a binary frame, a JSON body or the object a network server already decoded, and it produces the same measurements as any other source, with the units and validation of the metrics you declared.

:::note Live decoding is in v1.2
A decoder you publish runs on the live uplinks of an [inbound integration](./inbound-integrations.md): when the device an uplink is about has a device type whose profile version carries a decoder, the uplink is decoded with it instead of being stored as the network server decoded it. A newly published decoder can take up to about a minute to be picked up. A device type with no decoder is stored as before. Rolling the upgrade out is described in [Releases and upgrades](../deployment/releases-and-upgrades.md#v120-payload-decoders).

The same decoder also reads a payload a device sends on its own connection, over HTTP or MQTT, when the operator has turned that on. See [A device's own connection](#device-connection).
:::

## When you need one {#when}

You probably do not need one when the network server already decodes the frame. ChirpStack with a codec and The Things Stack with a payload formatter send a decoded object, and an integration [stores its numbers as they are](./inbound-integrations.md#what-is-stored). You need a decoder when:

- the network server has no codec or formatter for the device, and you have the byte layout of its frames;
- you want to rename, rescale or reshape what the network server decoded, such as converting Fahrenheit to Celsius or reading `bat.mv` as `battery`;
- the sender is a [generic JSON](./inbound-integrations.md#generic-json) or [generic binary](./inbound-integrations.md#generic-binary) integration and you want more than the numbers it flattens;
- the frame carries a Cayenne LPP payload, which `lpp.decode` reads in one call;
- the device connects to the platform itself, with no network server in between, and publishes a compact binary frame or JSON in a shape of its own: it can [send that payload as it is](#device-connection).

## What a decoder is {#what-it-is}

A decoder belongs to a device profile and travels with it:

- You edit it on the profile's **draft**, in the **Decoder** tab. Saving writes the draft only.
- **Publishing** the profile freezes the decoder into that version. A published version's decoder never changes, and rolling back to an earlier version brings its decoder back with it.
- An uplink is decoded with the decoder of the version its device resolves through.

The decoder is a document of expressions in a small, loop-free language (CEL). It has one **input** kind it reads, optional **shared values**, and one or more **sections**; each section has **rows**, and each row says which metric it writes and how to compute the value. The language has no clock, no randomness and no side effects, so the same input always gives the same readings.

A decoder can have at most 8 sections, 16 shared values and 20 saved vectors, and the whole document is at most 32 KiB. The [function reference](../reference/decoder-functions.md#decoder-functions-limits) lists the limits.

## Inputs {#inputs}

The decoder's **input** says which part of an uplink it reads. An uplink that does not carry that part fails to decode with `missing_input`; it is never read as zero.

| Input | What it is |
| --- | --- |
| **Binary frame** (`FRAME`) | The raw bytes of the device's message, at most 4 KiB. |
| **Generic JSON** (`JSON`) | One item of a generic JSON integration, without the fields that name the device and the time. At most 64 KiB, nested at most 32 levels. |
| **Decoded object** (`DECODED`) | The object the network server decoded (ChirpStack's `object`, The Things Stack's `decoded_payload`), under the same caps. |

An expression sees these names:

| Name | Type | Meaning |
| --- | --- | --- |
| `frame` | bytes | The raw frame. |
| `json` | map | The generic JSON item. |
| `decoded` | map | The network server's decoded object. |
| `fPort` | int | The LoRaWAN application port, or `-1` when the uplink did not come from a LoRaWAN network server. |
| `fCnt` | int | The LoRaWAN uplink frame counter, or `-1`. |
| `link` | map of doubles | `rssi`, `snr` and `gateways`, when the network server reported them and the integration has **Store link metrics** on. |
| your shared values | the type of their expression | Any value you named under **Shared values**, usable by everything after it. |

The two map inputs are read with constant keys: `decoded.battery.mv` or `decoded["battery"]["mv"]`. A key cannot be computed, so a JSON array cannot be indexed. To read a key that may be missing, test it first with `has(decoded.humidity)`.

ChirpStack supplies `frame` (`data`), `decoded` (`object`), `fPort` and `fCnt`. The Things Stack supplies `frame` (`frm_payload`), `decoded` (`decoded_payload`), `fPort` (`f_port`) and `fCnt` (`f_cnt`). A generic JSON integration supplies `json`, and `frame` as well when you set its `framePointer`. A generic binary integration supplies `frame`.

## Sections and fields {#sections}

A decoder is a list of **sections**, and **the first section whose condition holds is the one that runs**. A section with no condition always matches. Use sections to give each message type its own layout, usually by `fPort`: the Decoder tab has an **fPort** box that is a shortcut for the condition `fPort == 2`, and an **Only when** box for any other condition.

- **A section that is marked to ignore** accepts the uplink and stores nothing. Use it for message types you do not want. It is counted as ignored, not failed.
- **No section matched** fails the decode with `no_section`. Put an ignore section last, with no condition, if you want every other message accepted silently.
- **A section that matched but produced no reading** (every row's condition was false) fails with `no_outputs`.

Each row writes one measurement. A row is one of three kinds.

### The field table {#field-table}

The Decoder tab starts you with a **field table**, and every row of it becomes an expression. You choose:

- **Metric**: the key the value is stored under. Only metrics declared on the profile as measured, not calculated, are offered.
- A metric key may contain dots (`battery.voltage`), but the metric's token may not: tokens take only letters, digits, `-` and `_`. Declare the metric with a token such as `battery-voltage` and keep `battery.voltage` as its key; the decoder row names the key.
- For a frame: the **type** (unsigned or signed 8, 16, 24 or 32 bits, 32 or 64-bit float, a single bit, a bit field, a signed bit field, or BCD digits), the **byte order** where it matters, and the **byte offset**. A bit field also takes a bit offset and a width of 1 to 32; BCD takes a length of 1 to 8 bytes.
- For JSON: the **path** in the message, such as `bat.mv`, whether it is a number or a true/false value, and whether to skip the row silently when the path is absent.
- Optionally **multiply by**, **divide by** and **add**, applied in that order.
- Optionally an **Only when** condition. A row whose condition is false is skipped, with no error and no count.

The table is a convenience that writes the expression for you. A row you convert to an expression stays an expression: nothing turns an expression back into a table row.

### Raw expressions {#raw-cel}

A row of kind **Expression** has a metric key and a free expression. The expression must produce a number or a true/false value; a true/false value is stored as `1` or `0`. Everything the expression may call is on the [function reference](../reference/decoder-functions.md).

```text
double(i16be(frame, 1)) / 10.0
```

The expression language is typed, and three rules catch most first attempts:

- **Whole numbers divide as whole numbers.** `u16be(frame, 3) / 7` drops the remainder. Write `10.0`, or convert with `double(...)`, when you want a fraction.
- **A comparison needs numbers the compiler knows are numbers.** `decoded.t > 1` is refused because `decoded.t` could be anything. Write `double(decoded.t) > 1.0`.
- **An error that the other side of `&&` or `||` settles is not an error.** `has(decoded.x) && double(decoded.x) > 1.0` is simply false when `x` is missing. Any other error fails the decode.

A bit is counted from the least significant bit of the byte: `bit(frame, 0, 0)` is the lowest bit of the first byte and `bit(frame, 0, 7)` is the highest. A bit field continues into the next byte, so `bits(frame, 0, 4, 8)` on the bytes `AB CD` reads eight bits starting at bit 4 of the first byte and gives `0xDA`.

### Maps {#maps}

A row of kind **Map** takes a whole group of keys from one expression, such as `lpp.decode(frame)`, or a sub-object of the input such as `decoded.values`. You give it a **key prefix** and **Most keys** (`maxKeys`, 1 to 64):

- Each key of the map is stored as the prefix plus the key. A key that is not a declared metric is skipped, not an error, because a sensor may add a channel; the test panel lists it as not stored.
- A map with more than `maxKeys` keys fails the decode with `map_overflow`. It is never cut short.
- If more than one row writes the same key in one uplink, one entry is kept and the others are skipped, listed as `collision` in the test panel. A field row wins over a map; between two maps, the earlier row wins.
- A key that starts with `lorawan.` is skipped: that prefix belongs to the link metrics the platform appends.

### Shared values {#shared-values}

A **shared value** is a named expression computed once per uplink, before the sections, in the order you list them. Use one for something several rows need, such as a header byte or a sliced payload:

```text
hdr = u8(frame, 0)
```

A name is letters, digits and `_`, does not start with a digit, and may not be one of the built-in names (`frame`, `fPort`, `math` and so on).

### How many readings {#max-readings}

The decoder's **maximum readings** is the most one uplink can produce: for the largest section, its field and expression rows plus the `maxKeys` of its map rows. It is at most 64 for a section, and it is shown beside the decoder in the Decoder tab as "Up to N readings per uplink". An uplink for a device type with a decoder is counted against the tenant's ingest allowance when it is accepted: at least this many readings (plus three when the integration stores link metrics), more for a decoder that is costly to run, and never more than 256, whatever the uplink ends up producing.

### What gets stored {#output-values}

Every scalar row must name a **declared, measured metric of the same profile**, and the value is written in that metric's type:

| Metric type | What the value must be |
| --- | --- |
| Integer | A whole number within the 64-bit range. `12.0` is accepted, `12.5` is refused. |
| Double | Any finite number. |
| Boolean | A true/false value, or exactly `0` or `1`. |

The value then goes through the metric's own validation (its minimum, maximum and allowed values), as a value sent by a device does. A value that fails stops the whole decode with `invalid_output`. There are no partial results: **if any row fails, nothing from that uplink is stored.**

A key may be written once in a section. `lorawan.*` keys are reserved for link metrics and cannot be outputs, and a calculated metric cannot be an output either, because the platform would drop the value in favour of the computed one. These are refused when you save.

## Vectors and publishing {#vectors}

A **vector** is a saved input with the readings it must produce: a name, a frame (hex or base64), a JSON object or a decoded object, optionally an `fPort` and `fCnt`, and the expected key and value pairs. A vector passes only when the decoder produces **exactly** those keys, with numerically equal values: a missing key fails it and so does an extra one. A vector for an ignore section expects no readings.

You can save a decoder with failing vectors, so work in progress is never lost. **You cannot publish a profile while one of its vectors fails.** Publishing a profile compiles the decoder against the metrics being published and runs every saved vector, and refuses the publish naming the first expression or vector that fails. Rolling back to an earlier version runs the same check on that version, and refuses when its decoder no longer compiles or one of its vectors fails. A decoder can hold up to 20 vectors.

Keep a vector for every message layout the device sends, and one for each case that once failed. They are how you know that changing a decoder did not change what an old message gives.

## The test panel {#test-panel}

The **Test** panel of the Decoder tab runs the decoder **as it is in the editor, saved or not**, with the platform's own compiler and evaluator, against the profile's draft metrics. Nothing is stored. Choose what to test with:

- **Frame**: paste hex or base64 (whitespace is ignored, at most 4,096 bytes), and optionally an `fPort` and `fCnt`.
- **JSON**: a JSON object, given to whichever of `json` or `decoded` the decoder reads.
- **Webhook body**: choose a stored integration and paste the **whole request body**, with its headers, query parameters, message type and content type where the integration reads them. The panel parses it with the same code that parses a live request, so what you see is what the integration will hand the decoder. If the body holds several uplinks, the first is tested and the panel says how many there were. If the integration has **Store link metrics** on, the link readings appear in the result too.

The result shows:

- **Decoded**, **Ignored** or **Failed**, and the name of the section that matched;
- the **readings**, as they would be stored;
- the keys **not stored**, each with its reason: `undeclared` (no such metric is declared), `collision` (another row already writes the key) or `reserved`;
- on failure, the **reason**, the **expression** that failed (such as `sections[0].outputs[1].expr`), the message and the byte offset a reader refused;
- the **cost** the decode spent;
- every saved vector, passing or failing. **Save as vector** turns a successful Frame or JSON test into a vector; it needs permission to edit devices, and a webhook-body test cannot be saved directly.

A document that does not compile is reported in the same place as an error, beside the editor, not as a request failure.

## Cost limits {#cost}

A decoder cannot loop, but it can still be expensive: reading a long slice, decoding a long text or a long LPP frame costs more than reading one number. Each expression therefore has a **cost**, a count of the work it does: about 1 for every name, literal, operator and conversion, the cost shown on each function in the [function reference](../reference/decoder-functions.md#decoder-functions-library) for the call, and 1 per key for a map row.

- **Before it runs**, the platform adds up the worst case. A single expression above **10,000**, or a whole decoder above **50,000**, is refused when you save or publish, and the error names the expression. A full 4 KiB read is about 4,100, so ordinary decoders are far below it.
- **While it runs**, a decode that spends more than it estimated stops with `cost_exceeded`. That cannot happen to a well-formed decoder; it is the fence behind the estimate.
- **Reading JSON costs 1 per byte of the input.** The estimate shown in the Decoder tab includes the parse of the largest allowed JSON (65,536), which is why a decoder over `json` or `decoded` shows an estimated cost in the tens of thousands even when its expressions are small. That figure is not held against the 50,000 limit; a real uplink is priced by its actual size.

A function that reads a fixed number of bytes costs 1 however long the frame is. Slicing, decoding text and `lpp.decode` cost by length, so slice before you decode, and decode only what you need.

## When a decode is refused {#refusals}

### At save and publish {#refusals-save}

Saving, publishing and rolling back refuse a decoder that is not sound, and say where. The GraphQL error carries a code in `extensions.code`:

| Code | Meaning |
| --- | --- |
| `INVALID_DECODER` | The document does not parse (an unknown key is refused, not ignored), does not compile, names a metric that is undeclared, calculated or reserved, writes a key twice, is over a cost ceiling or a size limit, or, at publish or rollback, has a saved vector that fails. The message names the expression or the vector. |
| `UNSUPPORTED_DECODER_KIND` | The document says `"kind": "WASM"`. Only `CEL` runs; the other kind is refused rather than skipped. |
| `RATE_LIMITED` | Too many test requests; see [the test rate limit](#rate-limit). |
| `INVALID_VALUE` | A test request named more than one input, or a webhook body without an integration. |
| `LIMIT_EXCEEDED` | A test input is larger than its limit. |

A save reports every problem the compiler found in one pass, each located by its path, so you can fix them together:

```text
sections[0].outputs[1].key: "nope" is not a declared measured metric of this profile (a calculated metric cannot be a decoder output)
```

### In a test or an uplink {#refusal-reasons}

A decode that fails reports one of these reasons. Nothing is stored for that uplink.

| Reason | Meaning |
| --- | --- |
| `compile_failed` | The decoder does not compile. |
| `env_unsupported` | The decoder names a function-library version this build does not know. |
| `missing_input` | The uplink does not carry the input the decoder reads. |
| `no_section` | No section's condition held. |
| `no_outputs` | The section that matched produced no reading. |
| `eval_error` | An expression failed: a read past the end of the frame, a missing JSON key read without `has()`, a value that is not a finite number, a failed conversion. A read error names the function and the offset, as in `u16be: reads 2 byte(s) at offset 3 of a 2-byte frame (offset 3)`. |
| `invalid_output` | A value cannot be stored for its metric: it is the wrong type, not finite, or outside the metric's limits. |
| `map_overflow` | A map row produced more keys than its `maxKeys`. |
| `cost_exceeded` | The decode spent more than its estimate. |
| `input_too_large` | The frame or the JSON is over its cap. |
| `input_malformed` | The JSON does not parse or nests deeper than 32 levels, or the test input is not valid hex or base64. |
| `unsupported_encoding` | A test-panel failure: the pasted body uses an encoding the integration does not accept, such as ChirpStack's Protobuf encoding. |
| `ignored_message` | A test-panel failure: the integration ignores this message type, such as a join, so there is nothing to decode. |

An uplink whose decode fails is not stored, and is recorded as a failed event with the reason `DECODE_FAILED`; the start of its error text is the reason in this table. It is recorded on the first delivery and not retried.

## A device's own connection {#device-connection}

A device that is not behind an integration can use its type's decoder too. Instead of the [event JSON](../reference/device-protocol.md#device-event), it sends its own payload, bytes and all, to a route made for that, and the decoder on its device type's profile turns the bytes into measurements. It is the same decoder, the same validation of the metrics you declared, and the same budgets, health and samples as for an integration.

**Nothing changes for a device that sends event JSON.** A payload is read by a decoder only when a device addresses the raw-payload route or topic. The `events` route and topic never run a profile decoder, and nothing is chosen by what a body looks like.

### 1. Write the decoder {#device-connection-decoder}

Declare the metrics, write the decoder and publish the profile, as in [the ChirpStack example](#example-chirpstack). A device's own connection has no LoRaWAN port, so `fPort` and `fCnt` are `-1` there: use a section with no condition. This decoder reads a six-byte frame:

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "uplink",
      "outputs": [
        { "key": "temperature", "expr": "double(i16be(frame, 0)) / 10.0" },
        { "key": "humidity", "expr": "double(u8(frame, 2)) / 2.0" },
        { "key": "battery", "expr": "u16be(frame, 3)" },
        { "key": "door", "expr": "bit(frame, 5, 0)" }
      ]
    }
  ],
  "vectors": [
    {
      "name": "room sensor",
      "input": { "frameHex": "00D75A0CE401" },
      "expect": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": 1 }
    }
  ]
}
```

The device's own profile version must be the one that carries the decoder. A newly published decoder can take up to about a minute to be picked up.

What the decoder reads decides what is carried:

| Decoder input | What the device's payload is |
| --- | --- |
| **Binary frame** | The bytes, at most 4 KiB. A larger payload is refused. |
| **Generic JSON** | A JSON object, at most 64 KiB once parsed and 32 levels deep. Over HTTP the request's `Content-Type` must be `application/json` or end in `+json` (the command the console builds sends `application/octet-stream`, so change that header for a JSON decoder); over MQTT the payload must begin with `{`. A body announced as JSON that does not hold a JSON object is refused over HTTP; over MQTT it is read as bytes, and a decoder that needs the JSON records `missing_input`. |
| **Decoded object** | Not available: nothing on a device's connection has decoded the payload, so the decode fails with `missing_input`. |

### 2. Turn the wire on {#device-connection-enable}

Both wires are off until the operator turns them on, and the two settings are independent:

| Wire | Setting | Until it is on |
| --- | --- | --- |
| HTTP | `httpIngest.deviceUplinks` of the `event-sources` area | The route does not exist: `404`. |
| MQTT | `infrastructure.nats.mqttDeviceUplinks` | The broker refuses a publish to the topic. A device receives the grant when it next connects. |

Turn either on only after every `event-sources` and `device-management` replica runs a release that knows it; see [Releases and upgrades](../deployment/releases-and-upgrades.md#v130-device-uplinks) and the [Device Protocol Reference](../reference/device-protocol.md#raw-payload). The console shows the commands below whether or not the setting is on, because it cannot see the setting: a device that gets `404` or is refused by the broker is waiting on the operator.

### 3. Send a payload {#device-connection-send}

The **Send data** tab of a device in the console has a **Raw payload** choice that builds these commands for the device's own credential. Put the six bytes in a file:

```bash
printf '\x00\xd7\x5a\x0c\xe4\x01' > payload.bin
```

**Over HTTP**, `POST` the bytes to `/{instanceId}/{tenant}/devices/{deviceToken}/uplink` on the `event-sources` service (port 8081). A credential is required whatever the instance's authentication mode, and a binary body has nowhere to carry one, so it goes in headers: `Authorization: Bearer <access-token>` for an access token, or `X-DeviceChain-Credential-Type`, `X-DeviceChain-Credential-Id` and `X-DeviceChain-Credential-Secret` for any other type. Sending both forms is refused with `400`.

```bash
curl -i -X POST 'http://<ingest-host>:8081/<instance-id>/<tenant>/devices/<device>/uplink' \
  -H 'Authorization: Bearer <access-token>' \
  -H 'Content-Type: application/octet-stream' \
  --data-binary '@payload.bin'
```

**Over MQTT**, publish the bytes, with no envelope and no credential, to `{instanceId}/{tenant}/devices/{deviceToken}/uplink`. The device authenticates when it connects, exactly as for events, and the broker lets it publish only to the topic with its own token:

```bash
mosquitto_pub --cafile instance-ca.crt \
  -h '<mqtt-host>' -p 1883 \
  -i '<instance-id>:<tenant>:<device>' \
  -u '<tenant>:<access-token>' \
  -t '<instance-id>/<tenant>/devices/<device>/uplink' \
  -f payload.bin
```

A `202` or an MQTT acknowledgement means the payload was received, not that it decoded. Look for the readings on the device's Events tab: `temperature` 21.5, `humidity` 45, `battery` 3300 and `door` 1.

### 4. When it does not work {#device-connection-failures}

HTTP answers the device. Every answer that depends on the device is given only after its credential is proven, so a caller without the credential learns nothing about the device or its type:

| Status | Meaning |
| --- | --- |
| `202` | Received and handed to the decoder. |
| `400` | A credential header is malformed, or the body is announced as JSON and is not an object, or it nests deeper than 32 levels. |
| `401` | No credential, or one that does not authenticate. Every cause gets the same answer. |
| `404` | The route is not turned on. |
| `413` | The body is over the cap for what the decoder reads (4 KiB of frame, 64 KiB of JSON) or over 1 MiB. |
| `422` | The device's type has no decoder. The payload is never read as an event. |
| `429` | The tenant is over its ingest ceiling, by messages or by readings. `Retry-After` says when to retry. |
| `500` | The platform could not build the message. Not retryable by the device. |
| `503` | The decoder lookup is unavailable, ingest is applying backpressure, or the downstream stream could not take the payload. `Retry-After` is set for the first two and says when to retry. |

MQTT cannot answer a device, so the platform records what happened instead. The [Device Protocol Reference](../reference/device-protocol.md#raw-payload) has the full table; in short:

- **A payload that cannot be handed to a decoder** (the device's type has no decoder, no device has the token, or the payload is over a cap) is recorded as a failed decode with the cause, and is never read as an event. An operator sees it as `total_msg_failed_decode` and in `device_uplink_messages_total` by `result`.
- **A decode that runs and fails** is the same on both wires. Nothing from that payload is stored; it is counted in the decoder's health and recorded as a failed event with the reason `DECODE_FAILED`, whose error text names the [reason](#refusal-reasons), and it appears on the console's **Ingest failures** page and, as samples, on the profile's Decoder tab. A decoder that fails past its record budget in a window is counted and not recorded one failure at a time. On HTTP the device was already answered `202`.
- **A tenant over its ceiling** is shed: over HTTP the device is told `429`; over MQTT the message is acknowledged and dropped, and counted in `total_msg_rate_limited` or `total_msg_reading_limited`.
- **A decision the platform cannot make yet** (device-management cannot be asked, or backpressure) is retried: HTTP answers `503`, and an MQTT message waits and is offered again.

An operator watches `device_uplink_requests_total` (HTTP) and `device_uplink_messages_total` (MQTT), each by `result` (`accepted`, `bad_request`, `unauthorized`, `too_large`, `no_decoder`, `rate_limited`, `plan_unavailable`, `backpressure`, `publish_failed`, `internal_error`), and `device_uplink_charged_readings_total` for the readings charged.

A payload counts against the tenant's ingest ceilings once as a message and then as readings, at least the decoder's [maximum readings](#max-readings), as for an integration.

## Authoring over GraphQL {#graphql}

Everything the console does is available on the device-management GraphQL API. Saving and clearing need permission to edit devices; testing needs permission to read them, and a caller limited to part of the tenant is refused.

```graphql
# schema: device-management
mutation SaveDecoder($token: String!, $document: String!) {
  setDeviceProfileDecoder(profileToken: $token, document: $document) {
    token
  }
}
```

The `document` is the decoder as JSON text. `clearDeviceProfileDecoder(profileToken)` withdraws the draft decoder; published versions keep theirs. The profile's draft decoder is `DeviceProfile.decoder`, and the one frozen into a version is `DeviceProfileVersion.decoder`; both report `maxReadings` and `estimatedCost`.

```graphql
# schema: device-management
query TestDecoder($input: PayloadDecoderTestInput!) {
  testPayloadDecoder(input: $input) {
    ok
    ignored
    section
    cost
    readings { key value }
    skipped { key reason }
    error { reason path message offset }
    vectors { name ok error }
    uplinks
  }
}
```

Name at most one input in `PayloadDecoderTestInput`: `frameHex`, `frameBase64`, `json`, or `webhookBody` with its `integrationToken`. Name none to run only the saved vectors. The function table the console shows is `payloadDecoderFunctions`.

## Worked examples {#examples}

### ChirpStack: a binary frame {#example-chirpstack}

A room sensor sends six bytes on port 2, and the ChirpStack device profile has no codec.

| Bytes | Meaning |
| --- | --- |
| 0 to 1 | Temperature in tenths of a degree, signed, big-endian |
| 2 | Humidity in half-percent steps |
| 3 to 4 | Battery in millivolts, big-endian |
| 5 | Bit 0 is the door contact |

Declare the metrics on the device profile: `temperature` (double), `humidity` (double), `battery` (integer) and `door` (boolean). Then set the decoder's input to **Binary frame** and add two sections: one with **fPort** 2 and the four fields, and one that **ignores** every other port. This is the document the tab saves:

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
    },
    { "name": "other port", "input": { "frameHex": "00", "fPort": 9 }, "expect": {} }
  ]
}
```

In the test panel, choose **Frame**, paste `00D75A0CE401`, set `fPort` to 2 and run: the readings are `temperature` 21.5, `humidity` 45, `battery` 3300 and `door` 1. Run the same frame with `fPort` 9 and the result is **Ignored**. Send only the first three bytes, `00D75A`, and the result is `eval_error` at `sections[0].outputs[2].expr`: `u16be: reads 2 byte(s) at offset 3 of a 3-byte frame (offset 3)`.

To rehearse the whole request rather than the bytes, choose **Webhook body**, pick the ChirpStack integration and paste the body ChirpStack sends.

### The Things Stack: reshaping a decoded object {#example-tts}

The payload formatter in The Things Stack already decodes the frame, but to `{"temp_f": 70.7, "bat": {"mv": 3300}, "rh": 45}`. Left alone, the platform stores `temp_f`, `bat.mv` and `rh`, none of them under the keys of your profile. A decoder over the **Decoded object** input renames them and converts the unit:

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "DECODED",
  "sections": [
    {
      "name": "uplink",
      "outputs": [
        { "key": "temperature", "expr": "(double(decoded.temp_f) - 32.0) / 1.8" },
        { "key": "battery", "expr": "int(decoded.bat.mv)" },
        { "key": "humidity", "when": "has(decoded.rh)", "expr": "double(decoded.rh)" }
      ]
    }
  ],
  "vectors": [
    {
      "name": "with humidity",
      "input": { "decoded": { "temp_f": 70.7, "bat": { "mv": 3300 }, "rh": 45 } },
      "expect": { "temperature": 21.5, "battery": 3300, "humidity": 45 }
    },
    {
      "name": "without humidity",
      "input": { "decoded": { "temp_f": 70.7, "bat": { "mv": 3300 } } },
      "expect": { "temperature": 21.5, "battery": 3300 }
    }
  ]
}
```

The `humidity` row has a condition so that an uplink without `rh` still decodes; without it, the missing key would fail the whole decode with `eval_error`. The estimated cost of this decoder is above 65,000, almost all of it the allowance for parsing a full 64 KiB object; see [Cost limits](#cost). In the test panel, choose **Webhook body**, pick the The Things Stack integration, paste the whole webhook body and set the message type to `uplink-message`.

### Cayenne LPP {#example-lpp}

A device that sends Cayenne LPP needs no field table: one **Map** row decodes the whole frame. The frame `01 67 00 F5 02 68 6C` holds two channels, a temperature (type `67`, in tenths of a degree) on channel 1 and a humidity (type `68`, in half-percent steps) on channel 2.

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "lpp",
      "outputs": [{ "map": "lpp.decode(frame)", "prefix": "lpp.", "maxKeys": 8 }]
    }
  ],
  "vectors": [
    {
      "name": "two channels",
      "input": { "frameHex": "016700F502686C" },
      "expect": { "lpp.temperature_1": 24.5, "lpp.humidity_2": 54 }
    }
  ]
}
```

`lpp.decode` names each value by its type and channel, so the keys are `lpp.temperature_1` and `lpp.humidity_2` with the prefix. Declare those as metrics. A channel you have not declared is listed as not stored, and a frame with more than eight values fails with `map_overflow`. When the LPP payload follows a header, slice it off first: `lpp.decode(slice(frame, 4, size(frame) - 4))`.

## What a decoder does not do {#limits}

A decoder is deliberately small, so that it runs on every uplink at ingest rates and cannot harm the platform.

- **No loops.** There is no `for`, no list comprehension and no recursion. The only macro is `has()`. Variable-length data is handled by an owned function such as `lpp.decode`, not by a loop you write.
- **No network, files, clock or randomness.** A decoder cannot call out, read anything but its input, or give a different answer for the same input.
- **No text processing.** There are no string functions and no regular expressions. Text can be decoded from hex or base64 and compared with `==`, and an output is always a number or a true/false value, never text.
- **No lists or computed keys.** A JSON array cannot be indexed, and a key is always a constant string. List literals exist only for `in`, with at most 16 values.
- **No other engine.** `WASM` is a reserved value that is refused, not an engine that runs.
- **It cannot set the time of a reading.** The message's own time stays authoritative, which is what keeps [duplicate detection](./inbound-integrations.md#duplicates) working.
- **It cannot write a calculated metric, or a `lorawan.` key.**

A function's behaviour never changes once it has shipped. A fix arrives under a new name in a new library version, and a decoder keeps the version it was written for in `envVersion`.

## The test rate limit {#rate-limit}

`testPayloadDecoder` compiles the decoder on every call, so it is limited to **10 requests a second per tenant, with a burst of 20, on each `device-management` replica**. The limit is per replica, not per instance: with several replicas the tenant's total can be higher. A request over the limit is refused with `RATE_LIMITED`; wait a moment and retry. The console shows "Too many tests too quickly". Saving, publishing and the function list are not limited this way.

## Related {#related}

- [LoRaWAN Uplink to Alarm](./lorawan-uplink-to-alarm.md): a decoder, an integration, a rule and an alarm, end to end, with the simulator as the network server.
- [Decoder function reference](../reference/decoder-functions.md): the library's functions with their costs, and the limits.
- [Inbound integrations](./inbound-integrations.md): the senders a decoder reads from.
- [Device Protocol Reference](../reference/device-protocol.md#raw-payload): the raw-payload topic and route, and what happens to each message.
- [Device types and profiles](../user-guide/device-types-and-profiles.md): metrics, drafts and publishing.
- [Transport capability matrix](../reference/transport-matrix.md#inbound-integrations): what each way in supports.
- [Releases and upgrades](../deployment/releases-and-upgrades.md#v120-payload-decoders): what changed in this release.
