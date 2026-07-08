// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace DeviceChain.Sdk.Tests;

/// <summary>A configurable HttpMessageHandler that records requests and returns scripted replies.</summary>
internal sealed class StubHandler : HttpMessageHandler
{
    public sealed record Call(string Method, string Path, string Body, string? Authorization);

    private readonly Func<HttpRequestMessage, string, (HttpStatusCode Status, string Json)> _responder;
    public List<Call> Calls { get; } = new();

    public StubHandler(Func<HttpRequestMessage, string, (HttpStatusCode, string)> responder)
    {
        _responder = responder;
    }

    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        string body = request.Content is null ? "" : await request.Content.ReadAsStringAsync().ConfigureAwait(false);
        Calls.Add(new Call(
            request.Method.Method,
            request.RequestUri!.AbsolutePath,
            body,
            request.Headers.Authorization?.ToString()));

        (HttpStatusCode status, string json) = _responder(request, body);
        return new HttpResponseMessage(status)
        {
            Content = new StringContent(json, Encoding.UTF8, "application/json"),
        };
    }
}
