---
title: Inbound integrations
sidebar_position: 12.6
---

import GuideShot from '@site/src/components/GuideShot';

# Inbound integrations

Some devices never connect to DeviceChain themselves. A LoRaWAN sensor talks to a gateway, and a
**network server** such as ChirpStack or The Things Stack forwards what it decoded over HTTPS. An
**inbound integration** is the receiving end of that forward. It gives the sender a URL and a bearer
token, finds the device each message is about, and stores the readings as ordinary measurements.

This chapter is for **tenant administrators** and shows the console side: creating an integration,
connecting the sender, giving devices the identifiers the sender uses, and seeing what arrived and
could not be used. The full contract (the URL, the limits, how duplicates and retries are handled, and
what the sender is told) is in the [Inbound integrations guide](/guides/inbound-integrations).

**Integrations** is under **Connect** in the sidebar. The page needs the `device:read` permission to
open and `device:write` to change anything. A member limited to some of the tenant's customers or
groups does not see it. How many integrations a tenant may hold is a setting of its tier; creating one
past the ceiling is refused.

## The integration list {#the-list}

The list shows each integration's name, token and sender type, the device types it may reach, when a
request for it last failed authentication, and its status. Three badges call for attention:

- **Disabled**: the integration is not accepting data.
- **Rejected token**: a request for this integration has failed authentication. Check the token the
  sender presents.
- **N undecoded**: uplinks arrived with nothing to read readings from. See
  [Uplinks with nothing to read](#undecoded).

## Creating an integration {#creating}

Before you start, have a device type for the devices behind the sender, ideally with a
[metric](/user-guide/device-types-and-profiles#metrics) declared for each decoded value you want a unit
for. Then choose **New integration** and fill in:

- **Sender type**: **ChirpStack v4**, **The Things Stack v3**, **Generic JSON** or **Generic binary**.
  It fixes how the body is read and **cannot be changed** afterwards.
- **Token**: part of the URL the sender posts to. It cannot be changed afterwards. The bearer token the
  sender authenticates with is generated for you.
- **Name** and **Description**.
- **Device types**: only devices of these types are reached through the integration, whatever the
  sender claims. Choose at least one.
- **Device identifier** (network servers): how the sender names a device, by its **DevEUI**, or, for The
  Things Stack, by its **Device id** in the network server.
- **Store link metrics**: store signal strength, signal-to-noise ratio and gateway count beside each
  decoded uplink.
- **Accepting data**: turn it off to stop resolving devices through this integration at once.

For a generic sender, a **Where the data is** section appears:

- **Identifier scheme name**: the name the sender's identifiers are stored under on devices, as the
  alias `custom.<name>`. Use lower-case letters, digits, hyphen and underscore.
- **Identifier comes from**: **Header**, **Body field** or **Query parameter**. A binary body has no
  fields, so for **Generic binary** it is a header or a query parameter.
- **Identifier location**: the header name (such as `X-Device-Id`), a JSON pointer to the field (such
  as `/device/id`) or the query parameter name.
- **Items array** (optional): a JSON pointer to an array in the body; each element is one uplink.
  Leave it empty when the body is a single uplink.
- **Sender time** (optional): a JSON pointer to the time the sender recorded. Without it the time of
  receipt is used, and a retried request is stored twice.
- **Raw frame** and **Frame encoding** (optional, JSON only): where an encoded raw frame is, kept for
  a decoder. The frame is not decoded yet.

Choose **Create integration**. The next dialog, **Integration created**, shows the **bearer token**.
**It is shown once.** It cannot be read again; if it is lost, rotate it. Enter it in the sender now.

## Connecting the sender {#connecting}

Select an integration to open its page. **Connect the sender** lists what to enter in the sender, for
that sender type, with a copy button on each value: the endpoint URL, the header name and the header
value. The bearer token itself is not shown again here.

<GuideShot id="integration-detail" alt="The page of the integration ChirpStack EU868, token chirpstack-eu868, with Rotate token and Delete buttons at the top right. A Traffic panel shows 0 uplinks with nothing to read and the date the token was generated. Below it, a Connect the sender panel lists the Payload encoding JSON, the Event endpoint URL, the header name Authorization and the header value 'Bearer <bearer-token>', each with a copy button." />

### ChirpStack v4 {#chirpstack}

In ChirpStack, open the application, add an **HTTP** integration, and fill it in from the page:

- Set **Payload encoding** to **JSON**. A Protobuf integration is refused with a message that names the
  setting.
- Enter the **Event endpoint URL** from the page. ChirpStack adds the event type to the URL itself;
  only uplinks are read, and other events are acknowledged and ignored.
- Add the header `Authorization` with the value `Bearer <bearer token>`.
- Set a **codec** on the ChirpStack device profile. Without one an uplink carries no decoded values and
  is counted as undecoded.

### The Things Stack v3 {#the-things-stack}

In The Things Stack, open the application, add a **Custom webhook** with the JSON format, and enter
the **Base URL** and the `Authorization` header from the page. Enable the uplink message on one of the
paths the page lists. Messages on any other path are acknowledged and ignored. Set a **payload
formatter** on the application or device, or the uplink is counted as undecoded.

### Generic senders {#generic}

For **Generic JSON** and **Generic binary** the page gives an example request showing where the
identifier goes. A generic sender must send each uplink as an HTTP `POST` with the bearer token in the
`Authorization` header. The exact request shapes are in the guide's
[Generic JSON](/guides/inbound-integrations#generic-json) and
[Generic binary](/guides/inbound-integrations#generic-binary) sections. Generic binary only counts
uplinks and keeps a sample of the first bytes: it is for seeing what a sender sends, not for storing
readings.

### The sender's certificate {#sender-certificate}

A hosted network server will not trust an instance that serves a self-signed certificate. If your
instance does, ask whoever runs it for a certificate from a public CA before you connect a hosted
sender.

## Giving devices the sender's identifiers {#aliases}

A message names a device the way the sender knows it, not the way DeviceChain does. A device **alias**
records that name on the device. Open the device form and use the **Aliases** field: choose **Add
alias**, pick **LoRaWAN DevEUI**, **Network server device id** or **Custom scheme**, and type the
value. A device holds up to eight aliases, one value per scheme, and a scheme and value can belong to
only one device in the tenant. A DevEUI is normalized when it is stored, so upper or lower case is fine.

The integration's **Device identifier** decides which scheme it looks up: a DevEUI reads the
`lorawan.deveui` alias, a network server device id reads `tts.device-id`, and a generic integration
reads `custom.<name>`. A device that carries only a different scheme is not found. Aliases are
described in full in [Identify devices with aliases](/guides/inbound-integrations#aliases).

## Unregistered devices seen {#unregistered}

You do not have to create the devices first. On the integration page, **Unregistered devices seen**
lists identifiers the integration received that no device in its scope carries, with their
scheme, the identifier, how many times it was seen and when it was last seen. Beside each one, **Create
a device with this alias** opens the device form with the alias filled in; once the device exists, its
data starts being stored.

The list is observation only. **Clear list** forgets everything on it, and an identifier the sender is
still sending is listed again.

## Traffic and problems {#traffic}

The **Traffic** panel on the integration page shows what the integration has received and could not
use: how many uplinks had nothing to read, when the last one arrived, a sample of its last frame in
hexadecimal, when the last request was rejected and when the token was generated.

### Uplinks with nothing to read {#undecoded}

An uplink counts as undecoded when it carries no decoded object with a numeric value. For a network
server, the usual cause is a missing codec or payload formatter: set one on the device profile there,
and use JSON encoding. The raw frame is not decoded by DeviceChain.

### A rejected token {#rejected-token}

**Rejected token** means a request failed authentication. The sender may hold an old token, for example
after a rotation. Check the header value, or rotate the token and enter the new one.

## Rotating the token and deleting {#rotating}

- **Rotate token** replaces the bearer token. The old token stops working at once, with no overlap, so
  update the sender straight after. The new one is shown once.
- **Delete** removes the integration. The sender is refused at once. The aliases on devices are kept.

## Related

- [Inbound integrations guide](/guides/inbound-integrations): the URL, limits, duplicates and retries,
  and troubleshooting.
- [Devices](/user-guide/devices): the device form and its aliases.
- [Device types and profiles](/user-guide/device-types-and-profiles): declaring the metrics a decoded
  value is stored under.
- [Provisioning and trust anchors](/user-guide/provisioning-and-trust-anchors): devices that connect
  themselves.
