---
sidebar_position: 5
title: Device Protocol Reference
---

# Device Protocol Reference {#device-protocol-reference}

This page is the reference for every message a device exchanges with DeviceChain over MQTT and HTTP: the event a device sends, the command it receives, and the response it sends back. It lists every field, what happens to a message the platform refuses, and where that refusal can be seen. For a walkthrough, start with [Connecting a Device](../guides/connecting-a-device.md).

LwM2M and Sparkplug B devices do not use these messages. Their protocol gateways translate to the platform's internal form; see [LwM2M](../concepts/lwm2m.md) and [Sparkplug B](../concepts/sparkplug.md).

## Machine-readable schemas {#schemas}

Every message on this page is published as a JSON Schema (draft 2020-12). Use them to validate firmware output, or to generate types:

| Schema | Describes |
| --- | --- |
| [`device-event.schema.json`](pathname:///schema/device/device-event.schema.json) | The [event envelope](#device-event) a device sends |
| [`measurement-payload.schema.json`](pathname:///schema/device/measurement-payload.schema.json) | The payload of a [`Measurement`](#measurement-payload) event |
| [`location-payload.schema.json`](pathname:///schema/device/location-payload.schema.json) | The payload of a [`Location`](#location-payload) event |
| [`alert-payload.schema.json`](pathname:///schema/device/alert-payload.schema.json) | The payload of an [`Alert`](#alert-payload) event |
| [`new-relationship-payload.schema.json`](pathname:///schema/device/new-relationship-payload.schema.json) | The payload of a [`NewRelationship`](#new-relationship-payload) event |
| [`command-delivery.schema.json`](pathname:///schema/device/command-delivery.schema.json) | The [command](#command-delivery) the platform delivers to a device |
| [`command-response.schema.json`](pathname:///schema/device/command-response.schema.json) | The [response](#command-response) a device sends back |

The same list, with absolute URLs, is under `deviceProtocol` in [`/schema/index.json`](pathname:///schema/index.json). The envelope schema references the payload schemas by relative URL, so a validator that loads it by URL resolves them on its own.

The schemas are committed beside the code that decodes these messages, and a test fails the platform's build if a field is renamed, added or removed on one side and not the other.

What a schema cannot say:

- **Unknown members are ignored, not refused.** A misspelled optional field (`altID` for `altId` is fine, `alt_id` is not) is silently dropped rather than rejected. A validator run with `additionalProperties: false` added locally catches that in testing.
- **"Required" describes what a device must send, not everything the platform refuses at the door.** Most required fields are enforced, as the [rejection tables](#rejections) list. These are not:
  - `device` may be missing when a credential authenticates the event; the event is attributed to the credential's device.
  - A `NewRelationship` event with no `payload`, or with `relationshipType`, `targetType` or `target` missing, is accepted (HTTP `202`): each missing value reads as an empty string, and the event then fails at resolution and is dead-lettered with reason `ApiCallFailed`.
  - In a command response, a missing `success` reads as `false` and settles the command as `FAILED`; a missing `commandToken` matches no command and is dead-lettered with reason `exhausted` after its retries.

## Versioning {#versioning}

The messages carry no version field. The contract is the one described by the schemas published with the documentation for your release, and a schema changes in the same release as the code that decodes its message. Before v1.0.0, a release may change the contract, so compare the schemas between the release you run and the one you are moving to.

## Topics and endpoints {#topics}

The platform broker is the MQTT server built into NATS. An MQTT topic and the NATS subject the platform reads are the same name, with `/` written as `.`:

| Message | Direction | MQTT topic | NATS subject |
| --- | --- | --- | --- |
| [Event](#device-event) | device → platform | `{instanceId}/{tenant}/devices/{deviceToken}/events` | `{instanceId}.{tenant}.devices.{deviceToken}.events` |
| [Command delivery](#command-delivery) | platform → device | `{instanceId}/{tenant}/device-commands/{deviceToken}` | `{instanceId}.{tenant}.device-commands.{deviceToken}` |
| [Command response](#command-response) | device → platform | `{instanceId}/{tenant}/command-responses/{deviceToken}` | `{instanceId}.{tenant}.command-responses.{deviceToken}` |
| [Configuration](../guides/connecting-a-device.md#reserved-device-topics) and [update](../guides/connecting-a-device.md#over-the-air-updates) messages | platform → device | `{instanceId}/{tenant}/device-desired/{deviceToken}` | `{instanceId}.{tenant}.device-desired.{deviceToken}` |
| Configuration and update reports | device → platform | `{instanceId}/{tenant}/device-reports/{deviceToken}` | `{instanceId}.{tenant}.device-reports.{deviceToken}` |

- A device is authorized to publish and subscribe only on the topics carrying **its own** device token. It cannot read another device's commands or publish as another device.
- **Devices connect over MQTT, not raw NATS.** A device credential authorizes an MQTT connection only: a plain NATS client presenting one is refused at connect. The subject column is what an operator sees in NATS tooling, not a second way in. Connection settings (the client id, the `{tenant}:{credentialId}` username, TLS) are in [Connection settings](../guides/connecting-a-device.md#connection-settings).
- Publish events at QoS 0, or at QoS 1 with an [`altId`](#device-event). QoS 2 publishes are refused by default and the broker closes the connection; see [Quality of service](../guides/connecting-a-device.md#quality-of-service).

A device with no credential yet reaches the platform over HTTPS to [register itself](#self-registration), at `POST /api/device-management/provisioning/{tenant}/register`. That is a separate endpoint from event ingest, served by `device-management`.

Over **HTTP**, a device can send events only. `POST` the same [event body](#device-event) to `/{instanceId}/{tenant}/events` on the `event-sources` service (port 8081). There is no HTTP downlink: an HTTP-only device cannot receive commands, configuration or update assignments.

### The gateway namespace {#gateway-namespace}

A gateway (the [edge agent](../deployment/edge-services.md#gateway-identity) is one) is a device that publishes for other devices. It does so in its own namespace:

| Message | Direction | MQTT topic |
| --- | --- | --- |
| [Event](#device-event) for a device behind the gateway | gateway → platform | `{instanceId}/{tenant}/gateways/{gatewayToken}/devices/{deviceToken}/events` |
| Notice that an event was refused | platform → gateway | `{instanceId}/{tenant}/gateways/{gatewayToken}/notices` |

- A gateway is authorized to publish only under its **own** token, with any child token after `devices/`, and to subscribe only to its own notices. It cannot publish in another gateway's or another tenant's namespace, or on a child's own topic. The grant is the same size however many devices the gateway fronts, and every device holds it, so assigning a first device to a gateway needs no reconnect.
- The platform accepts an event only if the device named in the topic exists, is **assigned to that gateway**, and the gateway is enabled. Otherwise it refuses the event with the reason `NotGatewayChild` ([below](#rejections-after-acceptance)) and sends the gateway a notice. The broker acknowledges the publish either way, because MQTT 3.1.1 cannot refuse one.
- Identity is the topic plus the assignment. The body carries none: an event on this namespace that carries `credentialType`, `credentialId` or `credentialSecret` is refused, and so is a body `device` that names another device. The event is attributed to the child, and the resolved event carries the gateway's token as well.
- A notice is a JSON object, `{"altId": "...", "device": "...", "reason": "not_gateway_child"}`. It carries no payload and is not stored, so a gateway that was offline when it was sent misses it. The record of a refusal is the `failed-events` stream.
- The grant also covers `responses` and `reports` under the namespace and subscribing to `commands` and `desired`, but nothing reads or sends them yet. Publish **events** only.

## Device event {#device-event}

One JSON object per MQTT message or HTTP request. One message is one event.

| Field | Type | Required | Meaning | Example |
| --- | --- | --- | --- | --- |
| `device` | string | yes¹ | The token of the device sending the event. On MQTT it must equal the `{deviceToken}` in the topic. When a credential authenticates the event, it must name that credential's device. | `"sensor-001"` |
| `eventType` | string | yes | `Measurement`, `Location`, `Alert` or `NewRelationship`, case-sensitive. Selects the payload shape. | `"Measurement"` |
| `payload` | object | yes | The event's content. Its shape depends on `eventType`; see [Payloads](#payloads). | `{"entries":[…]}` |
| `occurredTime` | string, RFC 3339 | no | When the event happened. Omitted, the event is dated when the platform received the message. | `"2026-08-09T12:00:00.125Z"` |
| `altId` | string | no | A device-chosen idempotency key: a redelivered event carrying the same `altId` **and** envelope `occurredTime` is skipped. Currently matched per tenant, not per device, which is a known limitation; see [below](#altid). | `"sensor-001-4417"` |
| `relationship` | string | no | Accepted, carried through the pipeline, and **not used**. The platform records every one of the device's tracked relationships on the event, whatever this says. Do not rely on it. | — |
| `credentialType` | string | HTTP: see below. MQTT: never | `ACCESS_TOKEN` or `MQTT_BASIC`. With `credentialId`, authenticates the event. | `"ACCESS_TOKEN"` |
| `credentialId` | string | HTTP: see below. MQTT: never | For `ACCESS_TOKEN`, the token itself. For `MQTT_BASIC`, the username, without the `{tenant}:` prefix the MQTT connection uses. | `"5f98…98b2"` |
| `credentialSecret` | string | HTTP, `MQTT_BASIC` only. MQTT: never | The `MQTT_BASIC` password. | |

¹ Send `device` on every event. Strictly, the platform tolerates its absence when a credential authenticates the event, and then attributes the event to the credential's device.

**The credential, over MQTT.** Do not send one. On the platform's MQTT broker the device is authenticated when it connects, and the topic names the device, so `credentialType`, `credentialId` and `credentialSecret` must be absent: an event that carries any of them is refused (see the [rejection table](#rejections-at-the-door)). Treat the credential of a device that did so as exposed and rotate it. A broker you operate yourself and connect as an external MQTT source does not authenticate devices for the platform, so events from it still carry the credential, as over HTTP.

**The credential, over HTTP.** The default device-authentication mode is `required`: an event with no credential is refused (see the [rejection table](#rejections)). Omit the credential only on an instance configured as `optional` or `disabled`. A credential is read only when both `credentialType` and a non-empty `credentialId` are present. See [Device credentials](../guides/device-credentials.md).

**Timestamps.** Every `occurredTime`, on the envelope or on an entry, is RFC 3339. The platform refuses one that is not, one that is `0001-01-01T00:00:00Z` (reserved to mean "no time was reported"), and one more than **366 days** before the platform received the message. A time far ahead of the platform's clock is stored at a ceiling rather than refused; see [Clocks that run ahead](../guides/connecting-a-device.md#clocks-that-run-ahead).

### `altId` and duplicates {#altid}

At-least-once delivery (MQTT QoS 1, an HTTP retry after a `503`) can deliver one event twice. Without an `altId`, both copies are stored. With one, the second is skipped:

- The match is on `altId` **and** the envelope `occurredTime` together. An entry's `occurredTime` does not count.
- Send an `occurredTime` on the envelope. Without one, each copy is dated on arrival, the two times differ, and both are stored.
- The match is currently made per tenant rather than per device; see the limitation below.
- A second event with the same `altId` and `occurredTime` is skipped even if its content is different.

:::caution Known limitation
Duplicates are currently detected per **tenant** on (`altId`, `occurredTime`), not per device, so two devices that send the same `altId` for the same instant collide, and one of the events is skipped. This is a defect, and a fix is under way. Until it ships, make the value unique across the fleet, for example by prefixing the device token.
:::

### Payloads {#payloads}

`Measurement`, `Location` and `Alert` payloads wrap their content in an `entries` array. One entry is one reading at one instant, and each entry may carry its own `occurredTime`; an entry without one takes the envelope's. A payload with no entries, or with its content placed directly under `payload`, is refused.

One event carries **at most 256 readings**, on both transports. A reading is one measurement key, or one location or alert entry. A message over the limit is refused whole, never trimmed.

#### Measurement {#measurement-payload}

| Field | Type | Required | Meaning | Example |
| --- | --- | --- | --- | --- |
| `entries` | array | yes | The samples. At least one. | |
| `entries[].measurements` | object of string → **string** | yes | Metric name to value. Every value is a JSON **string**: `"21.5"`, not `21.5`. A bare number fails the whole message. At least one metric. | `{"temperature":"21.5"}` |
| `entries[].occurredTime` | string, RFC 3339 | no | This sample's instant. | |

#### Location {#location-payload}

Every location field is a JSON **string**, including the numeric ones. Each must parse as a finite number inside its range.

| Field | Type | Required | Meaning | Range |
| --- | --- | --- | --- | --- |
| `entries` | array | yes | The fixes. At least one. | |
| `entries[].latitude` | string | yes | WGS84 decimal degrees | −90 to 90 |
| `entries[].longitude` | string | yes | WGS84 decimal degrees | −180 to 180 |
| `entries[].elevation` | string | no | Metres above the WGS84 **ellipsoid**, not above mean sea level | magnitude ≤ 99999999 |
| `entries[].accuracy` | string | no | Horizontal accuracy, metres | 0 to 99999999 |
| `entries[].speed` | string | no | Metres per second | 0 to 99999999 |
| `entries[].heading` | string | no | Degrees clockwise from true north | 0 up to but not including 360; 359.99995 and above is refused |
| `entries[].occurredTime` | string, RFC 3339 | no | This fix's instant | |

#### Alert {#alert-payload}

| Field | Type | Required | Meaning | Example |
| --- | --- | --- | --- | --- |
| `entries` | array | yes | The alerts. At least one. | |
| `entries[].type` | string | yes | The classifier notification policies, rules and console filters route on. Empty is refused. | `"overheat"` |
| `entries[].level` | **integer** | no | Severity, a bare JSON integer from 0 to 2147483647. A quoted level fails the whole message. Omitted, it is 0. | `5` |
| `entries[].message` | string | no | Human-readable text. | `"coolant over limit"` |
| `entries[].source` | string | no | What on the device raised it. | `"ecu"` |
| `entries[].occurredTime` | string, RFC 3339 | no | This alert's instant. | |

#### NewRelationship {#new-relationship-payload}

Creates one relationship from the sending device to another entity in the same tenant. This payload has no `entries` array, and its three keys are read by their exact names, so their case matters. A missing key is not refused when the message is decoded: it reads as an empty string, and the event then fails at resolution like the cases below.

| Field | Type | Required | Meaning | Example |
| --- | --- | --- | --- | --- |
| `relationshipType` | string | yes | The token of the relationship type: one defined in the tenant, or one of the platform's reserved types, which are created on first use. | `"member"` |
| `targetType` | string | yes | `device`, `asset`, `area`, `customer` or `group`. | `"group"` |
| `target` | string | yes | The token of the target entity. | `"building-7-sensors"` |

A relationship the platform cannot create (an unknown target type, a target that does not exist, an unknown relationship type) is not created. Like any event that cannot be resolved, it is retried and then dead-lettered.

## Command delivery {#command-delivery}

What a device receives on its `device-commands` topic, once for each dispatch of a command. Delivery is **live-only**: a device that is not connected and subscribed when the command is published does not receive it, and the platform is not told either way. See [Receiving commands](../guides/connecting-a-device.md#receiving-commands).

| Field | Type | Always present | Meaning | Example |
| --- | --- | --- | --- | --- |
| `token` | string | yes | The **command's** token. It identifies the command, not the device. Send it back as `commandToken`. | `"6f1c0f8e-…"` |
| `deviceToken` | string | yes | The device the command is addressed to: always the device whose topic it arrived on. | `"sensor-001"` |
| `name` | string | yes | The command key. When the device's profile declares a command vocabulary, one of its published commands. | `"reboot"` |
| `dispatchNonce` | string | yes | Names this dispatch of the command. Opaque: do not parse it. Echo it in the response. If the same command arrives again, answer with the nonce from the latest delivery. | `"0f6f4a2c-…"` |
| `payload` | any JSON value | no | The command's parameters, exactly as issued. Already validated against the command's parameter schema when the profile declares one. Absent when the command was issued without parameters. | `{"delaySeconds":5}` |

## Command response {#command-response}

What a device publishes on its `command-responses` topic to settle a command. The platform takes the responding device from the **topic**, never from the body.

| Field | Type | Required | Meaning | Example |
| --- | --- | --- | --- | --- |
| `commandToken` | string | yes | The `token` of the delivery being answered: the command's token, not the device's. | `"6f1c0f8e-…"` |
| `dispatchNonce` | string | yes | The `dispatchNonce` of the delivery being answered. | `"0f6f4a2c-…"` |
| `success` | boolean | yes | `true` settles the command as `SUCCESSFUL`, `false` as `FAILED`. Omitted, it reads as `false`. | `true` |
| `payload` | any JSON value | no | The command's result data, stored with the command and returned by the API. A string is stored as its text; an object, array, number or boolean is stored as that JSON value, exactly as sent, so a device can return structured data directly. Omitted or `null`, the command has no response payload. | `"rebooting in 5s"` or `{"level":3}` |
| `error` | string | no | Why the command failed. Stored only when `success` is `false`; ignored when it is `true`. | `"actuator jammed"` |
| `reach` | string | no | For a gateway or protocol adapter that answers on a device's behalf; a device reporting its own outcome omits it. Read only when `success` is `false`. `"UNANSWERED"`: the command was put on the wire and nothing came back, so it settles as `TIMEOUT`. `"UNSENT"`: the adapter refused it before it reached the device, so it settles as `FAILED` and is recorded as never delivered. Omitted, a failure is the device's own answer. An unrecognised value is treated as `"UNANSWERED"`. | `"UNANSWERED"` |

```json
{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":false,"error":"actuator jammed"}
```

## Self-registration {#self-registration}

A device that has no credential yet can register itself, over HTTPS, with a fleet-wide **provision key and secret** from a [provisioning profile](../guides/device-self-registration.md). It makes up its own credential, sends it once, and the platform answers with the device's token and MQTT client id. The platform keeps only a digest of the credential, and no response contains a secret.

```text
POST /api/device-management/provisioning/{tenant}/register
```

- **HTTPS only.** The request carries the provision secret in its body. There is no MQTT registration: a device cannot register by connecting.
- **It needs the instance's API ingress**, which routes `/api/<area>` to the services and strips the prefix. With `ingress.enabled: false` the route has no public path. [`dcctl device register`](./dcctl.md#dcctl-device-register) takes the same `--server` host as the other commands.
- **There is no `Authorization` header.** The provision key and secret in the body are the authentication.

### Request {#self-registration-request}

`Content-Type: application/json`, at most **8 KiB**. A field the schema does not define is refused, so a misspelled field fails instead of registering the device without it.

```json
{
  "provisionKey": "acme-sensors-2026",
  "provisionSecret": "<shown once when the profile was created or rotated>",
  "externalId": "SN-000123",
  "credentialSecret": "<the device's own credential, 22 to 256 base64url characters>",
  "name": "optional",
  "metadata": { "optional": "object" }
}
```

| Field | Required | Rules |
| --- | :---: | --- |
| `provisionKey`, `provisionSecret` | yes | 1 to 256 characters each. The secret is the one the server generated for the profile. |
| `externalId` | yes | The device's serial number or other id, 1 to 256 characters. It is how the platform finds the device. |
| `credentialSecret` | yes | The device's own credential material, which becomes its access token. **base64url** (letters, digits, `-` and `_`, no padding), **22 to 256 characters**. Generate 32 random bytes and encode them (43 characters). |
| `name` | no | At most 128 characters. Used only when this request creates the device. |
| `metadata` | no | A JSON object. Used only when this request creates the device. |

**Generate the credential on the device and store it before you send the request.** The platform cannot give it back. A request whose answer was lost is then safe to repeat with the same material: it is recognised as a replay and answered `200`, instead of being refused as a second device.

### Response {#self-registration-response}

`201` when the request created the device, `200` when it adopted an existing one or recognised a replay. The body is the same in all three cases and holds nothing secret:

```json
{
  "outcome": "CREATED",
  "tenant": "acme",
  "deviceToken": "pv-sn-000123-1a2b3c4d",
  "externalId": "SN-000123",
  "credentialType": "ACCESS_TOKEN",
  "mqtt": { "clientId": "<instanceId>:acme:pv-sn-000123-1a2b3c4d" }
}
```

| `outcome` | Status | Meaning |
| --- | :---: | --- |
| `CREATED` | `201` | The device did not exist. The profile (strategy `ALLOW_NEW`) created it, of the profile's device type and earmarked for the profile, with the credential. The device token is derived from the external id and starts with `pv-`. |
| `ADOPTED` | `200` | The device already existed, was earmarked for this profile and held no credential. It was given this one. |
| `REPLAYED` | `200` | The device already holds this profile's credential and the request presented the same material. Nothing was written. |

Connect with the client id from the response and the MQTT username `{tenant}:{credentialSecret}` with no password, as in [Connection settings](../guides/connecting-a-device.md#connection-settings). The response does not repeat the credential.

### What decides the answer {#self-registration-decision}

Every refusal is decided before anything is written. A device the platform already knows is judged by these steps in order, and the first that matches is the answer:

| Step | Condition | Answer |
| :---: | --- | --- |
| 1 | The device is disabled. | `403 DEVICE_DISABLED` |
| 2 | Its device type is not the profile's. | `409 DEVICE_TYPE_MISMATCH` |
| 3 | It is not earmarked for this profile. | `409 DEVICE_NOT_EARMARKED` |
| 4 | This profile's credential on it was disabled or has expired. | `409 ALREADY_PROVISIONED`, with the message "the device's credential was revoked; an operator must reset its provisioning" |
| 5 | It holds this profile's live credential and the material matches. | `200 REPLAYED` |
| 6 | It holds this profile's live credential and the material differs, or it holds any other credential. | `409 ALREADY_PROVISIONED` |
| 7 | It holds no credential and the profile has registered as many devices as its `maxDevices`. | `422 LIMIT_EXCEEDED` |
| 8 | Otherwise. | `200 ADOPTED` |

A device the platform does not know (no live device has this `externalId`) is created under the strategy `ALLOW_NEW` (`422 LIMIT_EXCEEDED` at the cap), and refused with `404 DEVICE_NOT_REGISTERED` under `CHECK_PRE_PROVISIONED`. Neither strategy ever adopts a device that was not earmarked for the profile: an operator-made device is taken over only by a profile somebody pointed at it, which is what [earmarking](../guides/device-self-registration.md#earmarking) is.

### Errors {#self-registration-errors}

An error answers `{ "code": "...", "message": "..." }`. The set of codes is closed. A `429`, and a `503` for want of capacity, also carry `Retry-After`, in seconds. A message never contains a secret and never says which part of the authentication failed.

| Status | `code` | When |
| :---: | --- | --- |
| `400` | `INVALID_VALUE` | The tenant in the path is not a valid token; the body is not one JSON object or carries a field that is not defined; a field is out of range (the message names the field, never its value). |
| `401` | `PROVISIONING_FAILED` | The provision key or secret is not valid. **This is one answer for every cause**: an unknown tenant, an unknown key, a wrong secret, a profile that is disabled or expired, a profile whose secret the server never generated (see [rotate it](../guides/device-self-registration.md#rotate-required)) and a tenant that is being deleted. |
| `403` | `DEVICE_DISABLED` | Step 1 above. |
| `404` | `DEVICE_NOT_REGISTERED` | An unknown device under `CHECK_PRE_PROVISIONED`. |
| `408` | `INVALID_VALUE` | The body did not arrive within the read deadline (10 seconds by default). The connection is closed. |
| `409` | `DEVICE_NOT_EARMARKED`, `DEVICE_TYPE_MISMATCH`, `ALREADY_PROVISIONED` | Steps 3, 2, and 4 or 6 above. |
| `409` | `DEVICE_TOKEN_COLLISION` | The device token derived from this external id is held by a different device. An operator must resolve it. |
| `413` | `INVALID_VALUE` | The body is over 8 KiB. |
| `422` | `LIMIT_EXCEEDED` | The profile has registered as many devices as its `maxDevices`. |
| `429` | `RATE_LIMITED` | A limit is used up: the source address's allowance or the tenant's registrations per second. `Retry-After` says how long to wait. |
| `500` | `PROVISIONING_MISCONFIGURED` | The profile cannot register anything: its device type was deleted, or its strategy is not one the platform knows. An operator must fix the profile. |
| `500` | `PROVISIONING_UNAVAILABLE` | An internal failure. The registration was not completed; retry later. |
| `501` | `CREDENTIAL_TYPE_UNSUPPORTED` | The profile mints a credential type this build cannot. Terminal; it never falls back to another type. |
| `503` | `PROVISIONING_UNAVAILABLE` | The replica is busy (with `Retry-After`), or the instance has no keys to check a provision secret with (without). |

### The limits {#self-registration-limits}

The limits keep someone who has not authenticated from making the service spend more than the request is worth, and keep one factory run from drowning the rest of the tenant. In the order they apply:

1. **A body deadline.** The body must arrive within 10 seconds (`provisioning.readDeadline`, at most 1 minute). A request still sending its body holds nothing but its own connection.
2. **A size cap and parse.** 8 KiB, and the body is read and validated before the request counts against anything below.
3. **A bound on concurrent requests**: 64 per replica (`provisioning.maxInFlight`), then `503` with `Retry-After`.
4. **A per-source allowance**: a burst of 60 requests, then one a second, per client address and per replica (`provisioning.sourceBurst`, `provisioning.sourceEvery`). IPv6 clients are counted by /64. Nothing exempts a source that has succeeded before. The address is the real client's only when the services are told which proxies to believe: see [Client addresses and trusted proxies](../deployment/bootstrap.md#trusted-proxies).
5. **A per-tenant ceiling**, charged only once the provision key and secret have been accepted: by default a burst of 500 and 20 registrations a second, resolved through the tenant's [tier and overrides](../concepts/tenant-tiers.md) like the other [governance](../concepts/governance.md) ceilings. A missing or unreachable setting means the platform default, never unlimited.
6. **A cap per profile**: `maxDevices`, set by the operator. Without one, the profile is uncapped.

There is no limit on guessing the provision secret, because it cannot be guessed: it is 256 random bits, compared in constant time, and an unknown key costs the same work as a wrong secret. A sustained flood of `401`s raises an [alert](../deployment/observability.md#self-registration-alerts) instead.

### The retry contract {#self-registration-retry}

Persist the credential before the first send, and present **the same credential on every retry**.

| Answer | What to do |
| --- | --- |
| `429`, or `503` with `Retry-After` | Wait `Retry-After` plus a random extra of 0 to 100% of it, then retry. |
| A network error, a timeout, `408`, `502`, `504`, or `500` with `PROVISIONING_UNAVAILABLE` | Back off exponentially from 1 second to 5 minutes, with full jitter, then retry. |
| `400`, `413` | Stop. The request is wrong; this is a firmware defect. |
| `401`, `403`, `404`, `409`, `422`, `501`, `500` with `PROVISIONING_MISCONFIGURED` | Stop and report. Retrying cannot change the answer; an operator has to. |

The jitter matters: a fleet that powers on together and retries in step arrives together again. [`dcctl device register`](./dcctl.md#dcctl-device-register) implements this contract and is the reference for firmware authors.

## Rejections and errors {#rejections}

What happens to a message the platform does not accept depends on where it is refused, and on the transport. HTTP answers the request, so a device learns of a refusal at the door. MQTT acknowledges a publish (`PUBACK`) as soon as the broker has captured it, **before** it is decoded, so an MQTT device is never told: every refusal after the broker is visible only to an operator. With the default setting, a QoS 1 publish acknowledged while the capture stream changes leader can also be lost, both while the capture stream changes leader and, under sustained load, when ingest falls behind and the stream fills; see [Quality of service](../guides/connecting-a-device.md#quality-of-service).

### At the door {#rejections-at-the-door}

Checked by `event-sources` when the message is received and decoded.

| Condition | HTTP | MQTT | Where an operator sees it |
| --- | --- | --- | --- |
| Instance id in the path or topic is not this instance's | `404` | The broker refuses the publish: the device is not authorized for that topic | — |
| Tenant in the path is not a valid token | `400` | The broker refuses the publish | — |
| Tenant is over its ingest ceiling, by messages or by readings | `429` with `Retry-After: 1` | Acknowledged, then **dropped** | `total_msg_rate_limited`, `total_msg_reading_limited` |
| Body cannot be read, or is over 1 MiB | `400` | — | — |
| Body does not decode: not JSON, an unknown or platform-only `eventType` (`StateChange`, `CommandInvocation`, `CommandResponse`), a value of the wrong JSON type, no entries, an empty entry, a missing or out-of-range location field, an alert with no `type` or with a `level` over 2147483647 | `400`, with the reason in the body | Acknowledged, then routed to the `failed-decode` stream | `total_msg_failed_decode` |
| A timestamp that is not RFC 3339, is `0001-01-01T00:00:00Z`, or is more than 366 days old | `400`, naming the field | Routed to `failed-decode` | `total_msg_failed_decode`, `total_msg_invalid_event_time` |
| More than 256 readings | `400`, naming the count and the limit | Routed to `failed-decode` | `total_msg_failed_decode`, `total_msg_too_many_readings` |
| The body's `device` is not the device in the topic | — | Routed to `failed-decode` | `total_msg_failed_decode` |
| The body carries `credentialType`, `credentialId` or `credentialSecret`, on the platform's MQTT broker | — | Routed to `failed-decode` (redacted) | `total_msg_failed_decode`, `total_msg_credential_in_mqtt_body` |
| The platform is applying [backpressure](../deployment/observability.md#ingest-backpressure) | `503` **with** `Retry-After`: not stored, send it again | Not refused: it waits in the capture stream; the oldest captured messages are discarded only if that stream fills | |
| The event could not be handed to the pipeline | `503` **without** `Retry-After`: it may have been stored, so a resend stores it twice unless it carries an [`altId`](#altid) | — | |
| Accepted | `202` | `PUBACK` (QoS 1) | |

A `400` is terminal: the same request gets the same answer. A refusal at the door refuses the **whole message**, including every other entry in it.

### After acceptance {#rejections-after-acceptance}

An accepted event (`202`, or captured by the broker) is then resolved by `device-management`, which authenticates the device and attaches the event to it. These refusals are the same on both transports, and no device is told of them. The event is retried, then recorded on the `failed-events` stream with one of these reasons:

| Reason | Cause |
| --- | --- |
| `Unauthenticated` | No credential while device authentication is `required`; a credential that does not authenticate; or a body `device` that is not the credential's device. |
| `DeviceNotFound` | No credential was used, and the `device` token is not registered in the tenant. |
| `Invalid` | The event is older than 366 days, or could not be interpreted for its type. |
| `ApiCallFailed` | A lookup or write the resolution needed failed, including a [`NewRelationship`](#new-relationship-payload) the platform could not create. |
| `NotGatewayChild` | A gateway published for a device that is not assigned to it, or that does not exist, or the gateway is disabled. Not retried: it is recorded on the first delivery. |

A refused event is retried until its fifth delivery, so a device registered or a database restored in the meantime still gets its event stored. The exception is an event too old to store, which is recorded on its first delivery, since no retry can change its age.

### Command responses {#rejections-command-responses}

A command response is never answered, on any transport. What happens to it:

| Response | Outcome |
| --- | --- |
| Matches a command the device owns, with the `dispatchNonce` of its current dispatch | The command is settled `SUCCESSFUL` or `FAILED`. |
| Answers a command that is already finished | Ignored; the command keeps its outcome. |
| Not decodable: not JSON, or a field of the wrong type, such as a quoted `success` | Not settled; recorded on the `dead-letters` stream with reason `unprocessable`, naming the responding device. The command stays `SENT` until it times out, unless the device answers again correctly. |
| No `dispatchNonce` | Not settled; recorded on the `dead-letters` stream with reason `unprocessable`. |
| A `dispatchNonce` from a dispatch the command has moved off | Not settled; dead-lettered with reason `unprocessable`. |
| A `commandToken` that matches no command (most often the device's own token sent by mistake) | Retried until the fifth delivery, then dead-lettered with reason `exhausted`. |
| A command that belongs to a different device | Refused: logged and counted, not recorded against the command and not dead-lettered. |

A command nobody answers stays `SENT` until its expiry turns it into `TIMEOUT`. See [Why the nonce is required](../guides/connecting-a-device.md#why-the-nonce-is-required).
