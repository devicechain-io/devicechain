<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain SDKs — Unity

`io.devicechain.sdk/` is the DeviceChain **Unity UPM package** (ADR-035 sim-subsystem slice 4) — the
Unity/digital-twin layer over the [C# SDK](../csharp). It supplies Unity-appropriate transports behind
the SDK's transport seam and a platform-selecting bootstrap.

- **Package:** [`io.devicechain.sdk/`](./io.devicechain.sdk) — see its `README.md`.
- **Stage the SDK before opening in Unity:** `./stage-sdk.sh` (builds the C# SDK and copies its
  assembly closure into the package's gitignored `Runtime/Plugins/`).
- **Tier-1 compile check (no Editor):** `tools/UnityCompileCheck/compile-check.sh` compiles the
  package's C# against real UnityEngine reference assemblies in both platform branches (desktop +
  WebGL). Enforced in CI as `unity-compile-check`. Catches wrong-API / `#if UNITY_WEBGL` typos.
- **Verify in a Unity Editor:** `io.devicechain.sdk/VERIFICATION.md` — tier-1 does not run the code,
  compile the `.jslib`, or build for WebGL, so the runtime paths (the WebGL WebSocket especially) must
  still be validated on a Unity machine.

Distribution (OpenUPM vs git-URL vs Asset Store) is still open — see the sim-subsystem contract §7.
