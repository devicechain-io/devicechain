---
sidebar_position: 4
title: Device Credentials
---

# Device Credentials

A device's **identity** (its stable token) is kept separate from its **credentials** — the material it presents to authenticate (ADR-014). A device can hold several credentials and rotate them without changing its identity.

:::note Status
Available. Credentials are managed from the device detail page's **Credentials** tab in the console, or over the device-management GraphQL API.
:::

## Credential types

| Type | The device presents | Stored secret |
| --- | --- | --- |
| `ACCESS_TOKEN` | a bearer token (the credential id) | none — possession of the id is the proof |
| `MQTT_BASIC` | a username (the credential id) + password | the password |
| `X509_CERTIFICATE` | a certificate subject/fingerprint (the credential id) | none — possession is proved out of band |

## Secrets are write-only

Where a type carries a secret (the `MQTT_BASIC` password), that secret is **write-only**: it is submitted when the credential is registered and is **never returned on read**. The console shows it once, at creation time, and the API returns `null` for it thereafter. A holder of `device:read` cannot exfiltrate secrets.

## How a device presents a credential

Credentials ride in the event body, on any transport (see [Connecting a Device](./connecting-a-device.md)):

```json
{
  "device": "sensor-001",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "eventType": "Measurement",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

`MQTT_BASIC` additionally carries `"credentialSecret": "<password>"`.

The platform resolves the credential to its owning device and verifies it — honoring **expiry** and **revocation by disabling** the credential. An instance's device-auth mode governs enforcement:

- `disabled` — the self-asserted `device` token is trusted (no credential needed).
- `optional` — a presented credential is authoritative; without one the device token is trusted.
- `required` — a valid credential must be presented or the event is rejected. **This is the default** (ADR-025).

When a credential authenticates, the resolved device is authoritative: a `device` token naming a *different* device is rejected, so one authenticated device cannot impersonate another.

## Two layers: the connection and the event

The credential above is the **per-event** check. In addition, MQTT/NATS **connections** are authenticated at the broker itself (ADR-025):

- The MQTT and NATS listeners are **TLS** — a device connects over TLS with the instance CA.
- A NATS **auth-callout** authenticates the connection and binds it to per-tenant subjects, so a device can only publish or subscribe within its own tenant. For an `MQTT_BASIC` device, the connection presents MQTT username **`{tenant}:{credentialId}`** and the credential password — the same credential that authenticates its events — so a device that can't authenticate can't even connect.

See [Connecting a Device](./connecting-a-device.md) for the transport details.

## Register a credential (console)

1. Open the device's detail page and select the **Credentials** tab.
2. Choose the credential **type** and fill the fields for that type (generate or paste an access token; enter a username + password for MQTT-basic; enter a certificate id for X.509).
3. Click **Add credential**. For a secret-bearing type, copy the secret now — it will not be shown again.

Delete a credential from its row; the device can no longer authenticate with it.

## Register a credential (GraphQL)

```graphql
mutation {
  createDeviceCredential(request: {
    token: "b2e1…",                 # a fresh unique credential token
    deviceToken: "sensor-001",
    credentialType: "ACCESS_TOKEN",
    credentialId: "5f989616-2a0d-4160-8ae1-da5fad2898b2",
    enabled: true
  }) { id token credentialType credentialId enabled }
}
```

For `MQTT_BASIC`, also pass `credentialValue: "<password>"` (write-only). Registering requires the `device:write` authority; listing a device's credentials requires `device:read`.
