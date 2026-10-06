// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;
using UnityEngine.Networking;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// Fetches the runner's <c>/config.json</c> with <see cref="UnityWebRequest"/>. Construct it on
    /// the main thread: it captures that thread's context and starts every request there, because
    /// the token broker refreshes from whatever thread the SDK resumed on and
    /// <c>UnityWebRequest</c> may only be started from the main thread.
    /// </summary>
    public sealed class RunnerConfigClient
    {
        readonly SynchronizationContext main;
        readonly string runnerUrl;
        readonly LiveSettings expect;

        public RunnerConfigClient(LiveSettings settings)
        {
            if (settings == null) throw new ArgumentNullException(nameof(settings));
            main = SynchronizationContext.Current
                   ?? throw new InvalidOperationException("RunnerConfigClient must be constructed on the Unity main thread");
            runnerUrl = settings.RunnerUrl.TrimEnd('/') + "/config.json";
            expect = settings;
        }

        /// <summary>GET and validate. A transport failure is a failed result, not an exception.</summary>
        public Task<Parsed<RunnerConfig>> Fetch(CancellationToken ct)
        {
            var tcs = new TaskCompletionSource<Parsed<RunnerConfig>>(TaskCreationOptions.RunContinuationsAsynchronously);
            if (ct.IsCancellationRequested)
            {
                tcs.SetCanceled();
                return tcs.Task;
            }

            main.Post(_ => Begin(tcs), null);
            return tcs.Task;
        }

        void Begin(TaskCompletionSource<Parsed<RunnerConfig>> tcs)
        {
            UnityWebRequest req = null;
            try
            {
                req = UnityWebRequest.Get(runnerUrl);
                req.timeout = 10; // seconds; bounds the request, which is not cancellable once sent
                var op = req.SendWebRequest();
                op.completed += _ =>
                {
                    try
                    {
                        if (req.result != UnityWebRequest.Result.Success)
                            tcs.TrySetResult(Parsed<RunnerConfig>.Fail($"cannot reach the runner at {runnerUrl}: {req.error}"));
                        else
                            tcs.TrySetResult(RunnerConfigParser.Parse(req.downloadHandler.text, expect));
                    }
                    catch (Exception e)
                    {
                        tcs.TrySetException(e);
                    }
                    finally
                    {
                        req.Dispose();
                    }
                };
            }
            catch (Exception e)
            {
                req?.Dispose();
                tcs.TrySetException(e);
            }
        }
    }
}
