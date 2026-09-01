<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0

🔴 THIS FILE IS PUBLISHED. It is the body of the DeviceChain.Sdk listing on
nuget.org, so it is written for a reader who has never seen this repository and
cannot follow a reference into it. No ADR citations, no internal shorthand, no
links to private planning docs. `hack/check-docs-adr-refs.sh` enforces the ADR
half of that, because a csproj's PackageReadmeFile is registry-rendered prose
exactly as its <Description> is.

The repository-facing README, which is free to cite ADRs, is ../../README.md.
The two are deliberately separate files for that reason — do not merge them.
-->

# DeviceChain .NET SDK

The public C# client SDK for [DeviceChain](https://devicechain.io), an open source
IoT platform. It wraps the platform's documented wire seams so a .NET host — or a
Unity digital twin — can authenticate, query, subscribe to live telemetry, and emit
device-plane telemetry and commands.

The SDK is an **untrusted external client**. It speaks the same public surface any
third-party application does, and never the platform's administrative one.

## Install

```
dotnet add package DeviceChain.Sdk
```

## Quick start

```csharp
await using var client = new DeviceChainClient(new Uri("https://demo.devicechain.io"));

await client.LoginAsync("me@example.com", "…");
await client.SelectTenantAsync("acme");

// Query. The caller supplies its own source-generated JsonTypeInfo, which is what
// keeps the whole path AOT-safe:
var data = await client.Gql.SendAsync(
    Area.DeviceManagement,
    "query{devices(criteria:{pageNumber:1,pageSize:10}){results{token name}}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.DevicesData);

// Subscribe to live telemetry over graphql-transport-ws:
await foreach (var evt in client.Subscriptions(Area.EventManagement).SubscribeAsync(
    "subscription{measurementStream{deviceToken name value unit}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.MeasurementStreamData, ct))
{
    // …drive a chart, or a Unity twin transform
}

// Emit device-plane telemetry. The device-plane ingress is a SEPARATE listener from
// the GraphQL origin, so its origin is passed explicitly:
var publisher = client.DevicePublisher(
    ingressOrigin: new Uri("https://ingress.demo.devicechain.io"),
    instanceId: "dc", tenant: "acme");

await publisher.EmitMeasurementsAsync("car-42", credentialId,
    new Dictionary<string, double> { ["speed"] = 55.0 });
```

## What's in it

| Piece | Type |
|-------|------|
| Auth state machine | `Auth.AuthSession` — login, tenant selection, and proactive near-expiry refresh |
| GraphQL over HTTP | `GraphQlClient` — typed query and mutation against a chosen functional area |
| Live subscriptions | `Subscriptions.GraphQlWsClient` — `graphql-transport-ws`, one multiplexed socket per area |
| Device-plane emit | `Ingest.DeviceEventPublisher` — measurements and locations over HTTP or MQTT |
| Transport seam | `Transport.IHttpTransport` / `Transport.IWebSocketFactory` — pluggable, so the SDK runs where `HttpClient` and `ClientWebSocket` do not (Unity WebGL) |
| Facade | `DeviceChainClient` — wires all of the above against one origin |

## Targets and AOT

Multi-targets `netstandard2.1` (the target Unity's IL2CPP consumes) and `net8.0`.

Serialization is source-generated (`System.Text.Json` `[JsonSerializable]`); there is
no `Reflection.Emit` and no runtime code generation anywhere in the library, and it
builds with `IsAotCompatible` and warnings-as-errors. That is what makes the same
assembly safe under IL2CPP and NativeAOT as under CoreCLR.

The MQTT dependency is pinned to the MQTTnet 4.x line on purpose: MQTTnet 5 dropped
`netstandard2.1`, which would silently remove the Unity half of this SDK.

## Links

- Documentation — https://docs.devicechain.io
- Source and issues — https://github.com/devicechain-io/devicechain

Licensed under the Apache License 2.0.
