// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;

namespace DeviceChain.Harborpulse.Model
{
    /// <summary>Two-second polling with one request in flight; failures and gaps break positive coverage.</summary>
    public sealed class PumpCommandPoll
    {
        DateTimeOffset next;
        DateTimeOffset? healthySince, lastOk;
        bool busy;
        public bool TryBegin(DateTimeOffset now)
        {
            if (busy || now < next) return false;
            busy = true; next = now.AddSeconds(2); return true;
        }
        public void Complete(DateTimeOffset now)
        {
            if (!lastOk.HasValue || now - lastOk.Value > TimeSpan.FromSeconds(3)) healthySince = now;
            if (!healthySince.HasValue) healthySince = now;
            lastOk = now; busy = false;
        }
        public void Fail() { healthySince = null; lastOk = null; busy = false; }
        public CommandCoverage Coverage(DateTimeOffset now) => !lastOk.HasValue || now - lastOk.Value > TimeSpan.FromSeconds(3)
            ? default : new CommandCoverage(healthySince, lastOk);
    }
}
