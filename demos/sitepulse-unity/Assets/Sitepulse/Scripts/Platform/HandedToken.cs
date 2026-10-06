// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// One subscription socket's view of the operator token: the token it last handed the SDK for
    /// <c>connection_init</c>. When the server closes that socket with 4401, the token to report as
    /// refused is THAT one — not whatever the broker holds now. A socket opened before a refresh is
    /// closed when its old token expires, by which time the broker already holds a fresh one;
    /// reporting the fresh one would mark a good token dead, and since the runner keeps serving it,
    /// the observer would never recover. The broker ignores a refusal of a token it no longer holds.
    /// </summary>
    public sealed class HandedToken
    {
        readonly TokenBroker broker;
        string last;

        public HandedToken(TokenBroker broker)
        {
            this.broker = broker;
            Provider = Hand;
        }

        /// <summary>The SDK's token hook for this socket.</summary>
        public TokenProvider Provider { get; }

        /// <summary>The token this socket last connected with; null before its first connection.</summary>
        public string Last => Volatile.Read(ref last);

        async ValueTask<string> Hand(CancellationToken ct)
        {
            var token = await broker.Get(ct);
            Volatile.Write(ref last, token);
            return token;
        }

        /// <summary>The server refused this socket's token (4401): report that token, and only that one.</summary>
        public Task RefreshAfterRejection(CancellationToken ct)
        {
            var refused = Last;
            return refused == null ? Task.CompletedTask : broker.NotifyUnauthorized(refused);
        }
    }
}
