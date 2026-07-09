<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Verification checklist

**Status (2026-07-09):** items 0–3 **VERIFIED live on Unity 6000.5.3f1 + URP** (the whole non-WebGL
stack — compile, smoke test, and auth/query/subscribe against a live cluster with real telemetry).
Item 4 (**WebGL**) is the one **open follow-up** — see it for the known blocker.

**Tier-1 (automated, no Editor):** the Runtime + Sample C# is compiled against real UnityEngine 2021.3
reference assemblies in BOTH platform branches by `sdks/unity/tools/UnityCompileCheck` (locally via
`tools/UnityCompileCheck/compile-check.sh`; enforced in CI as `unity-compile-check`). Covers wrong-API
calls and `#if UNITY_WEBGL` branch typos — but does **not** run the code, compile the `.jslib` (it's
JavaScript), or produce a WebGL build.

## 0. Stage the SDK assemblies ✅

```bash
sdks/unity/stage-sdk.sh
```
Produces 10 assemblies in `Runtime/Plugins/`: `DeviceChain.Sdk.dll`, `System.Text.Json.dll`,
`System.Text.Encodings.Web.dll`, `System.Threading.Channels.dll`, `Microsoft.Bcl.AsyncInterfaces.dll`,
plus the 5 netstandard2.1 facades below.

## 1. Package imports & compiles ✅ (Unity 6/URP)

- Add the package (embed under `Packages/` or Add-from-disk). Resolves with no manifest errors.
- **Prune the DLLs Unity already ships**, or you get duplicate-assembly errors. On **Unity 6** the
  clean set is: **delete** `System.Buffers`, `System.Memory`, `System.Numerics.Vectors`,
  `System.Runtime.CompilerServices.Unsafe`, `System.Threading.Tasks.Extensions` (all part of Unity's
  netstandard2.1 profile); **keep** the other 5 (`DeviceChain.Sdk`, `System.Text.Json`,
  `System.Text.Encodings.Web`, `System.Threading.Channels`, `Microsoft.Bcl.AsyncInterfaces`). This is
  the classic System.Text.Json-in-Unity conflict set. With that prune the package compiles clean.
- `Marshal.PtrToStringUTF8` was replaced by a portable manual decode, so no .NET-profile pin remains.

## 2. Spinning-logo smoke test (the slice-4 acceptance gate) ✅ (Unity 6/URP)

- Import the **Spinning Logo (smoke test)** sample; empty scene → empty GameObject → `SpinningLogo` →
  Play.
- **PASS = a blue cube rotates and gently pulses.** Confirmed on URP — the pipeline-aware material
  resolves `Universal Render Pipeline/Lit` (blue, not magenta), and the runtime-spawned camera/light
  work.

## 3. Editor / standalone full stack ✅ (Unity 6/URP, live telemetry)

Verified with a small Editor MonoBehaviour (`sdks/unity/tools/live-smoke/DeviceChainLiveSmoke.cs` —
copy into a project's `Assets/`; it uses **reflection-based** System.Text.Json, which is Editor-only
and deliberately NOT in the AOT-safe package). Against local kind (`superuser@devicechain.local` /
`devicechain`, tenant `sim-bp`, origin `http://localhost`):

- `CreateClient` → `LoginAsync` → `SelectTenantAsync` → `Gql.SendAsync` device query returned the
  `bp-therm-*` devices. (`UnityWebRequestHttpTransport` ✓)
- `Subscriptions(Area.EventManagement).SubscribeAsync(measurementStream …)` streamed **live
  temperature/co2/humidity/setpoint with self-describing units** into the Console once the buildingpulse
  sim was emitting. (`ClientWebSocketFactory` ✓) — call the SDK from the main thread (no
  `ConfigureAwait(false)` in your driver).
- **Not yet exercised:** `DevicePublisher(...).EmitMeasurementsAsync(...)` (device-plane emit *from*
  Unity) — the transports it rides on are already proven above; it needs a device credential in the
  scene.

## 4. WebGL build — OPEN FOLLOW-UP (not proven)

The one unverified path; deferred to its own slice increment.

- **HTTP:** a WebGL build should still auth + query via `UnityWebRequestHttpTransport` (browser
  `fetch`). Expected to work; unverified.
- **WebSocket:** the `DeviceChainWebSocket.jslib` + `WebGlWebSocketFactory` poll path is untested, and
  there is a **known blocker in the SDK**: `GraphQlWsClient` starts its read loop with `Task.Run`,
  which has no thread pool under WebGL — so a live subscribe yields nothing and `DisposeAsync` (which
  awaits that never-run loop) hangs. **Fix = make the read loop pump-based** (driven from a
  MonoBehaviour `Update`) — a small slice-4.1 SDK amendment — then validate the `.jslib` in a real
  WebGL player against a live cluster.

## 5. Next

With the non-WebGL base proven, the next increments are: (a) the WebGL read-loop pump + build, and
(b) reconcile-by-`externalId` + MonoBehaviour twin scaffolding ("expand from there").
