---
sidebar_position: 4
title: C# and Unity SDKs
---

# C# and Unity SDKs

Install the .NET package with `dotnet add package DeviceChain.Sdk`. It targets
.NET 8 and .NET Standard 2.1. You supply the platform origin; there is no default host.
The Unity package adds transports and a main-thread subscription pump to this same API.

## Client and authentication

```csharp
using System;
using DeviceChain.Sdk;

await using var client = new DeviceChainClient(new Uri("https://devicechain.example.com"));
await client.LoginAsync("me@example.com", "your-password");
await client.SelectTenantAsync("acme");
```

`LoginAsync(email, password, cancellationToken)` returns `IdentityAuth` with
`IdentityToken`, `ExpiresAt`, `Superuser` and `Memberships`. For an account with a
second factor it throws `DeviceChain.Sdk.Auth.MfaRequiredException`: pass its
`Challenge` and the person's code to `VerifyMfaAsync(challenge, code, cancellationToken)`
before selecting a tenant. See [two-factor sign-in](../guides/two-factor-authentication.md#csharp).

`SelectTenantAsync(tenant, cancellationToken)` returns an `AuthToken` containing
`AccessToken`, `RefreshToken` and `ExpiresAt`. All cancellation tokens in these calls
are optional. `client.Auth` exposes the session's `Memberships`, `Superuser` and current
`AccessToken`. Use `GetAccessTokenAsync(cancellationToken)` to obtain a token with
near-expiry refresh, or `RefreshAsync(cancellationToken)` to rotate explicitly.
The client wires `GetAccessTokenAsync` into HTTP and subscription authentication.

## Queries and subscriptions

`client.Gql.SendAsync(area, query, variables, variablesInfo, dataInfo, options, cancellationToken)`
returns the deserialized GraphQL **data object**, not a response envelope. `variablesInfo`
and `dataInfo` are `JsonTypeInfo<T>` supplied by your application's source-generated
System.Text.Json context. `options` and `cancellationToken` are optional; use
`new RequestOptions { Anonymous = true }` to skip the bearer token.

For example, in a .NET project with the System.Text.Json source generator:

```csharp
using System;
using System.Text.Json;
using System.Text.Json.Serialization;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Json;

await using var client = new DeviceChainClient(new Uri("https://devicechain.example.com"));
await client.LoginAsync("me@example.com", "your-password");
await client.SelectTenantAsync("acme");

JsonElement data = await client.Gql.SendAsync(
    Area.DeviceManagement,
    "query{devices(criteria:{pageNumber:1,pageSize:10}){results{token name}}}",
    EmptyVariables.Value, AppJson.Default.EmptyVariables, AppJson.Default.JsonElement);
Console.WriteLine(data.GetProperty("devices"));

[JsonSerializable(typeof(EmptyVariables))]
[JsonSerializable(typeof(JsonElement))]
internal partial class AppJson : JsonSerializerContext { }
```

Replace `JsonElement` with your DTO and its generated metadata for typed results.
`EmptyVariables` lives in `DeviceChain.Sdk.Json`; the SDK's own `SdkJson` context is internal.

`client.Subscriptions(area).SubscribeAsync(query, variables, variablesInfo, dataInfo, cancellationToken)`
returns `IAsyncEnumerable<TData>`. It shares one socket per area and takes the same
serialization metadata as HTTP. Cancel enumeration with the optional token, and dispose
the client on teardown. A dropped or expired socket requires the consumer to subscribe
again; the new connection obtains a fresh token. Query missed history after reconnecting.
`client.WebSocketUri(area)` returns the subscription endpoint; `Areas.Path(area)` returns
its HTTP path. Use the `Area` enum rather than a string area name.

`GraphQlRequestException` exposes `Status`, `Errors` and optional `RetryAfter`.
Each `GraphQlError` exposes `Message` and `Code`; inspect these rather than matching prose.

## Publishing telemetry

`client.DevicePublisher(ingressOrigin, instanceId, tenant)` creates an HTTP publisher.
The device ingress origin is separate from the platform GraphQL origin.
`EmitMeasurementsAsync(deviceToken, credentialId, metrics, occurredTime, cancellationToken)`
takes an `IReadOnlyDictionary<string, double>`.
`EmitLocationAsync(deviceToken, credentialId, fix, occurredTime, cancellationToken)` takes
`new LocationFix(latitude, longitude)` with optional `Elevation`, `Accuracy`, `Speed` and
`Heading`. Both methods return `Task`; occurrence time and cancellation token are optional.
Coordinates are WGS84 degrees, elevation is metres above the ellipsoid, accuracy is metres,
speed is metres per second, and heading is degrees clockwise from true north. Use
`LocationFix.CanonicalHeading(degrees)` to normalize a bearing; leave unknown fields null.

For MQTT, construct `new DeviceEventPublisher(new MqttDeviceEventCarrier(session, deviceToken))`.
The session must already be started and the token must match its device. The emit methods
still take `credentialId`, but the MQTT carrier omits it from the body because CONNECT
already authenticated the device. A custom `IDeviceEventCarrier` must implement
`CarriesBodyCredential` and `SendAsync(deviceToken, jsonEvent, cancellationToken)`.

## MQTT device sessions

Types live in `DeviceChain.Sdk.Mqtt` and `DeviceChain.Sdk.Transport`.

```csharp
var options = new MqttSessionOptions(brokerUri, instanceId, tenant, deviceToken, credentialId);
await using var session = new MqttDeviceSession(options);
await session.StartAsync(async (command, ct) =>
{
    // Apply the command to your device before returning its outcome.
    await ApplyCommandAsync(command, ct); // your application function
    return CommandOutcome.Succeeded();
}, cancellationToken);
```

The host supplies the URI, instance, tenant, device credential, cancellation token and
`ApplyCommandAsync(DeviceCommand, CancellationToken)` function in this fragment.
`CommandHandler` returns `Task<CommandOutcome>`; use `CommandOutcome.Failed(error)` for a
failure or `CommandOutcome.Succeeded(payload)` for success, where payload is optional JSON text.
`DeviceCommand` carries `Token`, `Name`, optional `JsonElement` `Payload` and arrival `Sequence`.
`StartAsync` requires both handler and cancellation token and waits for a confirmed subscription.
Observe `State` and `StateChanged` (`Starting`, `Ready`, `Reconnecting`, `Blind`, `Stopped`),
`RunningCommands`, `QueuedCommands` and `MalformedFrames` for health.

Useful `MqttSessionOptions` settings are `Trust` (system roots by default),
`ClientCertificate`, `ClientIdDiscriminator`, `KeepAlive` (60 seconds),
`OperationTimeout` (30 seconds), `CommandHistorySize` (256), and reconnect delays
`ReconnectInitialDelay` / `ReconnectMaxDelay` (1 / 30 seconds).
`MaxConcurrentCommands` defaults to 1. Above 1, use `CommandLane` to preserve ordering
for related commands; `CommandShutdownTimeout` bounds disposal's wait for handlers
(default 5 seconds, maximum `MaxCommandShutdownTimeout`, 24 days).
See the [SDK README](https://github.com/devicechain-io/devicechain/tree/main/sdks/csharp#running-commands-concurrently)
for concurrent execution and duplicate delivery behavior, and
[certificate authentication](../guides/device-x509.md#csharp-sdk) for TLS identity and trust.

## HTTP command pulling

`DeviceChain.Sdk.Commands.HttpCommandPuller` is constructed separately from the facade:
`new HttpCommandPuller(transport, ingressOrigin, instanceId, tenant, deviceToken, accessToken)`.
Use the device's access token and an `IHttpTransport`, such as `HttpClientTransport` wrapping
a host-owned `HttpClient`. The device type must enable `HTTP_PULL`.

1. `PullAsync(waitSeconds, cancellationToken)` returns `IReadOnlyList<PulledCommand>`;
   wait defaults to `DefaultWaitSeconds` (25), with a range of 0 to `MaxWaitSeconds` (30).
   An empty list means no commands arrived.
2. Call `AcknowledgeAsync(command.Token, cancellationToken)` before acting. `Attempt`
   identifies possible redelivery, so make the device operation idempotent.
3. If `DeliveryMode` is `"ONEWAY"`, acknowledgment completes delivery; do not respond.
   Otherwise call `RespondAsync(command.Token, success, payload, error, cancellationToken)`
   after acting. `payload` is an optional `JsonElement`, and `error` an optional string.

`PulledCommand` also exposes `Name`, optional `Payload` and optional `ExpiresAt`.
Cancellation tokens in these puller methods are optional. Failures use
`GraphQlRequestException`; respect `RetryAfter` when the server throttles polling.

## Unity

Stage the SDK with `sdks/unity/stage-sdk.sh`, then add
`sdks/unity/io.devicechain.sdk/package.json` from disk in Unity's Package Manager.
The package requires Unity 2021.3 or newer and the .NET Standard 2.1 API profile.

`DeviceChain.Sdk.Unity.DeviceChainRuntime.CreateClient(origin)` accepts a `Uri` or a
string and returns the same disposable `DeviceChainClient` shown above. It uses
`UnityWebRequestHttpTransport` on every platform, `ClientWebSocketFactory` on native
platforms, and `WebGlWebSocketFactory` in the WebGL player. WebGL subscriptions are
advanced automatically by `DeviceChainPump` on the main thread. The WebGL player
runtime path remains unverified; MQTT requires a native build.

Construct the Unity client on the main thread so its HTTP transport captures Unity's
synchronization context. HTTP requests and cancellation aborts are marshalled back to
that context automatically. Start subscription enumeration on the main thread to
preserve that context; marshal scene changes from native MQTT handlers back to it. Compile your query DTOs and source-generated JSON context in a .NET
assembly before importing them into Unity. Dispose the client when its owner ends.

A host supplying its own transports uses
`new DeviceChainClient(origin, httpTransport, webSocketFactory, readLoopDriver)`;
the client owns neither injected transport. `readLoopDriver` is optional on threaded
hosts. For a custom WebGL loop, pass a `ManualPumpDriver` and call `Pump()` every frame,
or use `DeviceChainPump.Attach(driver)` and `Detach(driver)` to manage Unity pumping.
The plain .NET constructor can reuse a caller-owned `HttpClient`; otherwise it owns
and disposes the one it creates.
