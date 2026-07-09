<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Verification checklist

**Tier-1 (automated, no Editor):** the Runtime + Sample C# is compiled against real UnityEngine 2021.3
reference assemblies in BOTH platform branches by `sdks/unity/tools/UnityCompileCheck` (run locally
with `tools/UnityCompileCheck/compile-check.sh`; enforced in CI as the `unity-compile-check` job). That
covers wrong-API calls and `#if UNITY_WEBGL` branch typos — but it does **not** run the code, compile
the `.jslib` (it's JavaScript), or produce a WebGL build.

The rest of this checklist is what tier-1 can't reach — run it on a machine with Unity **2021.3 LTS+**
before relying on the package. Items are ordered cheapest → riskiest.

## 0. Stage the SDK assemblies

```bash
sdks/unity/stage-sdk.sh
```
Confirm `Runtime/Plugins/` now contains `DeviceChain.Sdk.dll` plus `System.Text.Json.dll`,
`System.Threading.Channels.dll`, and their transitive deps.

## 1. Package imports & compiles

- Add the package from disk (`package.json`). It should resolve with no manifest errors.
- The `DeviceChain.Sdk.Unity` assembly should compile. **Most likely failure: duplicate/among
  assemblies Unity already ships** (e.g. `System.Buffers`, `System.Memory`,
  `System.Runtime.CompilerServices.Unsafe`). If so, delete the offending DLL(s) from `Runtime/Plugins/`
  — Unity provides them — until it compiles. Note which ones, and extend the `skip_re` denylist in
  `stage-sdk.sh` accordingly.
- Check `Marshal.PtrToStringUTF8` resolves under the project's API compatibility level (.NET Standard
  2.1). If not, replace it in `WebGlWebSocketFactory.cs` with a manual UTF-8 decode.

## 2. Spinning-logo smoke test (the slice-4 acceptance gate)

- Import the **Spinning Logo (smoke test)** sample; create an empty scene, add an empty GameObject,
  attach `SpinningLogo`, press Play.
- **PASS = a blue cube rotates (and gently pulses) on a dark background.** This proves the package
  loaded, the component ran, and the render loop is alive. No backend involved.

## 3. Editor / standalone full stack (proven transports)

Against a reachable cluster (e.g. local kind, superuser `superuser@devicechain.local`/`devicechain`,
tenant `sim-bp`):

- `DeviceChainRuntime.CreateClient(origin)` → `LoginAsync` → `SelectTenantAsync` → a `Gql.SendAsync`
  device query returns data. (Exercises `UnityWebRequestHttpTransport`.)
- `Subscriptions(Area.EventManagement).SubscribeAsync(measurementStream …)` yields live measurements.
  In the Editor this uses the SDK's `ClientWebSocketFactory` and should behave exactly like the C# SDK
  live-smoke. Marshal `next` payloads to the main thread before touching Unity objects.
- `DevicePublisher(ingressOrigin, instanceId, tenant).EmitMeasurementsAsync(…)` returns without
  error (202). Remember the device-plane ingress is a **separate** listener from `/api`.

## 4. WebGL build (HIGH RISK — the known-fragile path)

- **HTTP:** a WebGL build should still auth + query via `UnityWebRequestHttpTransport` (browser
  `fetch`). Expect this to work.
- **WebSocket:** the `DeviceChainWebSocket.jslib` + `WebGlWebSocketFactory` poll-based path is
  entirely untested. Validate a live `SubscribeAsync` in a WebGL player. Two known risks:
  1. **`.jslib` linkage / marshalling** — string params, `_malloc`/`_free`, `UTF8ToString`/
     `stringToUTF8` names can drift across Emscripten versions.
  2. **The SDK read loop uses `Task.Run`** (`GraphQlWsClient`), which has no real thread pool under
     WebGL. If subscribe hangs, the fix is likely an SDK follow-up: make the read loop pump-based
     (driven from a MonoBehaviour `Update`) instead of `Task.Run`. Capture findings — this may become
     a small slice-4.1 SDK amendment.

## 5. Report back

Record what passed, what needed pruning/edits, and any SDK change WebGL forced — that becomes the next
slice-4 increment (reconcile-by-`externalId` + twin scaffolding rides on a working transport base).
