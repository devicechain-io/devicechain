// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Threading.Tasks;
using DeviceChain.Sdk.Ingest;
using Xunit;

namespace DeviceChain.Sdk.Tests;

public class DeviceEventPublisherTests
{
    private static DeviceEventPublisher Publisher(StubHandler handler)
    {
        var http = new HttpClient(handler);
        return new DeviceEventPublisher(http, new Uri("https://ingress.local"), "dc", "acme");
    }

    [Fact]
    public async Task Emits_the_device_plane_JsonEvent_shape_to_the_instance_tenant_path()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.Accepted, ""));
        DeviceEventPublisher pub = Publisher(handler);

        await pub.EmitMeasurementsAsync(
            "dev-1", "cred-9",
            new Dictionary<string, double> { ["temperature"] = 21.5 },
            new DateTimeOffset(2026, 7, 8, 12, 0, 0, TimeSpan.Zero));

        StubHandler.Call call = Assert.Single(handler.Calls);
        Assert.Equal("/dc/acme/events", call.Path);
        Assert.Contains("\"device\":\"dev-1\"", call.Body);
        Assert.Contains("\"eventType\":\"Measurement\"", call.Body);
        Assert.Contains("\"credentialType\":\"ACCESS_TOKEN\"", call.Body);
        Assert.Contains("\"credentialId\":\"cred-9\"", call.Body);
        Assert.Contains("\"temperature\":\"21.5\"", call.Body); // invariant string value
        Assert.Contains("\"occurredTime\":\"2026-07-08T12:00:00Z\"", call.Body);
        // No credential SECRET is sent (a token device presents type+id only).
        Assert.DoesNotContain("credentialSecret", call.Body);
    }

    [Fact]
    public async Task Throws_when_the_ingress_rejects_the_event()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.TooManyRequests, "rate limited"));
        DeviceEventPublisher pub = Publisher(handler);

        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() =>
            pub.EmitMeasurementsAsync("dev-1", "cred-9", new Dictionary<string, double> { ["t"] = 1 }));
        Assert.Equal(429, ex.Status);
    }
}
