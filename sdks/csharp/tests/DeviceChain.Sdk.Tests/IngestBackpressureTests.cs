// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net;
using System.Net.Http;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Ingest;
using DeviceChain.Sdk.Transport;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// Device ingest answers a backpressure refusal with 503 and a Retry-After, meaning the event was
// certainly not stored and can be sent again after the delay. A 503 without one is a failed
// publish. The SDK has to carry that header to the caller, or the two cannot be told apart.
public class IngestBackpressureTests
{
    private sealed class FixedResponse : HttpMessageHandler
    {
        private readonly HttpStatusCode _status;
        private readonly string? _retryAfter;

        public FixedResponse(HttpStatusCode status, string? retryAfter)
        {
            _status = status;
            _retryAfter = retryAfter;
        }

        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            var response = new HttpResponseMessage(_status) { Content = new StringContent("refused") };
            if (_retryAfter is not null)
            {
                response.Headers.TryAddWithoutValidation("Retry-After", _retryAfter);
            }
            return Task.FromResult(response);
        }
    }

    private static async Task<GraphQlRequestException> SendExpectingRefusal(HttpStatusCode status, string? retryAfter)
    {
        var carrier = new HttpDeviceEventCarrier(
            new HttpClientTransport(new HttpClient(new FixedResponse(status, retryAfter))),
            new Uri("http://ingest.example"), "inst", "acme");
        return await Assert.ThrowsAsync<GraphQlRequestException>(
            () => carrier.SendAsync("dev-1", new byte[] { (byte)'{', (byte)'}' }, CancellationToken.None));
    }

    [Fact]
    public async Task ABackpressure503CarriesItsRetryAfter()
    {
        GraphQlRequestException ex = await SendExpectingRefusal(HttpStatusCode.ServiceUnavailable, "10");
        Assert.Equal(503, ex.Status);
        Assert.Equal(TimeSpan.FromSeconds(10), ex.RetryAfter);
    }

    // Negative control: a failed publish is a bare 503, and must not read as a refusal.
    [Fact]
    public async Task ABare503HasNoRetryAfter()
    {
        GraphQlRequestException ex = await SendExpectingRefusal(HttpStatusCode.ServiceUnavailable, null);
        Assert.Equal(503, ex.Status);
        Assert.Null(ex.RetryAfter);
    }

    [Fact]
    public void RetryAfterReadsSecondsAndDates()
    {
        var now = new DateTimeOffset(2026, 9, 28, 12, 0, 0, TimeSpan.Zero);
        Assert.Equal(TimeSpan.FromSeconds(10), HttpRetryAfter.Parse("10", now));
        Assert.Equal(TimeSpan.FromSeconds(30), HttpRetryAfter.Parse("Mon, 28 Sep 2026 12:00:30 GMT", now));
        Assert.Equal(TimeSpan.Zero, HttpRetryAfter.Parse("Mon, 28 Sep 2026 11:00:00 GMT", now));
        Assert.Null(HttpRetryAfter.Parse(null, now));
        Assert.Null(HttpRetryAfter.Parse("soon", now));
        Assert.Null(HttpRetryAfter.Parse("-5", now));
    }
}
