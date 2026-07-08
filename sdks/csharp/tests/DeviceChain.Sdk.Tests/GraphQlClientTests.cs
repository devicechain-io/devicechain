// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Net;
using System.Net.Http;
using System.Text.Json;
using System.Text.Json.Serialization;
using System.Threading.Tasks;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Json;
using Xunit;

namespace DeviceChain.Sdk.Tests;

// A test-local source-gen context — proves the integrator path: the SDK serializes a caller's
// own types through the caller's JsonTypeInfo, never reflecting over them (AOT-safe).
internal sealed class Thing
{
    public string Name { get; set; } = "";
}

// A numeric-typed payload used to exercise a deserialization MISMATCH in the ws tests.
internal sealed class Num
{
    public int Value { get; set; }
}

[JsonSourceGenerationOptions(PropertyNamingPolicy = JsonKnownNamingPolicy.CamelCase)]
[JsonSerializable(typeof(Thing))]
[JsonSerializable(typeof(Num))]
[JsonSerializable(typeof(EmptyVariables))]
internal partial class TestJson : JsonSerializerContext
{
}

public class GraphQlClientTests
{
    private static GraphQlClient Client(StubHandler handler, TokenProvider? token = null)
    {
        var http = new HttpClient(handler);
        return new GraphQlClient(http, new Uri("https://test.local"), token);
    }

    private static Task<Thing> Send(GraphQlClient client, RequestOptions? options = null) =>
        client.SendAsync(Area.DeviceManagement, "query{thing{name}}", EmptyVariables.Value,
            TestJson.Default.EmptyVariables, TestJson.Default.Thing, options);

    [Fact]
    public async Task Parses_typed_data_and_targets_the_area_path()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.OK, "{\"data\":{\"name\":\"widget\"}}"));
        Thing thing = await Send(Client(handler));
        Assert.Equal("widget", thing.Name);
        Assert.Equal("/api/device-management/graphql", Assert.Single(handler.Calls).Path);
    }

    [Fact]
    public async Task Throws_on_a_graphql_errors_payload()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.OK, "{\"errors\":[{\"message\":\"boom\"}]}"));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => Send(Client(handler)));
        Assert.Equal("boom", ex.Message);
        Assert.Equal("boom", Assert.Single(ex.Errors).Message);
    }

    [Fact]
    public async Task Throws_with_status_on_a_non_2xx_response()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.InternalServerError, "nope"));
        GraphQlRequestException ex = await Assert.ThrowsAsync<GraphQlRequestException>(() => Send(Client(handler)));
        Assert.Equal(500, ex.Status);
    }

    [Fact]
    public async Task Attaches_the_bearer_token_unless_anonymous()
    {
        var handler = new StubHandler((_, _) => (HttpStatusCode.OK, "{\"data\":{\"name\":\"x\"}}"));
        var client = Client(handler, _ => new ValueTask<string?>("tok-123"));

        await Send(client);
        Assert.Equal("Bearer tok-123", handler.Calls[^1].Authorization);

        await Send(client, new RequestOptions { Anonymous = true });
        Assert.Null(handler.Calls[^1].Authorization);
    }
}
