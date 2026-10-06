// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;

namespace DeviceChain.Sitepulse.Platform
{
    /// <summary>
    /// What the screen says about the observer's own health. The site-wide banner appears whenever the
    /// measurement stream is not live: the values on the cards are then the last ones seen, and the
    /// banner says since when. It never says the observer is fine; a live stream shows no banner.
    /// </summary>
    public static class ObserverBanner
    {
        /// <summary>The banner text, or null when nothing needs saying.</summary>
        public static string Text(ObserverStatus status, ObservedState state)
        {
            var sb = new StringBuilder();
            var m = status.Measurements;
            if (m.State != StreamState.Live)
            {
                var frozen = state.LastMeasurementSeenAt ?? m.LastLiveAt;
                if (m.State == StreamState.Reconnecting && frozen.HasValue)
                    sb.Append("Observer reconnecting — values frozen at ").Append(Clock(frozen.Value));
                else if (m.State == StreamState.Reconnecting)
                    sb.Append("Observer reconnecting — no values observed yet");
                else if (m.State == StreamState.Subscribed)
                    sb.Append("Observer subscribed — no data yet");
                else
                    sb.Append("Observer connecting — no values observed yet");
            }

            // alarms are a second stream: while it is down the alarm state on a card may be out of date. One that is
            // subscribed and has said nothing is normal (alarms are events), so it is shown in the panel, not here
            var a = status.Alarms;
            if (m.State == StreamState.Live && a.State != StreamState.Live && a.State != StreamState.Idle && a.State != StreamState.Subscribed)
                sb.Append(a.State == StreamState.Reconnecting ? "Alarm stream reconnecting — alarm state may be out of date" : "Alarm stream connecting — alarm state not observed yet");
            return sb.Length == 0 ? null : sb.ToString();
        }

        /// <summary>One line for the readiness panel: both streams, and any poll that is failing.</summary>
        public static string Line(ObserverStatus status)
        {
            var sb = new StringBuilder("observer · measurements ").Append(Word(status.Measurements))
                .Append(" · alarms ").Append(Word(status.Alarms));
            if (status.AlarmSnapshotNote != null) sb.Append(" · ").Append(status.AlarmSnapshotNote);
            if (status.PollErrors.Count == 0) return sb.Append(" · polls ok").ToString();
            var names = new List<string>(status.PollErrors.Keys);
            names.Sort(StringComparer.Ordinal);
            foreach (var n in names) sb.Append(" · ").Append(n).Append(" poll failing: ").Append(status.PollErrors[n]);
            return sb.ToString();
        }

        static string Word(StreamStatus s)
        {
            switch (s.State)
            {
                case StreamState.Live: return string.IsNullOrEmpty(s.Reason) ? "live" : "live (" + s.Reason + ")";
                case StreamState.Reconnecting: return "reconnecting" + (string.IsNullOrEmpty(s.Reason) ? "" : " (" + s.Reason + ")");
                case StreamState.Connecting: return "connecting";
                case StreamState.Subscribed: return "subscribed · no data yet" + (string.IsNullOrEmpty(s.Reason) ? "" : " (" + s.Reason + ")");
                default: return "not started";
            }
        }

        /// <summary>A time of day in this machine's zone, to the second.</summary>
        public static string Clock(DateTimeOffset t) => t.ToLocalTime().ToString("HH:mm:ss", CultureInfo.InvariantCulture);
    }
}
