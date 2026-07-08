// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.IO;
using System.Linq;
using System.Net.WebSockets;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Json;
using DeviceChain.Sdk.Subscriptions;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Http;
using Xunit;

namespace DeviceChain.Sdk.Tests;

public class GraphQlWsClientTests
{
    // Stands up a loopback graphql-transport-ws server that does the handshake, then hands the
    // subscription id to `script` (which sends whatever frames the test needs). Returns the app +
    // the ws:// URL + a task capturing the connection_init frame.
    private static async Task<(WebApplication App, Uri WsUri, TaskCompletionSource<string> Init)> StartServer(
        Func<WebSocket, string, Task> script)
    {
        var init = new TaskCompletionSource<string>();
        WebApplication app = WebApplication.CreateBuilder().Build();
        app.Urls.Add("http://127.0.0.1:0");
        app.UseWebSockets();
        app.Map("/api/event-management/graphql", async ctx =>
        {
            if (!ctx.WebSockets.IsWebSocketRequest)
            {
                ctx.Response.StatusCode = 400;
                return;
            }
            using WebSocket ws = await ctx.WebSockets.AcceptWebSocketAsync("graphql-transport-ws");
            init.TrySetResult(await ReceiveText(ws));
            await SendText(ws, "{\"type\":\"connection_ack\"}");
            string subscribe = await ReceiveText(ws);
            string id = JsonDocument.Parse(subscribe).RootElement.GetProperty("id").GetString()!;
            await script(ws, id);
            try { await ReceiveText(ws); } catch { /* client closed */ }
        });
        await app.StartAsync();
        var wsUri = new Uri(app.Urls.First().Replace("http://", "ws://") + "/api/event-management/graphql");
        return (app, wsUri, init);
    }

    [Fact]
    public async Task Subscribe_negotiates_the_protocol_and_yields_next_payloads_until_complete()
    {
        (WebApplication app, Uri wsUri, TaskCompletionSource<string> init) = await StartServer(async (ws, id) =>
        {
            await SendText(ws, $"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"a\"}}}}}}");
            await SendText(ws, $"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"name\":\"b\"}}}}}}");
            await SendText(ws, $"{{\"id\":\"{id}\",\"type\":\"complete\"}}");
        });
        try
        {
            await using var client = new GraphQlWsClient(wsUri, _ => new ValueTask<string?>("tok-xyz"));
            var names = new List<string>();
            await foreach (Thing thing in client.SubscribeAsync(
                "subscription{x}", EmptyVariables.Value, TestJson.Default.EmptyVariables, TestJson.Default.Thing))
            {
                names.Add(thing.Name);
            }

            Assert.Equal(new[] { "a", "b" }, names);
            string initFrame = await init.Task;
            Assert.Contains("connection_init", initFrame);
            Assert.Contains("Bearer tok-xyz", initFrame); // token in connection_init, not a query param
        }
        finally
        {
            await app.StopAsync();
            await app.DisposeAsync();
        }
    }

    [Fact]
    public async Task An_error_frame_throws_from_the_stream()
    {
        (WebApplication app, Uri wsUri, _) = await StartServer(async (ws, id) =>
            await SendText(ws, $"{{\"id\":\"{id}\",\"type\":\"error\",\"payload\":[{{\"message\":\"bad subscription\"}}]}}"));
        try
        {
            await using var client = new GraphQlWsClient(wsUri);
            GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(async () =>
            {
                await foreach (Thing _ in client.SubscribeAsync(
                    "subscription{x}", EmptyVariables.Value, TestJson.Default.EmptyVariables, TestJson.Default.Thing))
                {
                }
            });
            Assert.Equal("bad subscription", ex.Message);
        }
        finally
        {
            await app.StopAsync();
            await app.DisposeAsync();
        }
    }

    [Fact]
    public async Task A_payload_that_does_not_match_the_type_fails_only_that_stream_without_hanging()
    {
        // Value is an int, the server sends a string — deserialization must fail the SUBSCRIPTION
        // (throw from the stream), not silently hang or unwind the whole client.
        (WebApplication app, Uri wsUri, _) = await StartServer(async (ws, id) =>
            await SendText(ws, $"{{\"id\":\"{id}\",\"type\":\"next\",\"payload\":{{\"data\":{{\"value\":\"not-a-number\"}}}}}}"));
        try
        {
            await using var client = new GraphQlWsClient(wsUri);
            using var cts = new CancellationTokenSource(TimeSpan.FromSeconds(10));
            await Assert.ThrowsAnyAsync<Exception>(async () =>
            {
                await foreach (Num _ in client.SubscribeAsync(
                    "subscription{x}", EmptyVariables.Value, TestJson.Default.EmptyVariables, TestJson.Default.Num, cts.Token))
                {
                }
            });
            Assert.False(cts.IsCancellationRequested, "the stream should have faulted, not hung until the timeout");
        }
        finally
        {
            await app.StopAsync();
            await app.DisposeAsync();
        }
    }

    private static async Task SendText(WebSocket ws, string text)
    {
        byte[] bytes = Encoding.UTF8.GetBytes(text);
        await ws.SendAsync(new ArraySegment<byte>(bytes), WebSocketMessageType.Text, true, CancellationToken.None);
    }

    private static async Task<string> ReceiveText(WebSocket ws)
    {
        var buffer = new byte[4096];
        using var ms = new MemoryStream();
        WebSocketReceiveResult result;
        do
        {
            result = await ws.ReceiveAsync(new ArraySegment<byte>(buffer), CancellationToken.None);
            if (result.MessageType == WebSocketMessageType.Close)
            {
                throw new IOException("closed");
            }
            ms.Write(buffer, 0, result.Count);
        }
        while (!result.EndOfMessage);
        return Encoding.UTF8.GetString(ms.ToArray());
    }
}
