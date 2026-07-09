<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain SDK for Unity

The Unity client plugin for the DeviceChain platform — the digital-twin enabler (ADR-035
sim-subsystem **slice 4**). It is a thin Unity layer over the AOT/IL2CPP-safe
[C# SDK](../../csharp): it supplies the Unity-appropriate **transports** behind the SDK's transport
seam (slice 4.1) and a platform-selecting bootstrap, so a scene can authenticate, query, subscribe to
live telemetry, and emit device-plane telemetry — as an **untrusted external client**, never the
admin surface.

> **Status: early / partially verified.** The Runtime + Sample C# is compile-checked against real
> UnityEngine reference assemblies in both platform branches (tier-1, enforced in CI — see
> [`VERIFICATION.md`](./VERIFICATION.md)), but has not been *run* in a Unity Editor. The
> **spinning-logo smoke test** and the **Editor/standalone** stack are expected to work; the **WebGL
> WebSocket** path (`.jslib` + `Task.Run` read-loop) is the known high-risk item. Work
> `VERIFICATION.md` on a Unity machine before relying on it.

## What's here

| Piece | Type | Role |
|-------|------|------|
| `DeviceChainRuntime` | static factory | `CreateClient(origin)` → a `DeviceChainClient` wired with platform-appropriate transports |
| `UnityWebRequestHttpTransport` | `IHttpTransport` | HTTP via `UnityWebRequest` (Editor, standalone, WebGL — where `HttpClient` fails) |
| `WebGlWebSocketFactory` / `…Connection` | `IWebSocketFactory` | browser `WebSocket` via `DeviceChainWebSocket.jslib` (WebGL only) |
| Spinning Logo | sample | the slice-4 acceptance smoke test (runtime-spawned, no backend) |

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
using DeviceChain.Sdk;
using DeviceChain.Sdk.Unity;

await using var client = DeviceChainRuntime.CreateClient("https://demo.devicechain.io");
await client.LoginAsync("me@example.com", "…");
await client.SelectTenantAsync("acme");

// Subscribe to live telemetry (drive a transform / material from resolved platform truth):
await foreach (var evt in client.Subscriptions(Area.EventManagement).SubscribeAsync(
    "subscription{measurementStream{deviceToken name value unit}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.MeasurementStreamData, ct))
{
    // …
}
```

**Threading:** call the SDK from Unity's main thread. `UnityWebRequest` is main-thread-affine, and
under WebGL there is only the main thread anyway. In the Editor/standalone the SDK's own awaits use
`ConfigureAwait(false)`, so continuations may land on a pool thread — marshal anything that touches
`Transform`/`Renderer` (and any follow-up SDK call) back onto the main thread. Note a corollary: a
`CancellationToken` cancelled from a background thread cannot actually abort an in-flight
`UnityWebRequest` (the abort is a no-op off the main thread) — the task cancels but the request
finishes; cancel from the main thread if you need the request torn down.

## Not yet

- Reconcile-by-`externalId` binding + MonoBehaviour twin scaffolding (the "expand from there" phase).
- WebGL live-subscribe hardening — see `VERIFICATION.md`.
