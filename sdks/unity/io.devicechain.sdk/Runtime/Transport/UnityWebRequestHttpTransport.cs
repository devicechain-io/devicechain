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
    /// </summary>
    public sealed class UnityWebRequestHttpTransport : IHttpTransport
    {
        /// <inheritdoc />
        public Task<HttpTransportResponse> SendAsync(HttpTransportRequest request, CancellationToken cancellationToken)
        {
            if (request == null) throw new ArgumentNullException(nameof(request));

            var tcs = new TaskCompletionSource<HttpTransportResponse>();
            var uwr = new UnityWebRequest(request.Uri.AbsoluteUri, request.Method)
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
            UnityWebRequestAsyncOperation op = uwr.SendWebRequest();
            op.completed += _ =>
            {
                registration.Dispose();
                try
                {
                    // ConnectionError / DataProcessingError = a genuine transport failure → throw.
                    // ProtocolError (an HTTP 4xx/5xx) still carries a status + body → return it.
                    if (uwr.result == UnityWebRequest.Result.ConnectionError
                        || uwr.result == UnityWebRequest.Result.DataProcessingError)
                    {
                        tcs.TrySetException(new Exception(uwr.error ?? "transport error"));
                        return;
                    }
                    tcs.TrySetResult(new HttpTransportResponse
                    {
                        Status = (int)uwr.responseCode,
                        Body = uwr.downloadHandler != null && uwr.downloadHandler.data != null
                            ? uwr.downloadHandler.data
                            : Array.Empty<byte>(),
                    });
                }
                finally
                {
                    uwr.Dispose();
                }
            };

            if (cancellationToken.CanBeCanceled)
            {
                registration = cancellationToken.Register(() =>
                {
                    try { uwr.Abort(); }
                    catch { /* already completing */ }
                    tcs.TrySetCanceled(cancellationToken);
                });
            }

            return tcs.Task;
        }
    }
}
