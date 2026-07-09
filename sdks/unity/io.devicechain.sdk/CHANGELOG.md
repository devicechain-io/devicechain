<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Changelog

## [0.1.0] — unreleased

Initial Unity plugin scaffolding (ADR-035 sim-subsystem slice 4.2), built on the C# SDK's transport
seam (slice 4.1).

- `DeviceChainRuntime.CreateClient` — platform-selecting bootstrap (UnityWebRequest HTTP everywhere;
  `ClientWebSocketFactory` off-WebGL, `WebGlWebSocketFactory` on WebGL).
- `UnityWebRequestHttpTransport` (`IHttpTransport`).
- `WebGlWebSocketFactory` / `WebGlWebSocketConnection` + `DeviceChainWebSocket.jslib`
  (`IWebSocketFactory`) — WebGL, **unverified** (see `VERIFICATION.md`).
- Spinning Logo smoke-test sample.
