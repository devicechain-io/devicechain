---
sidebar_position: 5
title: Device Credentials
---

# Device Credentials

A device's **identity** is its stable token. Its **credentials** are the material it presents to authenticate, and they are kept separate from that identity. A device can hold several credentials and rotate them without changing its identity.

:::note Status
Available. You manage credentials from the **Credentials** tab of the device detail page in the console, or over the device-management GraphQL API.
:::

## Credential types

| Type | The device presents | Stored secret |
| --- | --- | --- |
| `ACCESS_TOKEN` | a bearer token (the credential id) | none — possession of the id is the proof |
| `MQTT_BASIC` | a username (the credential id) + password | the password |
| `X509_CERTIFICATE` | a certificate subject/fingerprint (the credential id) | none — possession is proved out of band |

## Reading a credential requires `device:write` {#reading-a-credential}

A secret, where a type has one, is **write-only**. Only `MQTT_BASIC` has one: its password. You submit the password when you register the credential, and it is never returned on read. The console never displays it: you enter it in a masked field, and the field is cleared once the credential is created. From then on the API returns `null` for it.

That protects the `MQTT_BASIC` password and nothing else. `ACCESS_TOKEN` and `X509_CERTIFICATE` store no secret to withhold, because the **`credentialId` is itself the bearer**. The table above says so for the access token, and the per-event check also accepts a certificate credential on its id alone. `credentialId` is a plainly readable field. So whatever the type, reading a device's credentials gives you what you need to authenticate as that device.

For that reason, every query that returns a credential requires **`device:write`**, not `device:read`. A read-only user cannot list a device's credentials, which is why the console does not show them the **Credentials** tab. The requirement takes nothing away from a `device:write` holder: they can already register a credential for any device in the tenant and impersonate it. What it prevents is that capability reaching the read-only baseline that every enabled tenant member receives.

## How a device presents a credential

Credentials travel in the event body, on any transport (see [Connecting a Device](./connecting-a-device.md)):

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

The platform resolves the credential to the device that owns it and verifies it. Verification honors the credential's **expiry**, and **disabling** a credential revokes it. The instance's device-auth mode decides how strictly this is enforced:

- `disabled` — the self-asserted `device` token is trusted, and no credential is needed.
- `optional` — a presented credential is authoritative; without one, the device token is trusted.
- `required` — the event is rejected unless it presents a valid credential. **This is the default.**

When a credential authenticates, the device it resolves to is authoritative. An event whose `device` token names a *different* device is rejected, so one authenticated device cannot impersonate another.

## Two layers: the connection and the event

The credential in the event body is the **per-event** check. MQTT and NATS **connections** are also authenticated, at the broker itself:

- **TLS.** The MQTT and NATS listeners use TLS. A device connects over TLS with the instance CA.
- **Auth-callout.** The broker asks the platform to approve each new connection (a NATS auth-callout). The platform authenticates the connection and binds it to that one device's subjects, not its tenant's. The device can publish its own events and read its own commands, and nothing else. An `MQTT_BASIC` device connects with the MQTT username `{tenant}:{credentialId}` and the credential password: the same credential that authenticates its events. A device that can't authenticate can't even connect.
- **Client id.** The connection must also present the MQTT client id `{instanceId}:{tenant}:{deviceToken}`. It may add `:` and a suffix of the device's choosing, for example `{instanceId}:{tenant}:{deviceToken}:cmd`. The suffix lets a second concurrent session run without evicting the first, such as one connection publishing and another subscribed for commands. Any id that does not start with the device's own `{instanceId}:{tenant}:{deviceToken}` is refused. The broker files a device's session under its client id, so letting the device choose it freely would let one device take over another's session.

See [Connecting a Device](./connecting-a-device.md) for the transport details.

## Repeated failed connects are slowed down {#connect-backoff}

After repeated failures, MQTT connects that present a username and password are slowed down, so a password cannot be guessed at the rate the broker accepts connections.

- **What counts:** consecutive failed password connects for one MQTT username (`{tenant}:{credentialId}`). An unknown username counts exactly like a real one, so the response never reveals which usernames exist.
- **The schedule:** the first 10 consecutive failures are not slowed down. After the 10th, the next attempt on that username waits 1 second. Each further failure doubles the wait, up to 30 seconds.
- **During the wait, even the correct password is refused.** The broker gives the same refusal as for a wrong password. The device connects normally once the wait is over.
- **A successful connect resets the count.**
- **Not slowed down:** access-token connects (a connect with no password), and credentials carried in event bodies. The per-event check gives the sender no answer, so it cannot be used to guess.

:::warning Anyone who knows a device's username can delay its reconnects
The count is per username, and the username is not secret. Someone who keeps sending wrong passwords for it can stop that device reconnecting for as long as they keep going: each wait is capped at 30 seconds, but they can start the next as soon as the last ends. A device already connected is not affected until it reconnects. Don't publish device usernames, and give every `MQTT_BASIC` credential a strong password.
:::

The counts are kept in JetStream, the broker's persistent storage, so every device-management replica sees the same ones.

- **If JetStream cannot be reached, password connects are refused** until it can, because a connect that cannot be counted is not checked. This includes a brief window while the JetStream leader of the bucket that holds the counts changes, for example while a NATS node restarts. Access-token connects keep working.
- **The bucket holding the counts is size-limited.** Every connect, successful or not, keeps an entry for ten minutes. If the bucket fills, connects keep working but are **no longer slowed down**, and the `DeviceCredentialAttemptStoreFull` alert fires. That happens when connects arrive for a very large number of different usernames, or when a very large fleet reconnects at once. The bucket empties on its own ten minutes later. To give it more room, raise `instance.config.infrastructure.nats.kvStateMaxBytes`.

The `devicechain_devicemanagement_credential_checks_total` metric counts every password connect check by `outcome`. The outcomes include:

- `throttled` — a connect refused during a wait.
- `unavailable` — the counts could not be reached.
- `store_full` — the bucket was full.

## Register a credential (console)

1. Open the device's detail page and select the **Credentials** tab.
2. Choose the credential **type** and fill in the fields for that type: generate or paste an access token, enter a username and password for MQTT-basic, or enter a certificate id for X.509.
3. For `MQTT_BASIC`, record the password before you continue. The field is cleared on success and the password is never shown again.
4. Click **Add credential**.

To delete a credential, use its row. The device can no longer authenticate with it.

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

For `MQTT_BASIC`, also pass `credentialValue: "<password>"` (write-only). Registering a credential and listing a device's credentials both require the `device:write` authority; see [Reading a credential requires `device:write`](#reading-a-credential).
