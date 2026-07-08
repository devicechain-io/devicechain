// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Text.Json;
using System.Text.Json.Serialization;
using DeviceChain.Sdk.Auth;
using DeviceChain.Sdk.Ingest;

namespace DeviceChain.Sdk.Json;

// SdkJson is the SOURCE-GENERATED serializer context for every wire type the SDK owns
// (ADR-035 slice 3 AOT/IL2CPP rule: no reflection-based (de)serialization, no
// Reflection.Emit — the generator emits the metadata at compile time so the same assembly
// runs under Unity's IL2CPP). Integrators serialize THEIR own query types through their own
// [JsonSerializable] context and hand the SDK the JsonTypeInfo (see GraphQlClient.SendAsync).
[JsonSourceGenerationOptions(
    PropertyNamingPolicy = JsonKnownNamingPolicy.CamelCase,
    DefaultIgnoreCondition = JsonIgnoreCondition.WhenWritingNull)]
[JsonSerializable(typeof(GraphQlError))]
[JsonSerializable(typeof(GraphQlError[]))]
[JsonSerializable(typeof(EmptyVariables))]
// Auth
[JsonSerializable(typeof(LoginVariables))]
[JsonSerializable(typeof(LoginData))]
[JsonSerializable(typeof(SelectTenantVariables))]
[JsonSerializable(typeof(SelectTenantData))]
[JsonSerializable(typeof(RefreshVariables))]
[JsonSerializable(typeof(RefreshData))]
[JsonSerializable(typeof(IdentityAuth))]
[JsonSerializable(typeof(AuthToken))]
// Ingest
[JsonSerializable(typeof(MeasurementEvent))]
internal partial class SdkJson : JsonSerializerContext
{
}

/// <summary>Marker for a GraphQL operation that takes no variables (serializes to <c>{}</c>).</summary>
public sealed class EmptyVariables
{
    /// <summary>The shared instance — a variable-less op passes this.</summary>
    public static readonly EmptyVariables Value = new();
}
