<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Live-SDK smoke (Editor)

`DeviceChainLiveSmoke.cs` drives the real SDK against a live cluster from the Unity Editor — the
reproducible form of VERIFICATION.md item 3. It authenticates, queries devices (over
`UnityWebRequestHttpTransport`), and subscribes to `measurementStream` (over `ClientWebSocketFactory`),
logging to the Console.

**Editor-only, by design.** It uses reflection-based `System.Text.Json`
(`DefaultJsonTypeInfoResolver`), which works in the Mono Editor but is **not** AOT/IL2CPP-safe — so it
is deliberately kept **out** of the AOT-safe `io.devicechain.sdk` package. A real player build supplies
source-generated `JsonTypeInfo` instead.

## Use

1. Have the `io.devicechain.sdk` package in your project (see `../../io.devicechain.sdk/README.md`).
2. Copy `DeviceChainLiveSmoke.cs` into the project's `Assets/`.
3. Add the **Device Chain Live Smoke** component to a GameObject. The fields default to a local kind
   cluster (`http://localhost`, `superuser@devicechain.local` / `devicechain`, tenant `sim-bp`).
4. Press Play. You should see `login → selectTenant → devices → subscribing…`, then live `📈`
   measurement lines once something is emitting into the tenant (e.g. run the buildingpulse sim:
   `backend/sims/dc-simulator/dc-simulator --handshake ~/.devicechain/sims/bp.json`).

Call the SDK from the main thread (this script does — no `ConfigureAwait(false)` in the driver — so
`UnityWebRequest` stays on the main thread).
