// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Commands;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

public class HttpCommandPullerTests
{
    private sealed class FakeTransport : IHttpTransport
    {
        private readonly Func<HttpTransportRequest, HttpTransportResponse> _responder;
        public List<HttpTransportRequest> Requests { get; } = new();

        public FakeTransport(Func<HttpTransportRequest, HttpTransportResponse> responder) => _responder = responder;

        public Task<HttpTransportResponse> SendAsync(HttpTransportRequest request, CancellationToken cancellationToken)
        {
            Requests.Add(request);
            return Task.FromResult(_responder(request));
        }
    }

    private static HttpCommandPuller Puller(FakeTransport transport) =>
        new(transport, new Uri("https://ingress.local"), "dc", "acme", "dev-1", "tok-abc");

    private static HttpTransportResponse Status(int status, string body = "") =>
        new() { Status = status, Body = Encoding.UTF8.GetBytes(body) };

    [Fact]
    public async Task Pull_sends_a_bearer_GET_to_the_device_commands_path_with_the_wait()
    {
        var transport = new FakeTransport(_ => Status(204));
        IReadOnlyList<PulledCommand> got = await Puller(transport).PullAsync(7);

        Assert.Empty(got);
        HttpTransportRequest sent = Assert.Single(transport.Requests);
        Assert.Equal("GET", sent.Method);
        Assert.Equal("/dc/acme/devices/dev-1/commands", sent.Uri.AbsolutePath);
        Assert.Equal("?wait=7", sent.Uri.Query);
        Assert.Equal("tok-abc", sent.BearerToken);
        Assert.Null(sent.Body);
        // The token is never in the URL, where it would reach an access log.
        Assert.DoesNotContain("tok-abc", sent.Uri.ToString());
    }

    [Fact]
    public async Task Pull_reads_the_commands_the_platform_handed_out()
    {
        const string body = "{\"commands\":[" +
            "{\"token\":\"c1\",\"name\":\"setRelay\",\"payload\":{\"channel\":2,\"on\":true},\"attempt\":1,\"expiresAt\":\"2030-01-01T00:00:00Z\"}," +
            "{\"token\":\"c2\",\"name\":\"blinkLed\",\"deliveryMode\":\"ONEWAY\",\"attempt\":2}]}";
        var transport = new FakeTransport(_ => Status(200, body));

        IReadOnlyList<PulledCommand> got = await Puller(transport).PullAsync();

        Assert.Equal(2, got.Count);
        Assert.Equal("c1", got[0].Token);
        Assert.Equal("setRelay", got[0].Name);
        Assert.Equal(2, got[0].Payload!.Value.GetProperty("channel").GetInt32());
        Assert.Equal(1, got[0].Attempt);
        Assert.Null(got[0].DeliveryMode);
        Assert.Equal("2030-01-01T00:00:00Z", got[0].ExpiresAt);
        Assert.Equal("ONEWAY", got[1].DeliveryMode);
        Assert.Equal(2, got[1].Attempt);
        Assert.Null(got[1].Payload);
        Assert.Contains("?wait=25", transport.Requests[0].Uri.Query);
    }

    [Theory]
    [InlineData(-1)]
    [InlineData(31)]
    public async Task Pull_refuses_a_wait_the_platform_would_not_accept(int wait)
    {
        var transport = new FakeTransport(_ => Status(204));
        await Assert.ThrowsAsync<ArgumentOutOfRangeException>(() => Puller(transport).PullAsync(wait));
        Assert.Empty(transport.Requests);
    }

    [Theory]
    [InlineData(401)]
    [InlineData(403)]
    [InlineData(503)]
    public async Task Pull_surfaces_a_refusal_with_its_status(int status)
    {
        var transport = new FakeTransport(_ => Status(status, "no"));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => Puller(transport).PullAsync(0));
        Assert.Equal(status, ex.Status);
    }

    [Fact]
    public async Task Pull_carries_the_retry_after_of_a_429()
    {
        var transport = new FakeTransport(_ => new HttpTransportResponse { Status = 429, RetryAfter = TimeSpan.FromSeconds(5) });
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => Puller(transport).PullAsync(0));
        Assert.Equal(429, ex.Status);
        Assert.Equal(TimeSpan.FromSeconds(5), ex.RetryAfter);
    }

    [Fact]
    public async Task Acknowledge_posts_to_the_command_ack_path()
    {
        var transport = new FakeTransport(_ => Status(204));
        await Puller(transport).AcknowledgeAsync("c1");

        HttpTransportRequest sent = Assert.Single(transport.Requests);
        Assert.Equal("POST", sent.Method);
        Assert.Equal("/dc/acme/devices/dev-1/commands/c1/ack", sent.Uri.AbsolutePath);
        Assert.Equal("tok-abc", sent.BearerToken);
    }

    [Theory]
    [InlineData(404)]
    [InlineData(409)]
    public async Task Acknowledge_surfaces_a_command_that_is_not_waiting(int status)
    {
        var transport = new FakeTransport(_ => Status(status));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => Puller(transport).AcknowledgeAsync("c1"));
        Assert.Equal(status, ex.Status);
    }

    [Fact]
    public async Task Respond_posts_success_payload_and_error_as_the_published_body()
    {
        var transport = new FakeTransport(_ => Status(204));
        JsonElement payload = JsonDocument.Parse("{\"relay\":\"on\"}").RootElement.Clone();

        await Puller(transport).RespondAsync("c1", true, payload);
        await Puller(transport).RespondAsync("c2", false, error: "actuator jammed");

        Assert.Equal("/dc/acme/devices/dev-1/commands/c1/response", transport.Requests[0].Uri.AbsolutePath);
        Assert.Equal("application/json", transport.Requests[0].ContentType);
        using JsonDocument first = JsonDocument.Parse(transport.Requests[0].Body!);
        Assert.True(first.RootElement.GetProperty("success").GetBoolean());
        Assert.Equal("on", first.RootElement.GetProperty("payload").GetProperty("relay").GetString());
        Assert.False(first.RootElement.TryGetProperty("error", out _));

        using JsonDocument second = JsonDocument.Parse(transport.Requests[1].Body!);
        Assert.False(second.RootElement.GetProperty("success").GetBoolean());
        Assert.Equal("actuator jammed", second.RootElement.GetProperty("error").GetString());
        Assert.False(second.RootElement.TryGetProperty("payload", out _));
    }

    [Fact]
    public async Task A_transport_failure_surfaces_as_the_sdks_request_exception()
    {
        var transport = new FakeTransport(_ => throw new InvalidOperationException("connection refused"));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => Puller(transport).PullAsync(0));
        Assert.Equal(0, ex.Status);
    }

    [Fact]
    public void The_constructor_requires_every_identity_part()
    {
        var transport = new FakeTransport(_ => Status(204));
        var origin = new Uri("https://ingress.local");
        Assert.Throws<ArgumentException>(() => new HttpCommandPuller(transport, origin, "", "acme", "dev-1", "t"));
        Assert.Throws<ArgumentException>(() => new HttpCommandPuller(transport, origin, "dc", "", "dev-1", "t"));
        Assert.Throws<ArgumentException>(() => new HttpCommandPuller(transport, origin, "dc", "acme", "", "t"));
        Assert.Throws<ArgumentException>(() => new HttpCommandPuller(transport, origin, "dc", "acme", "dev-1", ""));
    }
}
