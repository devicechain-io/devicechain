---
sidebar_position: 7
title: Device APIs at a Glance
---

# Device APIs at a Glance {#device-apis}

Every way a device can reach DeviceChain, each with one command you can paste into a shell. This page is a map, not a specification: each row links to the page that explains the transport, and the [Device Protocol Reference](./device-protocol.md) lists every field and every refusal. What each transport can and cannot do is in the [Transport Capability Matrix](./transport-matrix.md).

Examples use the local kind install (tenant `acme`, instance `devicechain`). Replace the host, tenant and credentials with your own.

| Transport | Direction | Authenticates with | Go to |
| --- | --- | --- | --- |
| [MQTT](#mqtt) | telemetry in, commands out | credential id and secret, per connection | [Connecting a device](../guides/connecting-a-device.md#mqtt) |
| [HTTP](#http) | telemetry in | credential in the request body | [Connecting a device](../guides/connecting-a-device.md#http) |
| [LwM2M](#lwm2m) | telemetry in, writes out | DTLS pre-shared key | [LwM2M](../concepts/lwm2m.md) |
| [Sparkplug B](#sparkplug) | telemetry in, commands out | the source's broker credential | [Sparkplug B](../concepts/sparkplug.md) |
| [X.509 certificate](#x509) | as MQTT | client certificate | [Certificate devices](../guides/device-x509.md) |
| [Self-registration](#self-registration) | obtain a device and credential | provision key and secret | [Self-registration](../guides/device-self-registration.md) |

## MQTT {#mqtt}

Publish a [device event](./device-protocol.md#device-event) to the device's events topic on the platform broker. The username is `{tenant}:{credentialId}`, and the event body carries no credential.

```bash
mosquitto_pub --cafile instance-ca.crt -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/devices/sensor-001/events' \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

Commands arrive on a topic the device subscribes to:

```bash
mosquitto_sub --cafile instance-ca.crt -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/device-commands/sensor-001'
```

## HTTP {#http}

`POST` the same event body to the `event-sources` service. Over HTTP the credential travels in the body, and the answer is `202` when the event was accepted. A `429` or a `503` with `Retry-After` means send it again later.

```bash
curl -i -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

HTTP is upstream-only: an HTTP device cannot receive commands. Port 8081 has no transport authentication, so keep it behind network controls.

## LwM2M {#lwm2m}

An LwM2M client registers over CoAP on DTLS, **UDP 5684**, presenting a pre-shared-key identity that the platform resolves to a tenant and a device. Use any LwM2M client that supports PSK; with the Leshan demo client, for instance:

```bash
./leshan-client-demo -u coaps://<lwm2m-host>:5684 -n sensor-001 -i <pskIdentity> -p <pskKeyHex>
```

The platform then observes the device's sensor objects and stores each notification as a measurement. See [LwM2M](../concepts/lwm2m.md) for presence, writes and what is not supported.

## Sparkplug B {#sparkplug}

The platform is a Sparkplug host: it subscribes to your own broker and does not accept a connection from the edge node. Point a configured [source](../concepts/sparkplug.md) at the broker, then publish a node birth followed by data as usual. The topic names are the Sparkplug ones; the payloads are Protobuf, so a Sparkplug library builds them. With a plain MQTT client you can confirm the topic is reachable:

```bash
mosquitto_sub -h <your-broker> -p 1883 -u <user> -P <password> -t 'spBv1.0/plant-a/#' -v
```

Each `{group}/{node}` becomes the external id of a device in the source's tenant.

## X.509 certificate {#x509}

A device holding a certificate issued by a trusted CA connects with the username `{tenant}:@x509` and no password.

```bash
mosquitto_pub --cafile instance-ca.crt --cert device.crt --key device.key \
  -h <mqtt-host> -p 1883 -i 'devicechain:acme:<deviceToken>' -u 'acme:@x509' \
  -t 'devicechain/acme/devices/<deviceToken>/events' \
  -m '{"device":"<deviceToken>","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## Self-registration {#self-registration}

A device with a provision key and secret creates itself and its credential. Generate and save the credential before sending, then register:

```bash
curl -sS https://<instance-host>/api/device-management/provisioning/acme/register \
  -H 'Content-Type: application/json' \
  -d '{"provisionKey":"acme-sensors-2026","provisionSecret":"<secret>","externalId":"SN-000123","credentialSecret":"<credential you saved>"}'
```

The answer names the device token and the MQTT client id to connect with. Repeating the same request is safe and answers `REPLAYED`. See the [retry contract](./device-protocol.md#self-registration-retry).
