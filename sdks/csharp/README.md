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
| Device-plane emit | `Ingest.DeviceEventPublisher` | Measurement + Location over `POST /{instanceId}/{tenant}/events` or MQTT (the `JsonEvent` shape, in-body credential per ADR-014/025) |
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

// …and where it is. Latitude/longitude are required; the rest are optional and are OMITTED from
// the wire when null — send what the receiver actually knows rather than a placeholder, because a
// zero heading is stored as "due north" and can never be told apart from a measured one afterwards.
await publisher.EmitLocationAsync("car-42", credentialId,
    new LocationFix(33.74912345, -84.38812345)
    {
        Elevation = 320.5,                                  // metres above the WGS84 ELLIPSOID
        Speed = 24.6,                                       // metres per second
        Heading = LocationFix.CanonicalHeading(bearing),     // degrees clockwise from true north
    });
```

An HTTP emit the ingress does not accept throws `GraphQlRequestException` with the status. When the
response carried a `Retry-After`, it is on `RetryAfter`: a `429` (the tenant is over its ingest rate
limit) or a `503` meaning the platform is applying backpressure because a consumer is far behind.
Both mean the event was not stored; wait `RetryAfter` and send it again. A `503` with no
`RetryAfter` means the publish itself failed, and the event may have been stored.

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
  This includes the server's own close at token expiry: the server ends a subscription socket with
  close code **4401** when the access token it authenticated with expires, which surfaces from
  `SubscribeAsync` as a `GraphQlRequestException` naming that code. Re-subscribing opens a new socket,
  and its `connection_init` carries a fresh token from the `TokenProvider` (`AuthSession` refreshes it).
  The socket carries subscription operations only; send queries and mutations over HTTP.
- Provisioning mutation helpers (the Go `dcctl sim` runner provisions; a twin is read + subscribe + emit).

## MQTT device plane

`MqttDeviceSession` speaks the device plane the way a device does: it publishes telemetry and
receives commands over MQTT, which is the only carrier commands travel on. One session is one
device — MQTT 3.1.1 has no shared subscriptions and the broker grant confines a connection to a
single device — so a scene with 18 machines holds 18 connections.

Three properties are worth knowing before you use it:

- **A subscribe is not a subscription until the broker grants it.** `StartAsync` returns only after
  a confirmed SUBACK, and a refusal leaves the session `Blind` rather than `Ready`. This is not
  defensive coding: `SubscribeAsync` in the underlying client completes *successfully* on a refusal,
  so the naive spelling yields a device that connects, reports healthy, and receives nothing.
- **A command is answered for what the device did, not for what arrived.** The handler's returned
  outcome becomes the response; acknowledging on receipt would report that a machine acted when
  only the network did.
- **Commands run one at a time by default.** A slow handler holds back every later command for that
  device until it returns. [Running commands concurrently](#running-commands-concurrently) is the
  opt-out, and it has a cost you should read first.

Telemetry can go over either carrier. Only the MQTT one reaches the ingest durability capture
stream, which the HTTP ingress does not feed.

### Running commands concurrently

```csharp
var options = new MqttSessionOptions(brokerUri, instanceId, tenant, deviceToken, credentialId)
{
    MaxConcurrentCommands = 4,
    // Commands that return the same lane run one after another; null means "no ordering".
    CommandLane = c => c.Name is "writeFirmware" or "executeFirmware" ? "firmware" : null,
};
```

`MaxConcurrentCommands` defaults to 1, which is the one-at-a-time behavior above, unchanged. Above
1, the receive callback hands each command to a bounded executor and returns, so later commands are
received while earlier handlers run. At most that many handlers run at once; the rest wait and
start in arrival order. Handlers run on thread-pool threads, and `RunningCommands` and
`QueuedCommands` report the current counts.

🔴 **The platform delivers a device's commands in order; above 1 the SDK executes them in that
order only within a lane.** Commands in the same lane run strictly in arrival order, each starting
after the previous handler returned. Different lanes run in parallel, up to the cap. A command
with no lane (no `CommandLane`, or a selector that returns `null`) has no ordering constraint at
all. That default is deliberate: someone raising the cap is asking for parallelism, and treating
`null` as one shared lane would make the setting do nothing until a selector was also written. To
order everything except a few commands, return one constant lane for everything else. A write
followed by an execute, such as a firmware update, **must** share a lane.

Start order among the commands that can start is arrival order, but a command whose lane is busy
is passed over, so a later command in a free lane can start first. The order the *responses* are
published in is not guaranteed, even within a lane.

A selector that throws does not run the command in no lane, which would silently drop the ordering
you asked for. The command is not run and is answered as failed. The settings are read once, when
the session is constructed.

**Duplicates.** A command token that arrives again, whether its handler is running, still queued,
or finished, does not run the handler again. It is answered once per delivery, under that delivery's
dispatch nonce. Above 1 this window is live: the callback returns at admission, so a re-dispatched
command can arrive while its first delivery is running. A duplicate never waits for the handler.

**Acknowledgement.** MQTTnet acknowledges a QoS 1 message when the receive callback returns.
At 1 that is after the handler and the response, as before. Above 1 it is at admission, before the
handler runs. For commands issued by the platform this changes nothing: they reach the device at
QoS 0, which has no acknowledgement, so the platform's sweep and the broker never redeliver them on
the strength of one. It matters only for a QoS 1 message published to the command topic by another
MQTT client. If the process crashes between admission and completion, that message is not
redelivered at the higher setting (it is not run), where at 1 it would be redelivered and run
again, because the de-duplication history lives in memory.

🔴 **A response is not held across a disconnect, at any setting.** A handler that finishes while the
session is reconnecting has its response dropped, and nothing re-dispatches a command the platform
already sent over MQTT, so the command stays `SENT` until it times out. A longer-running handler
widens that window.

**Dispose.** Disposing the session at a value above 1 first stops the executor: commands still
waiting are never started and are not answered (they stay `SENT` until they time out), then it
cancels the token running handlers were given and waits for them for up to
`CommandShutdownTimeout` (5 seconds by default, at most 24 days). A handler that ignores cancellation past that
bound is abandoned and disposal still returns. Responses from handlers that finish during or after
disposal are not published, so those commands time out rather than being answered. A handler that
disposes its own session waits out the whole bound. At 1, disposal is unchanged and does not wait
for the handler.

**Unity and WebGL.** The executor adds `Task.Run` and nothing else of its own. It adds no new WebGL
blocker: the MQTT session already depends on MQTTnet's own `Task.Run` read loop, which is the open
WebGL blocker recorded in the Unity package README. A Unity host still marshals to the main thread
in its own pump, and handlers may now run concurrently, so that pump must be safe to call from
several threads.

### What the gates cover, and what they do not

`dotnet test` runs against a real `nats-server` MQTT gateway in CI — the broker the platform ships,
built from the version the backend pins — plus a scripted broker for the wire outcomes a
cooperative broker will not produce on demand (a refused subscription). A Native-AOT gate publishes
a native binary **and runs it** through a real round-trip.

🔴 None of that is evidence about IL2CPP. NativeAOT and IL2CPP are different AOT compilers and
`dotnet test` runs on CoreCLR, so a green job is strong evidence for the Unity path and proof of
nothing about it. Only a native Unity player closes that gap.
