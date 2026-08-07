<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Verification checklist

**Status (2026-07-31):** items 0–3 **VERIFIED live on Unity 6000.5.3f1 + URP** (the whole non-WebGL
stack — compile, smoke test, and auth/query/subscribe against a live cluster with real telemetry).
Item 4 (**WebGL**) is still the one **open follow-up**, but its SDK-side blocker is now **closed**
(slice 4.3, the read-loop pump) — what remains there is a real browser build, which nothing below
one can stand in for.

**Tier-1 (automated, no Editor):** the Runtime + Sample C# is compiled against real UnityEngine 2021.3
reference assemblies in BOTH platform branches by `sdks/unity/tools/UnityCompileCheck` (locally via
`tools/UnityCompileCheck/compile-check.sh`; enforced in CI as `unity-compile-check`). Covers wrong-API
calls and `#if UNITY_WEBGL` branch typos — but does **not** run the code, compile the `.jslib` (it's
JavaScript), or produce a WebGL build.

## 0. Stage the SDK assemblies ✅

```bash
sdks/unity/stage-sdk.sh
```
Produces 11 assemblies in `Runtime/Plugins/`: `DeviceChain.Sdk.dll`, `System.Text.Json.dll`,
`System.Text.Encodings.Web.dll`, `System.Threading.Channels.dll`, `Microsoft.Bcl.AsyncInterfaces.dll`,
`MQTTnet.dll`, plus the 5 netstandard2.1 facades below.

`MQTTnet.dll` backs the MQTT device plane (publish telemetry + receive commands). It adds exactly
one DLL because MQTTnet has **zero** transitive package dependencies on `netstandard2.1` — measured
from the restore graph, not assumed — so it needs no change to the staging script and no addition to
the prune set below (Unity ships nothing that collides with it).

## 1. Package imports & compiles ✅ (Unity 6/URP)

- Add the package (embed under `Packages/` or Add-from-disk). Resolves with no manifest errors.
- **Prune the DLLs Unity already ships**, or you get duplicate-assembly errors. On **Unity 6** the
  clean set is: **delete** `System.Buffers`, `System.Memory`, `System.Numerics.Vectors`,
  `System.Runtime.CompilerServices.Unsafe`, `System.Threading.Tasks.Extensions` (all part of Unity's
  netstandard2.1 profile); **keep** the other 6 (`DeviceChain.Sdk`, `System.Text.Json`,
  `System.Text.Encodings.Web`, `System.Threading.Channels`, `Microsoft.Bcl.AsyncInterfaces`,
  `MQTTnet`). This is the classic System.Text.Json-in-Unity conflict set. With that prune the
  package compiles clean.
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
  `ConfigureAwait(false)` in your driver). Since slice 4.3 the SDK no longer uses
  `ConfigureAwait(false)` on its own operational path either, so an `await foreach` over a
  subscription now resumes on the context it was started from rather than a pool thread.
- **Not yet exercised:** `DevicePublisher(...).EmitMeasurementsAsync(...)` (device-plane emit *from*
  Unity) — the transports it rides on are already proven above; it needs a device credential in the
  scene.

## 4. WebGL build — OPEN FOLLOW-UP (still not proven)

The one unverified path. Its **SDK blocker is closed**; its **browser validation is not**, and the
two must not be confused.

### Closed — the read-loop pump (slice 4.3)

`GraphQlWsClient` started its read loop with `Task.Run`, which under WebGL/IL2CPP is never
scheduled — a live subscribe would ack and then yield nothing, and `DisposeAsync` (awaiting that
never-run loop) would hang. Two things were wrong, not one: even without `Task.Run`, every await on
the read path carried `ConfigureAwait(false)`, which *asks* for the missing pool.

The fix injects **how** the loop is driven (`IReadLoopDriver`), the third platform seam alongside
`IHttpTransport`/`IWebSocketFactory`: `BackgroundTaskDriver` (default, unchanged .NET behaviour) or
`ManualPumpDriver`, which advances the loop one turn per `DeviceChainPump.Update()` on the main
thread. `ConfigureAwait(false)` is gone from the operational path (teardown deliberately keeps it —
a component disposes in `OnDestroy`, after its last `Update`).

Pinned by `ReadLoopPumpTests` in the C# SDK over a fake socket that mirrors
`WebGlWebSocketFactory`'s real shape (a poll loop whose only suspension point is a bare
`await Task.Yield()`), and every one of those tests was checked to FAIL against a deliberately
broken driver before being trusted.

### Still open — the browser

- **HTTP:** a WebGL build should still auth + query via `UnityWebRequestHttpTransport` (browser
  `fetch`). Expected to work; unverified.
- **WebSocket:** `DeviceChainWebSocket.jslib` is **JavaScript**. Nothing below a real WebGL player
  compiles or runs it, so neither the tier-1 compile check nor the SDK unit tests say anything
  about it. Validation means a WebGL build in a browser against a live cluster.

🔴 A green tier-1 plus green `ReadLoopPumpTests` is **not** WebGL confidence. It is confidence that
the SDK no longer makes WebGL impossible.

## 5. Next

With the non-WebGL base proven and the pump blocker cleared, the next increments are: (a) the WebGL
browser build itself, and (b) reconcile-by-`externalId` + MonoBehaviour twin scaffolding ("expand
from there").

A tier-2 rung would close the gap between them: headless Editor batchmode tests via the game-ci
GitHub Action (needs a Unity license secret). That runs code, unlike tier-1 — though still not the
`.jslib`.
