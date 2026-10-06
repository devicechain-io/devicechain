// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// The one <see cref="JsonTypeInfo"/> the SDK is handed: <see cref="JsonElement"/>, read and
    /// written by hand everywhere else. Its options carry an explicit resolver that knows that one
    /// type and nothing more. Without it, options are frozen with whatever default the runtime
    /// supplies: in the Editor that is the reflection resolver, so everything works; in an IL2CPP
    /// player reflection is off by default and the first request throws ("must specify a
    /// TypeInfoResolver"). A missing resolver therefore passes every Editor test and fails only in
    /// the build, which is why this is the only place the options are made.
    /// </summary>
    public static class PlatformJson
    {
        public static readonly JsonSerializerOptions Options =
            new JsonSerializerOptions { TypeInfoResolver = new ElementOnly() };

        public static readonly JsonTypeInfo<JsonElement> Element =
            (JsonTypeInfo<JsonElement>)Options.GetTypeInfo(typeof(JsonElement));

        sealed class ElementOnly : IJsonTypeInfoResolver
        {
            public JsonTypeInfo GetTypeInfo(Type type, JsonSerializerOptions options) =>
                type == typeof(JsonElement)
                    ? JsonMetadataServices.CreateValueInfo<JsonElement>(options, JsonMetadataServices.JsonElementConverter)
                    : null;
        }
    }
}
