// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Threading;
using System.Threading.Tasks;
using DeviceChain.Sdk;

namespace DeviceChain.Sitepulse.Platform
{
    public enum TokenState { Fresh, Refreshing, Expired }

    /// <summary>
    /// Owns the operator token from the runner's <c>/config.json</c>. The runner hands out a fresh
    /// 15-minute token on every request, so the broker re-asks <see cref="Margin"/> before the
    /// current one expires (the <c>exp</c> claim is read unverified, for scheduling only) and, on
    /// demand, after the platform answers 401. It is the SDK's <see cref="TokenProvider"/>.
    ///
    /// If the runner cannot be reached the broker keeps serving the token it has until it truly
    /// expires, then turns <see cref="TokenState.Expired"/> with a reason the UI shows; device
    /// sessions are unaffected (device credentials do not expire) but observed values go stale,
    /// which is the honest picture. State is read from the main thread; refreshes may finish on any.
    /// </summary>
    public sealed class TokenBroker
    {
        public static readonly TimeSpan DefaultMargin = TimeSpan.FromSeconds(120);
        static readonly TimeSpan RetryEvery = TimeSpan.FromSeconds(15);

        readonly Func<CancellationToken, Task<Parsed<RunnerConfig>>> fetch;
        readonly Func<DateTimeOffset> clock;
        readonly object gate = new object();
        string token;
        DateTimeOffset expiry;
        DateTimeOffset nextAttempt = DateTimeOffset.MinValue;
        Task inflight;
        TokenState state = TokenState.Fresh;
        string reason;

        public TimeSpan Margin { get; }

        public TokenBroker(RunnerConfig initial, Func<CancellationToken, Task<Parsed<RunnerConfig>>> fetch,
            Func<DateTimeOffset> clock = null, TimeSpan? margin = null)
        {
            if (initial == null) throw new ArgumentNullException(nameof(initial));
            this.fetch = fetch ?? throw new ArgumentNullException(nameof(fetch));
            this.clock = clock ?? (() => DateTimeOffset.UtcNow);
            Margin = margin ?? DefaultMargin;
            if (!Jwt.TryGetExpiry(initial.Token, out expiry))
                throw new ArgumentException("the operator token has no readable exp claim", nameof(initial));
            token = initial.Token;
        }

        public TokenState State { get { lock (gate) return state; } }

        /// <summary>Why the token is expired; null otherwise.</summary>
        public string Reason { get { lock (gate) return reason; } }

        public DateTimeOffset ExpiresAt { get { lock (gate) return expiry; } }

        /// <summary>The SDK's per-request token hook.</summary>
        public TokenProvider AsProvider() => ct => Get(ct);

        /// <summary>The current token, refreshed first when it is inside the margin.</summary>
        public async ValueTask<string> Get(CancellationToken ct)
        {
            Task wait = null;
            lock (gate)
            {
                if (Due() && clock() >= nextAttempt) wait = StartRefresh();
                else if (inflight != null && !inflight.IsCompleted && clock() >= expiry) wait = inflight;
            }

            if (wait != null) await wait.ConfigureAwait(false);
            lock (gate)
            {
                if (clock() >= expiry) throw new InvalidOperationException("the operator token has expired: " + (reason ?? "not refreshed"));
                return token;
            }
        }

        /// <summary>
        /// The platform rejected <paramref name="rejected"/> (a 401). Fetch a new one unless a newer
        /// one has already replaced it.
        /// </summary>
        public Task NotifyUnauthorized(string rejected)
        {
            lock (gate)
            {
                if (rejected != token) return Task.CompletedTask;
                // a 401 means this token is dead whatever its exp says
                expiry = clock();
                nextAttempt = DateTimeOffset.MinValue;
                return StartRefresh();
            }
        }

        /// <summary>Called every frame from the main thread: starts the scheduled refresh without waiting for a request.</summary>
        public void Tick()
        {
            lock (gate)
            {
                if (Due() && clock() >= nextAttempt) StartRefresh();
            }
        }

        // the runner answered with the token already held, or with one that expires no later
        bool IsCurrent(string candidate, DateTimeOffset candidateExpiry)
        {
            lock (gate) return candidate == token || candidateExpiry <= expiry;
        }

        bool Due() => (inflight == null || inflight.IsCompleted) && clock() >= expiry - Margin;

        // caller holds the gate
        Task StartRefresh()
        {
            if (inflight != null && !inflight.IsCompleted) return inflight;
            state = TokenState.Refreshing;
            inflight = RefreshCore();
            return inflight;
        }

        async Task RefreshCore()
        {
            // yield first: the caller holds the gate while this starts, and a synchronous fetch
            // must not re-enter it
            await Task.Yield();
            string failure = null;
            try
            {
                var r = await fetch(CancellationToken.None).ConfigureAwait(false);
                if (!r.Ok) failure = "the runner refused: " + r.Error;
                else if (!Jwt.TryGetExpiry(r.Value.Token, out var exp)) failure = "the runner's token has no readable exp";
                else if (IsCurrent(r.Value.Token, exp))
                {
                    // The runner caches its token and renews it only a minute before it expires,
                    // later than this broker asks. Getting the same token back is not a failure,
                    // but asking again at once would hit the runner every frame until it renews:
                    // keep the token, ask again after the retry interval. After a 401 the same
                    // token is dead whatever its exp says, so it stays expired until a new one comes.
                    lock (gate)
                    {
                        nextAttempt = clock() + RetryEvery;
                        if (clock() >= expiry)
                        {
                            state = TokenState.Expired;
                            reason = "the runner still serves the token the platform refused";
                        }
                        else
                        {
                            state = TokenState.Fresh;
                        }
                    }

                    return;
                }
                else
                {
                    lock (gate)
                    {
                        token = r.Value.Token;
                        expiry = exp;
                        state = TokenState.Fresh;
                        reason = null;
                    }

                    PlatformLog.Info($"operator token refreshed, expires {exp:HH:mm:ss}Z");
                    return;
                }
            }
            catch (Exception e)
            {
                failure = e.Message;
            }

            lock (gate)
            {
                nextAttempt = clock() + RetryEvery;
                if (clock() >= expiry)
                {
                    state = TokenState.Expired;
                    reason = failure;
                }
                else
                {
                    state = TokenState.Fresh; // the old token is still good; try again shortly
                }
            }

            PlatformLog.Warn("operator token refresh failed: " + failure);
        }
    }
}
