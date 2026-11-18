<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain SDK for Unity

The Unity client plugin for the DeviceChain platform — the digital-twin enabler. It is a thin
Unity layer over the AOT/IL2CPP-safe
[C# SDK](../../csharp): it supplies the Unity-appropriate **transports** behind the SDK's transport
seam and a platform-selecting bootstrap, so a scene can authenticate, query, subscribe to
live telemetry, and emit device-plane telemetry — as an **untrusted external client**, never the
admin surface.

> **Status: early — non-WebGL stack verified live.** On **Unity 6000.5.3f1 + URP** the package
> compiles, the spinning-logo smoke test renders, and the full SDK stack (auth + `UnityWebRequest`
> query + live `graphql-transport-ws` subscription) runs against a real cluster with live telemetry
> (VERIFICATION.md items 0–3). The **WebGL** browser build remains unverified. Its SDK read-loop
> blocker is fixed: `DeviceChainRuntime` attaches a `DeviceChainPump` to advance the subscription
> loop on the main thread (see VERIFICATION.md item 4). Also compile-checked against real
> UnityEngine reference assemblies in both platform branches (tier-1, enforced in CI).

## What's here

| Piece | Type | Role |
|-------|------|------|
| `DeviceChainRuntime` | static factory | `CreateClient(origin)` → a `DeviceChainClient` wired with platform-appropriate transports |
| `UnityWebRequestHttpTransport` | `IHttpTransport` | HTTP via `UnityWebRequest` (Editor, standalone, WebGL — where `HttpClient` fails) |
| `WebGlWebSocketFactory` / `…Connection` | `IWebSocketFactory` | browser `WebSocket` via `DeviceChainWebSocket.jslib` (WebGL only) |
| Spinning Logo | sample | a runtime smoke test (runtime-spawned, no backend) |

On every non-WebGL platform the WebSocket uses the SDK's own `ClientWebSocketFactory` (which works in
the Editor and IL2CPP standalone) — only WebGL swaps in the `.jslib` transport.

## Install

1. Stage the SDK assemblies (build artifacts, gitignored):
   ```bash
   sdks/unity/stage-sdk.sh
   ```
2. In Unity, **Window ▸ Package Manager ▸ + ▸ Add package from disk…** and pick
   `sdks/unity/io.devicechain.sdk/package.json`.
3. Import the **Spinning Logo (smoke test)** sample from the package page and run it (see
   [`Samples~/SpinningLogo/README.md`](./Samples~/SpinningLogo/README.md)).

Requires Unity **2021.3 LTS** or newer (for the .NET Standard 2.1 API profile).

## Usage

```csharp
using System;
using System.Threading;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Unity;
using UnityEngine;

public sealed class LiveMeasurements : MonoBehaviour
{
    private readonly CancellationTokenSource lifetime = new CancellationTokenSource();

    private async void Start()
    {
        await using var client = DeviceChainRuntime.CreateClient("https://devicechain.example.com");
        try
        {
            await client.LoginAsync("me@example.com", "your-password", lifetime.Token);
            await client.SelectTenantAsync("acme", lifetime.Token);
            await foreach (var data in client.Subscriptions(Area.EventManagement).SubscribeAsync(
                "subscription{measurementStream{deviceToken name value unit}}",
                EmptyVariables.Value, MyJson.Default.EmptyVariables,
                MyJson.Default.JsonElement, lifetime.Token))
            {
                Debug.Log(data.GetProperty("measurementStream").ToString());
            }
        }
        catch (OperationCanceledException) when (lifetime.IsCancellationRequested) { }
        catch (Exception error) { Debug.LogException(error); }
    }

    private void OnDestroy() => lifetime.Cancel();
}
```

`MyJson` below is application code, not an SDK type. Add this source-generated
context to your .NET project (for Unity, compile it into an assembly with the
System.Text.Json source generator before importing it). The result is a `JsonElement`
containing the GraphQL `data` object; replace it with your own result DTO and its
`JsonTypeInfo` when you want typed properties. `SdkJson` is internal to the SDK.

```csharp
using DeviceChain.Sdk.Json;
using System.Text.Json;
using System.Text.Json.Serialization;

[JsonSerializable(typeof(EmptyVariables))]
[JsonSerializable(typeof(JsonElement))]
public partial class MyJson : JsonSerializerContext { }
```

**Threading:** construct `DeviceChainRuntime.CreateClient` (and any standalone
`UnityWebRequestHttpTransport`) on Unity's main thread so the HTTP transport captures its
`SynchronizationContext`. The transport posts requests and cancellation-triggered aborts
back to that context, including when the SDK's token refresh resumes on a worker thread.
Cancellation from a background thread cancels the task and queues the request abort on the
main thread. Subscription enumeration preserves the consumer's context, so start `await foreach` on
the main thread to update the scene there. Native MQTT command handlers run on background
threads; marshal their scene changes back to Unity's main thread.

## Not yet

- Reconcile-by-`externalId` binding + MonoBehaviour twin scaffolding (the "expand from there" phase).
- WebGL live-subscribe hardening — see `VERIFICATION.md`.

See the [C# and Unity API guide](https://docs.devicechain.io/reference/csharp-sdk#unity) for authentication, command pulling,
MQTT session setup, transport ownership and teardown.
