// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Globalization;
using System.IO;
using System.Text;
using System.Text.Json;
using System.Text.Json.Serialization.Metadata;
using System.Threading;
using System.Threading.Channels;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Subscriptions;

/// <summary>
/// A GraphQL subscription client over the <c>graphql-transport-ws</c> protocol (ADR-037) — the
/// live half of the client wire, the same surface dashboards use. ONE WebSocket per client,
/// multiplexing every subscription over it: a per-connection background read loop dispatches
/// server frames to per-operation channels by id. The token is resolved on connect via the
/// <see cref="TokenProvider"/> and carried in <c>connection_init</c> (never a query param).
///
/// Each socket generation owns its own operation map, so a dropped socket fails only ITS
/// operations — a caller that re-subscribes gets a fresh connection whose ops are untouched by
/// the old generation's teardown. Per-operation deserialization is contained: one subscription's
/// bad payload fails only that subscription, never the read loop. Auto-reconnect of a dropped
/// socket is a documented follow-up (a consumer re-subscribes today).
/// </summary>
public sealed class GraphQlWsClient : IAsyncDisposable
{
    private const string SubProtocol = "graphql-transport-ws";
    // Per-operation buffer bound: a slow consumer drops the OLDEST buffered events rather than
    // growing without bound (latest-wins — right for live telemetry driving a chart/twin).
    private const int OperationBufferCapacity = 1024;

    private readonly IWebSocketFactory _webSocketFactory;
    private readonly Uri _endpoint;
    private readonly TokenProvider? _tokenProvider;
    private readonly SemaphoreSlim _connectLock = new(1, 1);
    private readonly CancellationTokenSource _disposedCts = new();

    private Connection? _connection;
    private volatile bool _disposed;
    private int _nextId;
    private int _generation;

    /// <param name="webSocketFactory">Creates the underlying socket (default <see cref="ClientWebSocketFactory"/>; Unity WebGL injects its own).</param>
    /// <param name="endpoint">The absolute ws(s):// URL of one area's GraphQL endpoint.</param>
    /// <param name="tokenProvider">Resolves the Bearer token for connection_init; null = anonymous.</param>
    public GraphQlWsClient(IWebSocketFactory webSocketFactory, Uri endpoint, TokenProvider? tokenProvider = null)
    {
        _webSocketFactory = webSocketFactory ?? throw new ArgumentNullException(nameof(webSocketFactory));
        _endpoint = endpoint ?? throw new ArgumentNullException(nameof(endpoint));
        _tokenProvider = tokenProvider;
    }

    /// <summary>Convenience overload for the plain-.NET path: uses the default <see cref="ClientWebSocketFactory"/>.</summary>
    /// <param name="endpoint">The absolute ws(s):// URL of one area's GraphQL endpoint.</param>
    /// <param name="tokenProvider">Resolves the Bearer token for connection_init; null = anonymous.</param>
    public GraphQlWsClient(Uri endpoint, TokenProvider? tokenProvider = null)
        : this(new ClientWebSocketFactory(), endpoint, tokenProvider)
    {
    }

    /// <summary>
    /// Opens a subscription and yields each <c>next</c> payload's typed data until the server
    /// completes it, the caller stops enumerating, or <paramref name="cancellationToken"/> fires.
    /// A GraphQL <c>error</c> frame (or a dropped socket) throws <see cref="GraphQlRequestException"/>
    /// from the stream.
    /// </summary>
    public async IAsyncEnumerable<TData> SubscribeAsync<TVars, TData>(
        string query,
        TVars variables,
        JsonTypeInfo<TVars> variablesInfo,
        JsonTypeInfo<TData> dataInfo,
        [System.Runtime.CompilerServices.EnumeratorCancellation] CancellationToken cancellationToken = default)
    {
        ThrowIfDisposed();
        Connection conn = await EnsureConnectedAsync(cancellationToken).ConfigureAwait(false);

        string id = Interlocked.Increment(ref _nextId).ToString(CultureInfo.InvariantCulture);
        var op = new Operation<TData>(dataInfo);
        conn.Operations[id] = op;

        try
        {
            await SendSubscribeAsync(conn, id, query, variables, variablesInfo, cancellationToken).ConfigureAwait(false);
        }
        catch
        {
            conn.Operations.TryRemove(id, out _);
            throw;
        }

        try
        {
            while (await op.Reader.WaitToReadAsync(cancellationToken).ConfigureAwait(false))
            {
                while (op.Reader.TryRead(out TData? item))
                {
                    yield return item!;
                }
            }
        }
        finally
        {
            conn.Operations.TryRemove(id, out _);
            await TryCompleteAsync(conn, id).ConfigureAwait(false);
        }
    }

    private async Task<Connection> EnsureConnectedAsync(CancellationToken cancellationToken)
    {
        Connection? existing = _connection;
        if (IsLive(existing))
        {
            return existing!;
        }
        await _connectLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            ThrowIfDisposed();
            existing = _connection;
            if (IsLive(existing))
            {
                return existing!;
            }

            IWebSocketConnection socket = _webSocketFactory.Create();
            try
            {
                await socket.ConnectAsync(_endpoint, SubProtocol, cancellationToken).ConfigureAwait(false);
                await SendConnectionInitAsync(socket, cancellationToken).ConfigureAwait(false);
                await AwaitConnectionAckAsync(socket, cancellationToken).ConfigureAwait(false);
            }
            catch
            {
                socket.Dispose(); // never leak a connected socket when the handshake fails
                throw;
            }

            var conn = new Connection(socket, Interlocked.Increment(ref _generation));
            conn.ReadLoop = Task.Run(() => ReadLoopAsync(conn, _disposedCts.Token));
            _connection = conn;
            return conn;
        }
        finally
        {
            _connectLock.Release();
        }
    }

    // A connection is usable only while its socket is open AND its read loop is still running —
    // a loop that exited (a bad frame, a server close) with the socket not yet observably closed
    // must not be reused, or a subscribe would send into a socket nobody reads (a silent hang).
    private static bool IsLive(Connection? conn) =>
        conn is { Socket.IsOpen: true } && !conn.ReadLoop.IsCompleted;

    private async Task SendConnectionInitAsync(IWebSocketConnection socket, CancellationToken cancellationToken)
    {
        string? token = _tokenProvider is null ? null : await _tokenProvider(cancellationToken).ConfigureAwait(false);
        using var ms = new MemoryStream();
        using (var w = new Utf8JsonWriter(ms))
        {
            w.WriteStartObject();
            w.WriteString("type", "connection_init");
            if (!string.IsNullOrEmpty(token))
            {
                w.WritePropertyName("payload");
                w.WriteStartObject();
                w.WriteString("Authorization", $"Bearer {token}");
                w.WriteEndObject();
            }
            w.WriteEndObject();
        }
        // No read loop yet + single caller under _connectLock, so a lock-free send is safe here.
        await socket.SendTextAsync(ms.ToArray(), cancellationToken).ConfigureAwait(false);
    }

    private async Task AwaitConnectionAckAsync(IWebSocketConnection socket, CancellationToken cancellationToken)
    {
        while (true)
        {
            string message = await ReceiveTextAsync(socket, cancellationToken).ConfigureAwait(false);
            using var doc = JsonDocument.Parse(message);
            string type = TypeOf(doc.RootElement);
            switch (type)
            {
                case "connection_ack":
                    return;
                case "ping":
                    await socket.SendTextAsync(PongFrame, cancellationToken).ConfigureAwait(false);
                    break;
                default:
                    throw new GraphQlRequestException($"expected connection_ack, got '{type}'");
            }
        }
    }

    private async Task SendSubscribeAsync<TVars>(
        Connection conn, string id, string query, TVars variables, JsonTypeInfo<TVars> variablesInfo, CancellationToken cancellationToken)
    {
        using var ms = new MemoryStream();
        using (var w = new Utf8JsonWriter(ms))
        {
            w.WriteStartObject();
            w.WriteString("id", id);
            w.WriteString("type", "subscribe");
            w.WritePropertyName("payload");
            w.WriteStartObject();
            w.WriteString("query", query);
            w.WritePropertyName("variables");
            JsonSerializer.Serialize(w, variables, variablesInfo);
            w.WriteEndObject();
            w.WriteEndObject();
        }
        await LockedSendAsync(conn, ms.ToArray(), cancellationToken).ConfigureAwait(false);
    }

    private async Task ReadLoopAsync(Connection conn, CancellationToken cancellationToken)
    {
        Exception failure = new GraphQlRequestException("subscription socket closed");
        try
        {
            while (conn.Socket.IsOpen && !cancellationToken.IsCancellationRequested)
            {
                string message = await ReceiveTextAsync(conn.Socket, cancellationToken).ConfigureAwait(false);
                Dispatch(conn, message, cancellationToken);
            }
        }
        catch (OperationCanceledException)
        {
            failure = new GraphQlRequestException("subscription client disposed");
        }
        catch (Exception ex)
        {
            failure = ex;
        }
        finally
        {
            conn.Socket.Abort(); // guarantee State != Open so IsLive can never reuse this connection
            FailAll(conn, failure);
            // Only clear the shared slot if it still points at THIS (dead) connection — never a newer one.
            Interlocked.CompareExchange(ref _connection, null, conn);
        }
    }

    private void Dispatch(Connection conn, string message, CancellationToken cancellationToken)
    {
        JsonDocument doc;
        try
        {
            doc = JsonDocument.Parse(message);
        }
        catch (JsonException)
        {
            return; // a malformed frame is skipped, not fatal to the whole connection
        }
        using (doc)
        {
            JsonElement root = doc.RootElement;
            string type = TypeOf(root);

            if (type == "ping")
            {
                _ = SendPongSafeAsync(conn, cancellationToken);
                return;
            }
            if (type == "pong")
            {
                return;
            }

            if (!root.TryGetProperty("id", out JsonElement idElem) || idElem.ValueKind != JsonValueKind.String)
            {
                return; // a connection-level frame we don't act on
            }
            string id = idElem.GetString()!;
            if (!conn.Operations.TryGetValue(id, out IOperation? op))
            {
                return; // a frame for an operation we already completed
            }

            switch (type)
            {
                case "next":
                    DispatchNext(root, op);
                    break;
                case "error":
                    op.Fail(new GraphQlRequestException(FirstErrorMessage(root, "subscription error")));
                    conn.Operations.TryRemove(id, out _);
                    break;
                case "complete":
                    op.Complete();
                    conn.Operations.TryRemove(id, out _);
                    break;
            }
        }
    }

    // A `next` frame normally carries data; a per-event resolver error carries `errors` with no
    // data (graph-gophers). Surface the latter as an operation error rather than silently idling.
    private static void DispatchNext(JsonElement root, IOperation op)
    {
        if (!root.TryGetProperty("payload", out JsonElement payload))
        {
            return;
        }
        if (payload.TryGetProperty("data", out JsonElement data) && data.ValueKind != JsonValueKind.Null)
        {
            op.OnNext(data);
            return;
        }
        if (payload.TryGetProperty("errors", out JsonElement errors)
            && errors.ValueKind == JsonValueKind.Array && errors.GetArrayLength() > 0)
        {
            op.Fail(new GraphQlRequestException(FirstErrorMessage(payload, "subscription error")));
        }
    }

    private static void FailAll(Connection conn, Exception ex)
    {
        foreach (KeyValuePair<string, IOperation> entry in conn.Operations)
        {
            if (conn.Operations.TryRemove(entry.Key, out IOperation? op))
            {
                op.Fail(ex);
            }
        }
    }

    private async Task TryCompleteAsync(Connection conn, string id)
    {
        if (!conn.Socket.IsOpen)
        {
            return;
        }
        try
        {
            byte[] frame = Encoding.UTF8.GetBytes($"{{\"id\":\"{id}\",\"type\":\"complete\"}}");
            await LockedSendAsync(conn, frame, _disposedCts.Token).ConfigureAwait(false);
        }
        catch
        {
            // Socket closing/closed — nothing to complete.
        }
    }

    private async Task SendPongSafeAsync(Connection conn, CancellationToken cancellationToken)
    {
        try
        {
            await LockedSendAsync(conn, PongFrame, cancellationToken).ConfigureAwait(false);
        }
        catch
        {
            // Socket went away between the ping and our pong — the read loop will observe the close.
        }
    }

    private async Task LockedSendAsync(Connection conn, byte[] payload, CancellationToken cancellationToken)
    {
        await conn.SendLock.WaitAsync(cancellationToken).ConfigureAwait(false);
        try
        {
            await conn.Socket.SendTextAsync(payload, cancellationToken).ConfigureAwait(false);
        }
        finally
        {
            conn.SendLock.Release();
        }
    }

    private static async Task<string> ReceiveTextAsync(IWebSocketConnection socket, CancellationToken cancellationToken)
    {
        WebSocketMessage message = await socket.ReceiveAsync(cancellationToken).ConfigureAwait(false);
        if (message.Kind == WebSocketMessageKind.Closed)
        {
            // Carry the spec close code (4401 invalid token, 4429 rate-limited, …) so a
            // consumer can tell "re-auth needed" from a transient drop.
            throw new GraphQlRequestException(
                $"subscription socket closed by server ({message.CloseCode}): {message.CloseReason}");
        }
        return message.Text!;
    }

    private static readonly byte[] PongFrame = Encoding.UTF8.GetBytes("{\"type\":\"pong\"}");

    private static string TypeOf(JsonElement root) =>
        root.TryGetProperty("type", out JsonElement t) && t.ValueKind == JsonValueKind.String ? t.GetString()! : "";

    private static string FirstErrorMessage(JsonElement container, string fallback)
    {
        JsonElement errors = container;
        if (container.ValueKind == JsonValueKind.Object && container.TryGetProperty("payload", out JsonElement p))
        {
            errors = p;
        }
        if (errors.ValueKind == JsonValueKind.Array && errors.GetArrayLength() > 0
            && errors[0].TryGetProperty("message", out JsonElement msg) && msg.ValueKind == JsonValueKind.String)
        {
            return msg.GetString()!;
        }
        return fallback;
    }

    private void ThrowIfDisposed()
    {
        if (_disposed)
        {
            throw new ObjectDisposedException(nameof(GraphQlWsClient));
        }
    }

    /// <inheritdoc />
    public async ValueTask DisposeAsync()
    {
        if (_disposed)
        {
            return;
        }
        _disposed = true;
        _disposedCts.Cancel();

        Connection? conn = _connection;
        if (conn is not null)
        {
            conn.Socket.Abort(); // unblock the read loop's pending ReceiveAsync (no concurrent CloseAsync)
            try
            {
                await conn.ReadLoop.ConfigureAwait(false); // its finally fails all ops + clears the slot
            }
            catch
            {
                // Loop faulted on the way down — ops are failed either way.
            }
            conn.Socket.Dispose();
        }
        _disposedCts.Dispose();
    }

    // ── per-connection + per-operation plumbing ─────────────────────────────

    private sealed class Connection
    {
        public Connection(IWebSocketConnection socket, int generation)
        {
            Socket = socket;
            Generation = generation;
        }

        public IWebSocketConnection Socket { get; }
        public int Generation { get; }
        public Task ReadLoop { get; set; } = Task.CompletedTask;
        public SemaphoreSlim SendLock { get; } = new(1, 1);
        public ConcurrentDictionary<string, IOperation> Operations { get; } = new();
    }

    private interface IOperation
    {
        void OnNext(JsonElement data);
        void Fail(Exception ex);
        void Complete();
    }

    private sealed class Operation<T> : IOperation
    {
        private readonly Channel<T> _channel = Channel.CreateBounded<T>(new BoundedChannelOptions(OperationBufferCapacity)
        {
            FullMode = BoundedChannelFullMode.DropOldest,
            SingleReader = true,
            SingleWriter = false,
        });
        private readonly JsonTypeInfo<T> _dataInfo;

        public Operation(JsonTypeInfo<T> dataInfo) => _dataInfo = dataInfo;

        public ChannelReader<T> Reader => _channel.Reader;

        public void OnNext(JsonElement data)
        {
            // Contain a bad payload to THIS operation: a deserialization failure completes only
            // this subscription with the error, never unwinds the shared read loop.
            try
            {
                T? value = JsonSerializer.Deserialize(data.GetRawText(), _dataInfo);
                if (value is not null)
                {
                    _channel.Writer.TryWrite(value);
                }
            }
            catch (Exception ex)
            {
                _channel.Writer.TryComplete(ex);
            }
        }

        public void Fail(Exception ex) => _channel.Writer.TryComplete(ex);

        public void Complete() => _channel.Writer.TryComplete();
    }
}
