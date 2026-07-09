// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Unity
{
    /// <summary>
    /// Constructs a <see cref="DeviceChainClient"/> wired with the transports appropriate to the
    /// current Unity platform — the payoff of the SDK's transport seam (ADR-035 slice 4.1):
    /// <list type="bullet">
    ///   <item>HTTP is always <see cref="UnityWebRequestHttpTransport"/> (works in the Editor,
    ///   IL2CPP standalone, and WebGL, where <c>HttpClient</c> does not).</item>
    ///   <item>WebSocket is the SDK's own <see cref="ClientWebSocketFactory"/> everywhere EXCEPT
    ///   the WebGL player, where it falls back to <see cref="WebGlWebSocketFactory"/> (a browser
    ///   <c>WebSocket</c> via <c>.jslib</c>). <c>ClientWebSocket</c> works fine in the Editor and in
    ///   IL2CPP desktop/mobile builds, so those get the proven path.</item>
    /// </list>
    /// The caller drives the returned client with the normal SDK API (login → selectTenant →
    /// query / subscribe / emit). Dispose it (<c>await client.DisposeAsync()</c>) on teardown.
    /// </summary>
    public static class DeviceChainRuntime
    {
        /// <summary>Creates a platform-appropriate client for <paramref name="origin"/>.</summary>
        public static DeviceChainClient CreateClient(Uri origin)
        {
            if (origin == null) throw new ArgumentNullException(nameof(origin));

            IHttpTransport http = new UnityWebRequestHttpTransport();
            IWebSocketFactory sockets =
#if UNITY_WEBGL && !UNITY_EDITOR
                new WebGlWebSocketFactory();
#else
                new ClientWebSocketFactory();
#endif
            return new DeviceChainClient(origin, http, sockets);
        }

        /// <summary>Convenience overload taking the origin as a string.</summary>
        public static DeviceChainClient CreateClient(string origin) => CreateClient(new Uri(origin));
    }
}
