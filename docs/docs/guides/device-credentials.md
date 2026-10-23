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
| `ACCESS_TOKEN` | a bearer token (the credential id) | a one-way, keyed digest of the token |
| `MQTT_BASIC` | a username (the credential id) + password | a one-way, keyed digest of the password |

:::note X.509 credentials are not accepted
The `X509_CERTIFICATE` type is not supported until certificate verification ships. Creating or updating a credential of that type is refused (`UNSUPPORTED`), and a credential of that type that already exists no longer authenticates. Use `ACCESS_TOKEN` or `MQTT_BASIC`.
:::

## Reading a credential requires `device:write` {#reading-a-credential}

A secret is **write-only**, and both types have one: the password of an `MQTT_BASIC` credential and the token of an `ACCESS_TOKEN` credential. You submit it when you register the credential, and the platform keeps only a one-way, keyed digest of it, never the secret itself. The API returns `null` for `credentialValue` on every read. An access token is the `credentialId`, and the API returns it exactly **once**: in the response to the call that sets it (registering the credential, an update that rotates it, or a device replacement that mints one). Every read after that returns a fingerprint in its place, a value that begins `dctk1$`, identifies the credential and cannot be presented as the token. The console shows a password or a token **once**, right after you set it, and never again. See [Show-once passwords and tokens, and rotation](#show-once).

So whatever the type, reading a device's credentials does not give you what you need to authenticate as that device. You can still find a credential by its token: `deviceCredentials` accepts the token, or the fingerprint, as the `credentialId` in its criteria, and the platform digests the token you type before it matches.

Every query that returns a credential still requires **`device:write`**, not `device:read`, because the listing names a device's usernames, expiries and state. A read-only user cannot list a device's credentials, which is why the console does not show them the **Credentials** tab. The requirement takes nothing away from a `device:write` holder, who can already register a credential for any device in the tenant. What it does is keep the listing out of the read-only baseline that every enabled tenant member receives.

## How a device presents a credential

Over HTTP, a credential travels in the event body (see [Connecting a Device](./connecting-a-device.md)). Over MQTT it authenticates the connection and the event body carries none: an event on the platform's MQTT broker that includes `credentialType`, `credentialId` or `credentialSecret` is refused. The HTTP form is:

```json
{
  "device": "sensor-001",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "eventType": "Measurement",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

`MQTT_BASIC` additionally carries `"credentialSecret": "<password>"`. These are HTTP fields only.

The platform resolves the credential to the device that owns it and verifies it. Verification honors the credential's **expiry**, and **disabling** a credential revokes it. The instance's device-auth mode decides how strictly this is enforced:

- `disabled` — the self-asserted `device` token is trusted, and no credential is needed.
- `optional` — a presented credential is authoritative; without one, the device token is trusted.
- `required` — the event is rejected unless it presents a valid credential. **This is the default.**

When a credential authenticates, the device it resolves to is authoritative. An event whose `device` token names a *different* device is rejected, so one authenticated device cannot impersonate another.

### How quickly a revocation takes effect {#revocation-timing}

Each `device-management` replica keeps a credential it has just verified in memory for up to five minutes by default (`inMemoryCache.ttlSeconds`, from 1 to 3600 seconds), so that the device's next events are checked without reading the database again. The copy is checked exactly as the stored credential is: the password of an `MQTT_BASIC` credential is compared on every event, and an expiry takes effect at its time. A credential that failed to verify is never kept, so a corrected credential works on the device's next event.

When you disable, delete or change a credential, or replace, edit or delete its device, the replica that makes the change drops its copy before it answers you. It then tells the other replicas to drop theirs. So a revocation normally takes effect on the device's next event. A replica whose connection to the broker comes back after a drop discards every copy it holds, since it may have missed such a message. If the message is lost without a disconnect, or some replicas still run an earlier release during an upgrade, the revoked credential can still authenticate events on that replica until its copy expires: up to five minutes after the change by default. Lower `inMemoryCache.ttlSeconds` if that is too long for you; each event whose credential is not held in memory then reads the database.

Connections are not affected. Every MQTT connect, with a password or an access token, is checked against the database, so a revoked credential cannot open a new connection on any replica. When a deleted tenant's data is erased, every replica drops that tenant's copies.

What a device presents is not kept in the clear on its way through the platform either. The password or access token that arrives with an event is sealed before the event is put on the platform's internal event stream, and the copy of a message kept for diagnosis, such as one that could not be decoded, has its credentials removed.

## Two layers: the connection and the event

The credential in the event body is the **per-event** check. MQTT and NATS **connections** are also authenticated, at the broker itself:

- **TLS.** The MQTT and NATS listeners use TLS. A device connects over TLS with the instance CA.
- **Auth-callout.** The broker asks the platform to approve each new connection (a NATS auth-callout). The platform authenticates the connection and binds it to that one device's subjects, not its tenant's. The device can publish its own events and read its own commands, and nothing else. An `MQTT_BASIC` device connects with the MQTT username `{tenant}:{credentialId}` and the credential password: the same credential that authenticates its events. A device that can't authenticate can't even connect.
- **Client id.** The connection must also present the MQTT client id `{instanceId}:{tenant}:{deviceToken}`. It may add `:` and a suffix of the device's choosing, for example `{instanceId}:{tenant}:{deviceToken}:cmd`. The suffix lets a second concurrent session run without evicting the first, such as one connection publishing and another subscribed for commands. Any id that does not start with the device's own `{instanceId}:{tenant}:{deviceToken}` is refused. The broker files a device's session under its client id, so letting the device choose it freely would let one device take over another's session.

See [Connecting a Device](./connecting-a-device.md) for the transport details.

## Disabling a device {#disabling-a-device}

Disabling a device shuts it out without deleting it or touching its credentials. Use it to quarantine a device that is compromised or flooding the platform, then enable it again when it is fixed.

While a device is disabled:

- **Its events are discarded.** They are refused when they arrive, whichever credential or transport they came in on, and counted in `devicechain_devicemanagement_resolve_device_disabled_total`. They are not stored and not dead-lettered, and they do not come back when the device is enabled again. Its connection status is the exception, because the platform reports it rather than the device: when a connection the device already had ends, the device shows as disconnected, and the change is kept in its event history like any other connection change. That raises no alarm. A Sparkplug device can also show as connected while it is disabled, because its edge node reports its connection on its behalf.
- **It cannot open a new connection.** The broker refuses the connect with the same answer as a wrong password, and an LwM2M device's registration is refused with `4.03 Forbidden`. A connection the device already had stays open until it ends, but everything it publishes on it is discarded or refused: its events, its configuration requests and reports, and its update reports and download requests. The platform also stops reading its device information over LwM2M.
- **It is sent no new configuration or updates, and is caught up when you enable it.** A configuration revision made while the device is disabled is kept but not sent, and no update is offered to it. Enabling the device sends it its latest configuration revision and re-offers its current update, so it normally does not have to wait for its next restart or sync. Because it was refused while disabled, a sync it made then was not answered; nothing tells the device that.
- **Commands to it are refused** with `DEVICE_DISABLED`. A fleet command lists it among the refused devices. See [When an enqueue is refused](./sending-commands.md#when-an-enqueue-is-refused). Commands queued for it **before** it was disabled are **held**: not sent, and not cancelled. They go out, oldest first, when you enable it again, or lapse at their own expiry time while they wait. The device's **Commands** tab in the console shows how many are held, with a **Cancel all** action; see [Commands held for a disabled device](./sending-commands.md#held-while-disabled). The hold takes effect within moments of the disable, so a command published in that instant still reaches the device, and a command already sent is at the device and cannot be recalled.
- **It raises no silence alarm.** A rule that alarms when a device stops reporting ignores a disabled device, and a silence alarm it had already raised is **cleared** when the device is disabled. When it is enabled again, the rule waits a full silence window from that moment before it can report it silent.
- **Everything else is kept**: its data, credentials, configuration, relationships and group memberships.

The change takes effect on every replica on the device's next event: it is a change to the device, so it drops the cached copies the same way a [revocation](#revocation-timing) does.

In the console, open the device and choose **Disable** (or **Enable**); a disabled device shows a **Disabled** badge on its page and in the device list. Through GraphQL, set `enabled` in a partial update. `null` is refused, and disabling a device that is already disabled keeps the time it was first disabled, which `disabledAt` reports:

```graphql
mutation {
  updateDevice(token: "pump-7", request: { enabled: false }) {
    token
    enabled
    disabledAt
  }
}
```

A device can also be created disabled, with `enabled: false` in `createDevice`. The change is recorded in the audit log as an update of the device.

## Repeated failed connects are slowed down {#connect-backoff}

After repeated failures, MQTT connects that present a username and password are slowed down, so a password cannot be guessed at the rate the broker accepts connections.

- **What counts:** consecutive failed password connects for one MQTT username (`{tenant}:{credentialId}`). An unknown username counts exactly like a real one and is refused with the same answer, so the answer never reveals which usernames exist. The time a refusal takes is close but not identical: checking a username that exists reads one database row that an unknown username does not. That row holds only what the check needs, so the difference does not grow with how much the device stores in its metadata, name or description.
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
2. Choose the credential **type** and fill in the fields for that type: generate or paste an access token, or enter a username and password for MQTT-basic. For a password, type one or click **Generate strong password**.
3. Click **Add credential**.
4. A banner now shows the password (for `MQTT_BASIC`) or the access token (for `ACCESS_TOKEN`) with a copy button. **Copy it now**: this is the only time it is shown. Click **Done** when you have it. Leaving the page, reloading it or opening another device also clears the banner.

To delete a credential, use its row. The device can no longer authenticate with it.

## Show-once passwords and tokens, and rotation {#show-once}

Because the platform keeps only a digest of an `MQTT_BASIC` password and of an `ACCESS_TOKEN` token, nobody can read either back, including an administrator. The console follows from that:

- **The password or token is shown once**, in a banner with a copy button, when you add the credential or set a new one. After that the credential's row says only that a password or an access token is set. No screen ever shows a stored secret or its digest, and no form is filled in with one.
- **A password or token that is lost cannot be recovered.** Set a new one.
- **To rotate a password**, click **Set new password** on the credential's row, type one or click **Generate strong password**, and click **Save password**. The new password is shown once, as above. The device's current password stops opening new connections as soon as you save; on events it stops as a revocation does, within minutes by default (see [How quickly a revocation takes effect](#revocation-timing)). Have the new one ready to load onto the device. The credential's other fields are left as they were.
- **To rotate an access token**, click **Set new token** on the credential's row and confirm. The console generates a new token and shows it once, as above. The device's current token stops opening new connections as soon as you confirm, and stops on events as a revocation does. Through GraphQL, call `updateDeviceCredential` with only `credentialId`.
- **Generated passwords** are 24 random bytes from the browser's secure random source, written in URL-safe base64 (32 characters).
- **Length.** The platform refuses an `MQTT_BASIC` password shorter than 16 bytes. The console warns as you type, counting UTF-8 bytes rather than characters (a character outside ASCII takes two to four), and still sends what you typed, so the platform's own refusal is the one you see if it disagrees.

An `MQTT_BASIC` **username** is an identifier, not a secret, and stays listed with a copy button.

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

For an `ACCESS_TOKEN`, the `credentialId` in the response is the token you sent, and it is the last time any response contains it. A token is at most 256 bytes and is stored only as a keyed digest, so it is checked exactly as you send it, byte for byte. To replace a token, call `updateDeviceCredential` with only `credentialId`: the old token stops working at once and the response carries the new one. Changing an existing credential to or from `ACCESS_TOKEN` requires `credentialId` in the same request (`INVALID_VALUE` otherwise), because the stored id means a token under one type and a username under the other.

For `MQTT_BASIC`, also pass `credentialValue: "<password>"` (write-only). The password is checked exactly as you send it, including any leading or trailing spaces, so it must match what the device presents byte for byte; an empty value stores no password. The password must be at least 16 bytes long (and at most 4096); a shorter one is refused with `extensions.code` `INVALID_VALUE`, on registering a credential and on changing its password. A password set before this minimum existed keeps working until you change it. Changing an existing credential to `MQTT_BASIC` requires `credentialValue` in the same request. To change it later, call `updateDeviceCredential` with only `credentialValue`: the other fields are left alone, and the old password stops working at once. Neither call returns the password. Registering a credential and listing a device's credentials both require the `device:write` authority; see [Reading a credential requires `device:write`](#reading-a-credential).
