<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# DeviceChain .NET SDK

The public C# client SDK for the DeviceChain platform — the Unity/.NET enabler (ADR-035
sim-subsystem slice 3). It wraps the platform's documented wire seams so a .NET host (and,
via the slice-4 Unity UPM plugin, a digital twin) authenticates, reads/queries, subscribes to
live telemetry, and emits device-plane telemetry — as an **untrusted external client**, never
the admin surface.

**AOT / IL2CPP-safe by construction:** serialization is source-generated
(`System.Text.Json` `[JsonSerializable]`), there is no `Reflection.Emit` or runtime codegen,
and the library builds with `TreatWarningsAsErrors` + `IsAotCompatible`. It multi-targets
`netstandard2.1` (Unity's IL2CPP-consumable target) and `net8.0`.

## What's here

| Piece | Type | Seam |
|-------|------|------|
| Auth state machine | `Auth.AuthSession` | `login → selectTenant → refresh` (ADR-033), with proactive near-expiry refresh; the `TokenProvider` a client plugs in |
| GraphQL over HTTP | `GraphQlClient` | typed query/mutate against `/api/{area}/graphql`; caller supplies its own `JsonTypeInfo` (AOT-safe) |
| Live subscriptions | `Subscriptions.GraphQlWsClient` | `graphql-transport-ws`, one multiplexed socket per area; token in `connection_init` |
| Device-plane emit | `Ingest.DeviceEventPublisher` | telemetry over `POST /{instanceId}/{tenant}/events` (the `JsonEvent` shape, in-body credential per ADR-014/025) |
| Facade | `DeviceChainClient` | wires all of the above against one origin |

## Usage

```csharp
await using var client = new DeviceChainClient(new Uri("https://demo.devicechain.io"));

await client.LoginAsync("me@example.com", "…");
await client.SelectTenantAsync("acme");

// Query (the caller's own source-gen JsonTypeInfo keeps it AOT-safe):
var data = await client.Gql.SendAsync(
    Area.DeviceManagement,
    "query{devices(criteria:{pageNumber:1,pageSize:10}){results{token name}}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.DevicesData);

// Subscribe to live telemetry:
await foreach (var evt in client.Subscriptions(Area.EventManagement).SubscribeAsync(
    "subscription{measurementStream{deviceToken name value unit}}",
    EmptyVariables.Value, MyJson.Default.EmptyVariables, MyJson.Default.MeasurementStreamData, ct))
{
    // …drive a chart / a twin transform
}

// Emit device-plane telemetry (an interactive twin):
var publisher = client.DevicePublisher(instanceId: "dc", tenant: "acme");
await publisher.EmitMeasurementsAsync("car-42", credentialId,
    new Dictionary<string, double> { ["speed"] = 55.0 });
```

## Build & test

```bash
cd sdks/csharp
dotnet build -c Release   # both TFMs; warnings are errors
dotnet test  -c Release
```

## Not yet (documented follow-ups)

- Subscription **auto-reconnect** on a dropped socket (a consumer re-subscribes today).
- Provisioning mutation helpers (the Go `dcctl sim` runner provisions; a twin is read + subscribe + emit).
- Native-AOT `PublishAot` validation beyond the analyzer (needs the ILCompiler toolchain in CI).
