// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using DeviceChain.Sitepulse.Domain;

namespace DeviceChain.Sitepulse.Recording
{
    /// <summary>
    /// How what the observer handed the app becomes proof rows. The one function both the live app (from the observer's items, mapped to
    /// the lines a recording keeps) and a replay (from the recorded lines) call, so a drawer drawn from a recording holds the rows the live
    /// one held and no others.
    /// </summary>
    public static class ProofFeed
    {
        /// <param name="idOfToken">The scene id (SP-HL-0006) of a platform device token, or null for a device the scene does not know.</param>
        public static void Apply(ProofLog log, ObservedLine l, Func<string, string> idOfToken)
        {
            if (log == null) throw new ArgumentNullException(nameof(log));
            switch (l.K)
            {
                case ObservedKinds.Measurement:
                {
                    var id = idOfToken(l.Device);
                    if (id != null) log.SampleObserved(id, l.Name, l.Value, l.OccurredAt ?? l.Utc, l.FromSnapshot);
                    break;
                }

                case ObservedKinds.Alarm:
                    Alarm(log, l, idOfToken);
                    break;
                case ObservedKinds.AlarmSnapshot:
                    // what a snapshot lists is active; an alarm it does not list is not turned into a CLEARED row (nobody said it cleared)
                    foreach (var a in l.Alarms) Alarm(log, a, idOfToken);
                    break;
                case ObservedKinds.Command:
                {
                    var id = idOfToken(l.Device);
                    if (id != null) log.CommandObserved(id, l.Token, l.Name, l.State, l.QueuedAt ?? l.Utc, l.ObservedAt ?? l.Utc);
                    break;
                }
            }
        }

        static void Alarm(ProofLog log, ObservedLine a, Func<string, string> idOfToken)
        {
            var id = idOfToken(a.Device);
            if (id != null) log.AlarmObserved(id, a.Token, a.Name, a.State, a.Severity, a.OccurredAt ?? a.Utc);
        }
    }
}
