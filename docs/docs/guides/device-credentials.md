---
sidebar_position: 5
title: Device Credentials
---

# Device Credentials

A device's **identity** (its stable token) is kept separate from its **credentials** — the material it presents to authenticate. A device can hold several credentials and rotate them without changing its identity.

:::note Status
Available. Credentials are managed from the device detail page's **Credentials** tab in the console, or over the device-management GraphQL API.
:::

## Credential types

| Type | The device presents | Stored secret |
| --- | --- | --- |
| `ACCESS_TOKEN` | a bearer token (the credential id) | none — possession of the id is the proof |
| `MQTT_BASIC` | a username (the credential id) + password | the password |
| `X509_CERTIFICATE` | a certificate subject/fingerprint (the credential id) | none — possession is proved out of band |

## Reading a credential requires `device:write` {#reading-a-credential}

Where a type carries a secret (the `MQTT_BASIC` password), that secret is **write-only**: it is submitted when the credential is registered and is **never returned on read**. The console never displays it — the password is entered in a masked field and cleared once the credential is created — and the API returns `null` for it thereafter.

That protects the `MQTT_BASIC` password, and nothing else. `ACCESS_TOKEN` and `X509_CERTIFICATE` store no secret to withhold: the **`credentialId` is itself the bearer** — the table above says so for the access token, and the per-event check accepts a certificate credential on its id alone as well — and `credentialId` is a plainly readable field. So reading a device's credentials hands you what you need to authenticate as that device, whatever the type.

Every query that returns a credential is therefore gated on **`device:write`**, not `device:read`: a read-only user cannot list a device's credentials, which is why the console's **Credentials** tab is not shown to one. The gate takes nothing from a `device:write` holder — they can already register a credential for any device in the tenant and impersonate it. What it stops is that capability reaching the read-only baseline every enabled tenant member receives.

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
- `required` — a valid credential must be presented or the event is rejected. **This is the default**.

When a credential authenticates, the resolved device is authoritative: a `device` token naming a *different* device is rejected, so one authenticated device cannot impersonate another.

## Two layers: the connection and the event

The credential above is the **per-event** check. In addition, MQTT/NATS **connections** are authenticated at the broker itself:

- The MQTT and NATS listeners are **TLS** — a device connects over TLS with the instance CA.
- A NATS **auth-callout** authenticates the connection and binds it to **that one device's** subjects — not its tenant's — so a device can publish its own events and read its own commands, and nothing else. For an `MQTT_BASIC` device, the connection presents MQTT username **`{tenant}:{credentialId}`** and the credential password — the same credential that authenticates its events — so a device that can't authenticate can't even connect.
- The connection must also present the MQTT **client id** `{instanceId}:{tenant}:{deviceToken}`, optionally followed by `:` and a suffix of the device's choosing (for example `{instanceId}:{tenant}:{deviceToken}:cmd`) so a second concurrent session — one connection publishing, another subscribed for commands — does not evict the first. Any id that does not start with the device's own `{instanceId}:{tenant}:{deviceToken}` is refused. The client id is the key the broker files a device's session under, so leaving it to the device would let one device take over another's session.

See [Connecting a Device](./connecting-a-device.md) for the transport details.

## Repeated failed connects are slowed down {#connect-backoff}

MQTT connects that present a username and password are slowed down after repeated failures,
so a password cannot be guessed at the rate the broker accepts connections.

- **What counts:** failed password connects in a row for one MQTT username
  (`{tenant}:{credentialId}`). An unknown username counts exactly like a real one, so the
  answer never tells anyone which usernames exist.
- **The schedule:** the first 10 failures in a row are not slowed down. After the 10th, the
  next attempt on that username waits 1 second, and each further failure doubles the wait, up
  to 30 seconds.
- **During the wait, even the correct password is refused.** The broker gives the same
  refusal as for a wrong password. The device connects normally once the wait is over.
- **A successful connect resets the count.**
- **Not slowed down:** access-token connects (a connect with no password), and credentials
  carried in event bodies. The per-event check gives the sender no answer, so it cannot be
  used to guess.

:::warning Anyone who knows a device's username can delay its reconnects

The count is kept per username, and the username is not secret. Someone who keeps sending
wrong passwords for a device's username can keep that device from reconnecting for as long as
they keep it up. Each wait is capped at 30 seconds, but they can start the next one as soon as
the last ends. A device that is already connected is not affected until it reconnects.
Don't publish device usernames, and give every `MQTT_BASIC` credential a strong password.

:::

The counts are kept in JetStream, so every device-management replica sees the same ones:

- If JetStream cannot be reached, **password connects are refused** until it can, because a
  connect that cannot be counted is not checked. This includes a brief window while the
  JetStream leader of the bucket that holds the counts changes, for example while a NATS node
  restarts. Access-token connects keep working.
- The bucket holding the counts is size-limited. Every connect, successful or not, keeps an
  entry for ten minutes. If the bucket fills, connects keep working but are **no longer slowed
  down**, and the `DeviceCredentialAttemptStoreFull` alert fires. That happens when connects
  are sent for a very large number of different usernames, or when a very large fleet
  reconnects at once. The bucket empties on its own ten minutes later. To give it more room,
  raise `instance.config.infrastructure.nats.kvStateMaxBytes`.

The `devicechain_devicemanagement_credential_checks_total` metric counts every password
connect check by `outcome`: `throttled` for a connect refused during a wait, `unavailable`
when the counts could not be reached, and `store_full` when the bucket was full.

## Register a credential (console)

1. Open the device's detail page and select the **Credentials** tab.
2. Choose the credential **type** and fill the fields for that type (generate or paste an access token; enter a username + password for MQTT-basic; enter a certificate id for X.509).
3. For `MQTT_BASIC`, record the password before you continue — the field is cleared on success and the password is never shown again. Then click **Add credential**.

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

For `MQTT_BASIC`, also pass `credentialValue: "<password>"` (write-only). Registering a credential and listing a device's credentials both require the `device:write` authority — see [above](#reading-a-credential).
