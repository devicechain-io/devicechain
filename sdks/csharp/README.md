<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain .NET SDK

The public C# client SDK for the DeviceChain platform — the Unity/.NET enabler (ADR-035
sim-subsystem slice 3). It wraps the platform's documented wire seams so a .NET host (and,
via the slice-4 Unity UPM plugin, a digital twin) authenticates, reads/queries, subscribes to
live telemetry, and emits device-plane telemetry — as an **untrusted external client**, never
the admin surface.

**AOT / IL2CPP-safe by construction:** serialization is source-generated
(`System.Text.Json` `[JsonSerializable]`), there is no `Reflection.Emit` or runtime codegen,
and the library builds with `TreatWarningsAsErrors` + `IsAotCompatible`. It multi-targets
`netstandard2.1` (Unity's IL2CPP-consumable target) and `net8.0`.

## What's here

| Piece | Type | Seam |
|-------|------|------|
| Auth state machine | `Auth.AuthSession` | `login → selectTenant → refresh` (ADR-033), with proactive near-expiry refresh; the `TokenProvider` a client plugs in |
| GraphQL over HTTP | `GraphQlClient` | typed query/mutate against `/api/{area}/graphql`; caller supplies its own `JsonTypeInfo` (AOT-safe) |
| Live subscriptions | `Subscriptions.GraphQlWsClient` | `graphql-transport-ws`, one multiplexed socket per area; token in `connection_init` |
| Device-plane emit | `Ingest.DeviceEventPublisher` | telemetry over `POST /{instanceId}/{tenant}/events` (the `JsonEvent` shape, in-body credential per ADR-014/025) |
| Transport seam | `Transport.IHttpTransport` / `Transport.IWebSocketFactory` | pluggable HTTP + WebSocket, so the SDK runs where `HttpClient`/`ClientWebSocket` don't (Unity WebGL) |
| Facade | `DeviceChainClient` | wires all of the above against one origin |

## Usage

```csharp
await using var client = new DeviceChainClient(new Uri("https://demo.devicechain.io"));

await client.LoginAsync("me@example.com", "…");
await client.SelectTenantAsync("acme");

// Query (the caller's own source-gen JsonTypeInfo keeps it AOT-safe):
var data = await client.Gql.SendAsync(
    Area.DeviceManagement,
    "query{devices(criteria:{pageNumber:1,pageSize:10}){results{token name}}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.DevicesData);

// Subscribe to live telemetry:
await foreach (var evt in client.Subscriptions(Area.EventManagement).SubscribeAsync(
    "subscription{measurementStream{deviceToken name value unit}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.MeasurementStreamData, ct))
{
    // …drive a chart / a twin transform
}

// Emit device-plane telemetry (an interactive twin). The device-plane ingress is a SEPARATE
// listener from the /api GraphQL origin, so its origin is passed explicitly:
var publisher = client.DevicePublisher(
    ingressOrigin: new Uri("https://ingress.demo.devicechain.io"), instanceId: "dc", tenant: "acme");
await publisher.EmitMeasurementsAsync("car-42", credentialId,
    new Dictionary<string, double> { ["speed"] = 55.0 });
```

## Custom transports (Unity WebGL)

Under Unity WebGL/IL2CPP, `System.Net.Http.HttpClient` and `System.Net.WebSockets.ClientWebSocket`
do not work — HTTP and WebSocket must go through the browser (via `UnityWebRequest` and a `.jslib`
`WebSocket` shim). The SDK never hard-depends on either type: everything runs over two small seams
with plain-.NET defaults (`HttpClientTransport`, `ClientWebSocketFactory`) that a host can replace.

```csharp
// On plain .NET nothing changes — the defaults are used automatically. On Unity WebGL, inject
// transports backed by UnityWebRequest + a browser WebSocket (shipped by the slice-4 UPM plugin):
await using var client = new DeviceChainClient(
    new Uri("https://demo.devicechain.io"),
    httpTransport: new MyUnityWebRequestTransport(),   // : IHttpTransport
    webSocketFactory: new MyBrowserWebSocketFactory()); // : IWebSocketFactory
```

`IHttpTransport` returns the full response for every status (the caller classifies it) and throws
only on a genuine transport failure. `IWebSocketConnection` speaks whole text messages and surfaces
a server close as a `Closed` message carrying the spec close code — so the SDK's error/close handling
is identical no matter which transport backs it. The `TransportSeamTests` exercise the SDK end-to-end
over in-memory fakes that are neither `HttpClient` nor `ClientWebSocket`, proving the WebGL substitution.

## Build & test

```bash
cd sdks/csharp
dotnet build -c Release   # both TFMs; warnings are errors
dotnet test  -c Release
```

> If `dotnet` is "not found", check `ls ~/.dotnet/dotnet` before installing anything — an SDK placed
> there by `dotnet-install.sh` is not on `PATH`, and the shell's suggestion to `snap install dotnet`
> would add a second one beside it. `export PATH="$HOME/.dotnet:$PATH"`.

There is also a manual rig, `tools/DeviceChain.Sdk.TrustProbe`, which drives the MQTT transport's
pinned-CA trust decision against a **real** broker. It needs a live cluster, so it is not a CI gate
(CI compiles it, and its real-broker test rung runs over plaintext) — but it is the only thing that
exercises that path through an actual TLS handshake, and it is the cheapest pre-flight before the
Unity player check. See [`sdks/unity/MQTT-PLAYER-VERIFICATION.md`](../unity/MQTT-PLAYER-VERIFICATION.md).

## Not yet (documented follow-ups)

- Subscription **auto-reconnect** on a dropped socket (a consumer re-subscribes today). The MQTT
  device session does reconnect, and re-reads the broker's grant when it does.
- Provisioning mutation helpers (the Go `dcctl sim` runner provisions; a twin is read + subscribe + emit).

## MQTT device plane

`MqttDeviceSession` speaks the device plane the way a device does: it publishes telemetry and
receives commands over MQTT, which is the only carrier commands travel on. One session is one
device — MQTT 3.1.1 has no shared subscriptions and the broker grant confines a connection to a
single device — so a scene with 18 machines holds 18 connections.

Two properties are worth knowing before you use it:

- **A subscribe is not a subscription until the broker grants it.** `StartAsync` returns only after
  a confirmed SUBACK, and a refusal leaves the session `Blind` rather than `Ready`. This is not
  defensive coding: `SubscribeAsync` in the underlying client completes *successfully* on a refusal,
  so the naive spelling yields a device that connects, reports healthy, and receives nothing.
- **A command is answered for what the device did, not for what arrived.** The handler's returned
  outcome becomes the response; acknowledging on receipt would report that a machine acted when
  only the network did.

Telemetry can go over either carrier. Only the MQTT one reaches the ingest durability capture
stream, which the HTTP ingress does not feed.

### What the gates cover, and what they do not

`dotnet test` runs against a real `nats-server` MQTT gateway in CI — the broker the platform ships,
built from the version the backend pins — plus a scripted broker for the wire outcomes a
cooperative broker will not produce on demand (a refused subscription). A Native-AOT gate publishes
a native binary **and runs it** through a real round-trip.

🔴 None of that is evidence about IL2CPP. NativeAOT and IL2CPP are different AOT compilers and
`dotnet test` runs on CoreCLR, so a green job is strong evidence for the Unity path and proof of
nothing about it. Only a native Unity player closes that gap.
