// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk.Transport;
using UnityEngine.Networking;

namespace DeviceChain.Sdk.Unity
{
    /// <summary>
    /// An <see cref="IHttpTransport"/> backed by <see cref="UnityWebRequest"/> — the SDK's HTTP seam
    /// for Unity, where <c>System.Net.Http.HttpClient</c> does not work under WebGL/IL2CPP.
    /// <see cref="UnityWebRequest"/> runs on the main thread and, under WebGL, is backed by the
    /// browser's <c>fetch</c>. Faithful to the seam contract: an HTTP error status (4xx/5xx) is
    /// returned as a normal response (the caller classifies it); only a genuine connection/processing
    /// failure throws.
    ///
    /// <para><b>Construct this on the Unity main thread.</b> <see cref="UnityWebRequest.SendWebRequest"/>
    /// and <see cref="UnityWebRequest.Abort"/> are main-thread-only, but the SDK's proactive token
    /// refresh can resume a <see cref="SendAsync"/> call on a thread-pool thread (the auth path awaits
    /// with <c>ConfigureAwait(false)</c>). So the constructor captures the main-thread
    /// <see cref="SynchronizationContext"/> and every request marshals its <c>UnityWebRequest</c>
    /// lifecycle back onto it, instead of trusting whatever thread happened to call in. WebGL is
    /// single-threaded, so this is a no-op cost there and a correctness fix everywhere else.</para>
    /// </summary>
    public sealed class UnityWebRequestHttpTransport : IHttpTransport
    {
        private readonly SynchronizationContext _mainThread;

        /// <summary>
        /// Captures the current (Unity main-thread) <see cref="SynchronizationContext"/> for
        /// marshalling requests. Must be called on the main thread — Unity installs its context
        /// there; a null context means this was constructed off the main thread, which would make
        /// every request fail Unity's threading rule, so it throws rather than defer the failure.
        /// </summary>
        public UnityWebRequestHttpTransport()
        {
            _mainThread = SynchronizationContext.Current
                ?? throw new InvalidOperationException(
                    "UnityWebRequestHttpTransport must be constructed on the Unity main thread " +
                    "(no SynchronizationContext was captured).");
        }

        /// <inheritdoc />
        public Task<HttpTransportResponse> SendAsync(HttpTransportRequest request, CancellationToken cancellationToken)
        {
            if (request == null) throw new ArgumentNullException(nameof(request));

            var tcs = new TaskCompletionSource<HttpTransportResponse>();
            // Marshal the whole request onto the main thread. Post (not Send) so a caller already on
            // the main thread isn't re-entered; UnityWebRequest needs the main loop to pump regardless.
            _mainThread.Post(_ => Begin(request, cancellationToken, tcs), null);
            return tcs.Task;
        }

        // Creates and drives the UnityWebRequest. MUST run on the main thread (posted from SendAsync).
        private void Begin(HttpTransportRequest request, CancellationToken cancellationToken, TaskCompletionSource<HttpTransportResponse> tcs)
        {
            if (cancellationToken.IsCancellationRequested)
            {
                tcs.TrySetCanceled(cancellationToken);
                return;
            }

            UnityWebRequest uwr = null;
            // Once op.completed is wired up it owns disposal; before that, a throw here (a bad header
            // value, a disposed CTS in Register) must fault the task and dispose the request itself.
            // This method runs inside a SynchronizationContext.Post callback, so an escaping exception
            // would be swallowed by Unity and the awaiting caller — a lock-held refresh — would hang
            // forever instead of surfacing the error.
            bool ownedByCompletion = false;
            try
            {
                uwr = new UnityWebRequest(request.Uri.AbsoluteUri, request.Method)
                {
                    downloadHandler = new DownloadHandlerBuffer(),
                };
                if (request.Body != null)
                {
                    uwr.uploadHandler = new UploadHandlerRaw(request.Body);
                }
                if (!string.IsNullOrEmpty(request.ContentType))
                {
                    uwr.SetRequestHeader("Content-Type", request.ContentType);
                }
                if (!string.IsNullOrEmpty(request.BearerToken))
                {
                    uwr.SetRequestHeader("Authorization", "Bearer " + request.BearerToken);
                }

                CancellationTokenRegistration registration = default;
                UnityWebRequest sent = uwr;
                UnityWebRequestAsyncOperation op = uwr.SendWebRequest();
                op.completed += _ =>
                {
                    registration.Dispose();
                    try
                    {
                        // ConnectionError / DataProcessingError = a genuine transport failure → throw.
                        // ProtocolError (an HTTP 4xx/5xx) still carries a status + body → return it.
                        if (sent.result == UnityWebRequest.Result.ConnectionError
                            || sent.result == UnityWebRequest.Result.DataProcessingError)
                        {
                            tcs.TrySetException(new Exception(sent.error ?? "transport error"));
                            return;
                        }
                        tcs.TrySetResult(new HttpTransportResponse
                        {
                            Status = (int)sent.responseCode,
                            Body = sent.downloadHandler != null && sent.downloadHandler.data != null
                                ? sent.downloadHandler.data
                                : Array.Empty<byte>(),
                        });
                    }
                    finally
                    {
                        sent.Dispose();
                    }
                };
                ownedByCompletion = true; // op.completed will now dispose the request.

                if (cancellationToken.CanBeCanceled)
                {
                    registration = cancellationToken.Register(() =>
                    {
                        // The cancel may fire from any thread; Abort is main-thread-only (off-main it is
                        // a silent no-op), so marshal it back. op.completed then disposes the request.
                        _mainThread.Post(_ =>
                        {
                            try { sent.Abort(); } catch { /* already completing */ }
                        }, null);
                        tcs.TrySetCanceled(cancellationToken);
                    });
                }
            }
            catch (Exception ex)
            {
                if (!ownedByCompletion)
                {
                    uwr?.Dispose();
                }
                tcs.TrySetException(ex);
            }
        }
    }
}
