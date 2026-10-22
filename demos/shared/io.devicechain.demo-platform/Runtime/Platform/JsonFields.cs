// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Text.Json;

namespace DeviceChain.Demos.Platform
{
    /// <summary>Reads a named property off a platform response: a missing or mistyped property is null, never an exception.</summary>
    public static class JsonFields
    {
        public static string Str(JsonElement o, string name) =>
            o.ValueKind == JsonValueKind.Object && o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;

        public static JsonElement? Obj(JsonElement o, string name) =>
            o.ValueKind == JsonValueKind.Object && o.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Object ? v : (JsonElement?)null;
    }
}
