<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# AGENTS.md: DeviceChain .NET SDK

Notes for an AI coding agent working in, or building on, this SDK. Read `README.md` for the
piece-by-piece map and usage.

## What it is

A public client for the platform's documented wire seams: auth, GraphQL over HTTP, live
subscriptions, and device-plane telemetry emit. It is an **untrusted external client**: it
never calls an admin surface and holds no service credential.

## Rules that save time

- **AOT and IL2CPP safe.** Serialization is source-generated (`System.Text.Json`
  `[JsonSerializable]`). Do not add reflection-based serialization, `Reflection.Emit`,
  or any API the trimmer cannot see. The library builds with warnings as errors and
  `IsAotCompatible`; keep it that way.
- **Targets.** `netstandard2.1` (what Unity consumes) and `net8.0`. Do not use an API that
  `netstandard2.1` lacks without a guard.
- **The origin is always the caller's.** The SDK has no default host. Examples use
  `https://devicechain.example.com`; use your own instance address, never invent one.
- **Public API is covered by the versioning policy.** Add and deprecate with
  `[Obsolete]`; do not remove or rename in a minor release.
- **Transport is pluggable** (`IHttpTransport`, `IWebSocketFactory`) so the SDK runs where
  `HttpClient` or `ClientWebSocket` do not (Unity WebGL). Test against the seams, not the
  network.
- Every source file begins with the two-line SPDX header used across the repository.

## Commands

If `dotnet` is not found, an SDK installed by the install script lives in `~/.dotnet`:

```bash
export PATH="$HOME/.dotnet:$PATH"
cd sdks/csharp && dotnet build -c Release && dotnet test -c Release
```
