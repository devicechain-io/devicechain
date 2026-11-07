<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Changelog

## [0.1.0] — unreleased

- MQTT device sessions can authenticate with an X.509 client certificate (`MqttClientCertificate`,
  `MqttSessionOptions.ClientCertificate`), through the C# SDK the package builds on. Needs a TLS (`ssl://`)
  broker address and RSA or ECDSA keys; not available on WebGL, which has no MQTT.

Initial Unity plugin scaffolding, built on the C# SDK's transport seam.

- `DeviceChainRuntime.CreateClient` — platform-selecting bootstrap (UnityWebRequest HTTP everywhere;
  `ClientWebSocketFactory` off-WebGL, `WebGlWebSocketFactory` on WebGL).
- `UnityWebRequestHttpTransport` (`IHttpTransport`).
- `WebGlWebSocketFactory` / `WebGlWebSocketConnection` + `DeviceChainWebSocket.jslib`
  (`IWebSocketFactory`) — WebGL, **unverified** (see `VERIFICATION.md` item 4).
- Spinning Logo smoke-test sample.
- Verified live on **Unity 6000.5.3f1 + URP**: package compiles, smoke test renders, and the full
  non-WebGL SDK stack (auth + query + live subscription) runs against a real cluster (VERIFICATION.md
  items 0–3). WebGL remains an open follow-up.
