// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Runtime.InteropServices;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;

namespace DeviceChain.Sdk.Unity
{
    /// <summary>
    /// An <see cref="IWebSocketFactory"/> for the WebGL player, where
    /// <c>System.Net.WebSockets.ClientWebSocket</c> does not exist — it drives the browser's
    /// <c>WebSocket</c> through <c>DeviceChainWebSocket.jslib</c>. On non-WebGL platforms the SDK's
    /// own <see cref="ClientWebSocketFactory"/> is used instead (see <see cref="DeviceChainRuntime"/>),
    /// so this type's members throw off-WebGL.
    /// </summary>
    /// <remarks>
    /// UNVERIFIED — authored without a WebGL build to test against (see the package VERIFICATION.md).
    /// Known open risk beyond this class: the SDK's subscription read loop is started with
    /// <c>Task.Run</c>, which has no real thread pool under WebGL — so under WebGL a live subscribe
    /// yields nothing and (because <c>DisposeAsync</c> awaits that never-run loop) teardown hangs too.
    /// Making that loop pump-based is a required follow-up before WebGL live-subscribe works. HTTP
    /// (UnityWebRequest) and the Editor/standalone WebSocket path are unaffected.
    /// </remarks>
    public sealed class WebGlWebSocketFactory : IWebSocketFactory
    {
        /// <inheritdoc />
        public IWebSocketConnection Create() => new WebGlWebSocketConnection();
    }

    /// <summary>A single browser-<c>WebSocket</c> connection behind <see cref="IWebSocketConnection"/>.</summary>
    public sealed class WebGlWebSocketConnection : IWebSocketConnection
    {
        // Abnormal-closure code reported when the caller aborts a live connection.
        private const int AbortCloseCode = 1006;

        private int _id;
        private volatile bool _aborted;

#if UNITY_WEBGL && !UNITY_EDITOR
        [DllImport("__Internal")] private static extern int DCWs_Create(string url);
        [DllImport("__Internal")] private static extern void DCWs_Connect(int id, string subProtocol);
        [DllImport("__Internal")] private static extern int DCWs_State(int id);
        [DllImport("__Internal")] private static extern void DCWs_Send(int id, string message);
        [DllImport("__Internal")] private static extern int DCWs_HasMessage(int id);
        [DllImport("__Internal")] private static extern IntPtr DCWs_TakeMessage(int id);
        [DllImport("__Internal")] private static extern void DCWs_Free(IntPtr ptr);
        [DllImport("__Internal")] private static extern int DCWs_Closed(int id);
        [DllImport("__Internal")] private static extern int DCWs_CloseCode(int id);
        [DllImport("__Internal")] private static extern IntPtr DCWs_CloseReason(int id);
        [DllImport("__Internal")] private static extern void DCWs_Destroy(int id);
#else
        // Off-WebGL these are never reached (the runtime picks ClientWebSocketFactory), but the type
        // must compile on every platform so the bootstrap can name it unconditionally.
        private static int DCWs_Create(string url) => throw NotWebGl();
        private static void DCWs_Connect(int id, string subProtocol) => throw NotWebGl();
        private static int DCWs_State(int id) => throw NotWebGl();
        private static void DCWs_Send(int id, string message) => throw NotWebGl();
        private static int DCWs_HasMessage(int id) => throw NotWebGl();
        private static IntPtr DCWs_TakeMessage(int id) => throw NotWebGl();
        private static void DCWs_Free(IntPtr ptr) => throw NotWebGl();
        private static int DCWs_Closed(int id) => throw NotWebGl();
        private static int DCWs_CloseCode(int id) => throw NotWebGl();
        private static IntPtr DCWs_CloseReason(int id) => throw NotWebGl();
        private static void DCWs_Destroy(int id) => throw NotWebGl();

        private static Exception NotWebGl() =>
            new NotSupportedException("WebGlWebSocketConnection is only usable in a WebGL player; use ClientWebSocketFactory elsewhere.");
#endif

        /// <inheritdoc />
        public bool IsOpen => _id != 0 && !_aborted && DCWs_State(_id) == 1;

        /// <inheritdoc />
        public async Task ConnectAsync(Uri endpoint, string subProtocol, CancellationToken cancellationToken)
        {
            _id = DCWs_Create(endpoint.AbsoluteUri);
            DCWs_Connect(_id, subProtocol ?? string.Empty);
            try
            {
                while (true)
                {
                    int state = DCWs_State(_id);
                    if (state == 1) return;                       // OPEN
                    if (state == -1 || state == 2 || state == 3)  // errored / closing / closed
                    {
                        throw new Exception("WebGL WebSocket failed to open");
                    }
                    cancellationToken.ThrowIfCancellationRequested();
                    await Task.Yield(); // single-threaded: yield to the browser event loop between polls
                }
            }
            catch
            {
                Abort(); // never leak the jslib socket entry on a failed/cancelled open
                throw;
            }
        }

        /// <inheritdoc />
        public Task SendTextAsync(byte[] utf8Payload, CancellationToken cancellationToken)
        {
            DCWs_Send(_id, Encoding.UTF8.GetString(utf8Payload));
            return Task.CompletedTask;
        }

        /// <inheritdoc />
        public async Task<WebSocketMessage> ReceiveAsync(CancellationToken cancellationToken)
        {
            while (true)
            {
                // Checked FIRST so an Abort() between polls unblocks this receive (seam contract) even
                // though Abort tears down the underlying jslib socket.
                if (_aborted)
                {
                    return WebSocketMessage.OfClose(AbortCloseCode, "aborted");
                }
                if (_id != 0 && DCWs_HasMessage(_id) == 1)
                {
                    IntPtr ptr = DCWs_TakeMessage(_id);
                    try
                    {
                        return WebSocketMessage.OfText(PtrToStringUtf8(ptr));
                    }
                    finally
                    {
                        DCWs_Free(ptr);
                    }
                }
                if (_id != 0 && DCWs_Closed(_id) == 1)
                {
                    IntPtr reasonPtr = DCWs_CloseReason(_id);
                    try
                    {
                        return WebSocketMessage.OfClose(DCWs_CloseCode(_id), PtrToStringUtf8(reasonPtr));
                    }
                    finally
                    {
                        DCWs_Free(reasonPtr);
                    }
                }
                cancellationToken.ThrowIfCancellationRequested();
                await Task.Yield();
            }
        }

        /// <inheritdoc />
        public void Abort()
        {
            _aborted = true; // set BEFORE destroy so a racing ReceiveAsync returns a close, not a spin
            if (_id != 0)
            {
                DCWs_Destroy(_id);
                _id = 0;
            }
        }

        /// <inheritdoc />
        public void Dispose() => Abort();

        // A profile-agnostic replacement for Marshal.PtrToStringUTF8 (absent under Unity's ".NET
        // Framework" API compatibility level): read the NUL-terminated UTF-8 buffer manually.
        private static string PtrToStringUtf8(IntPtr ptr)
        {
            if (ptr == IntPtr.Zero)
            {
                return string.Empty;
            }
            int length = 0;
            while (Marshal.ReadByte(ptr, length) != 0)
            {
                length++;
            }
            if (length == 0)
            {
                return string.Empty;
            }
            var bytes = new byte[length];
            Marshal.Copy(ptr, bytes, 0, length);
            return Encoding.UTF8.GetString(bytes);
        }
    }
}
