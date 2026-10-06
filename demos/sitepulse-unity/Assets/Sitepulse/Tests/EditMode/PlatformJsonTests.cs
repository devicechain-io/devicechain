// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Text.Json;
using DeviceChain.Sitepulse.Platform;
using NUnit.Framework;

namespace DeviceChain.Sitepulse.Tests
{
    // The Editor has reflection on, so a resolver-less options object works here and fails only in
    // an IL2CPP player. These tests therefore pin the resolver itself, not just a round trip.
    public class PlatformJsonTests
    {
        [Test]
        public void OptionsCarryTheirOwnResolverNotTheReflectionDefault()
        {
            Assert.That(PlatformJson.Options.TypeInfoResolver, Is.Not.Null);
            Assert.That(PlatformJson.Options.TypeInfoResolver, Is.Not.InstanceOf<System.Text.Json.Serialization.Metadata.DefaultJsonTypeInfoResolver>());
            Assert.That(PlatformJson.Options.IsReadOnly, Is.True);
        }

        [Test]
        public void OnlyJsonElementIsKnown()
        {
            Assert.That(PlatformJson.Element.Type, Is.EqualTo(typeof(JsonElement)));
            Assert.Throws<NotSupportedException>(() => PlatformJson.Options.GetTypeInfo(typeof(string)));
        }

        [Test]
        public void ElementRoundTrips()
        {
            using var doc = JsonDocument.Parse("{\"ids\":[\"SP-HL-0006\"],\"n\":1}");
            var text = JsonSerializer.Serialize(doc.RootElement, PlatformJson.Element);
            var back = JsonSerializer.Deserialize(text, PlatformJson.Element);
            Assert.That(back.GetProperty("ids")[0].GetString(), Is.EqualTo("SP-HL-0006"));
            Assert.That(back.GetProperty("n").GetInt32(), Is.EqualTo(1));
        }
    }
}
