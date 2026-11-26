// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using DeviceChain.Harborpulse.Model;
using UnityEngine.Networking;

namespace DeviceChain.Harborpulse.Live
{
    /// <summary>
    /// Polls the runner's <c>GET /status</c> for the compressed day, on the main thread with <see cref="UnityWebRequest"/>, at most one request in
    /// flight. Each answer goes to the <see cref="ScenarioClockFollower"/>; a failure is recorded there, so the clock goes visibly stale rather
    /// than turning on its own. The control API carries no credential and this client sends none.
    /// </summary>
    public sealed class ScenarioStatusClient : IDisposable
    {
        public static readonly TimeSpan Every = TimeSpan.FromSeconds(1);

        readonly string url;
        readonly ScenarioClockFollower follower;
        UnityWebRequest inflight;
        DateTimeOffset next = DateTimeOffset.MinValue;
        bool disposed;

        public ScenarioStatusClient(string runnerUrl, ScenarioClockFollower follower)
        {
            if (SynchronizationContext.Current == null) throw new InvalidOperationException("ScenarioStatusClient must be constructed on the Unity main thread");
            url = runnerUrl.TrimEnd('/') + "/status";
            this.follower = follower ?? throw new ArgumentNullException(nameof(follower));
        }

        /// <summary>The last reason a poll failed, or null while they succeed.</summary>
        public ScenarioStatus Last { get; private set; }

        public string Problem { get; private set; }

        /// <summary>Main thread, every frame.</summary>
        public void Tick(DateTimeOffset now)
        {
            if (disposed || inflight != null || now < next) return;
            next = now + Every;
            var req = UnityWebRequest.Get(url);
            req.timeout = 3;
            inflight = req;
            var op = req.SendWebRequest();
            op.completed += _ =>
            {
                try
                {
                    if (disposed) return;
                    if (req.result != UnityWebRequest.Result.Success)
                    {
                        Fail("cannot reach " + url + ": " + req.error);
                        return;
                    }

                    ScenarioStatus status;
                    try { status = ScenarioStatus.Parse(req.downloadHandler.text); }
                    catch (FormatException e)
                    {
                        Fail(e.Message);
                        return;
                    }

                    Last = status;
                    Problem = null;
                    follower.Observe(status, DateTimeOffset.UtcNow);
                }
                finally
                {
                    if (ReferenceEquals(inflight, req)) inflight = null;
                    req.Dispose();
                }
            };
        }

        void Fail(string why)
        {
            Problem = why;
            follower.ObserveFailure(why);
        }

        public void Dispose()
        {
            // No Abort: see DirectorReads.Dispose. The poll in flight ends on its own 3 s timeout; its completion sees `disposed`.
            disposed = true;
        }
    }
}
