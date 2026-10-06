// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Sitepulse.DevicePlane;
using DeviceChain.Sitepulse.Platform;
using DeviceChain.Sitepulse.Recording;
using DeviceChain.Sitepulse.Simulation;

namespace DeviceChain.Sitepulse.App
{
    /// <summary>
    /// How what the live app saw becomes recording lines: the observer's items to observed.ndjson lines, the device plane's events and
    /// samples to device.ndjson lines. The only place the two meet; the Recording and Replay assemblies know the lines and nothing of
    /// the platform.
    /// </summary>
    public static class RecordingMaps
    {
        /// <summary>The line for an observer item, or null for one a recording does not keep.</summary>
        public static ObservedLine Observed(ObserverItem item)
        {
            switch (item)
            {
                case MeasurementItem m:
                {
                    var l = ObservedLine.Of(ObservedKinds.Measurement);
                    l.Device = m.DeviceToken;
                    l.Name = m.Name;
                    l.Value = m.Value;
                    l.OccurredAt = m.OccurredAt;
                    l.ObservedAt = m.ObservedAt;
                    l.FromSnapshot = m.FromSnapshot;
                    return l;
                }

                case AlarmItem a:
                    return Alarm(a.DeviceToken, a.Alarm);
                case AlarmSnapshotItem s:
                {
                    var l = ObservedLine.Of(ObservedKinds.AlarmSnapshot);
                    l.RequestedAt = s.RequestedAt;
                    l.ObservedAt = s.ObservedAt;
                    l.Truncated = s.Truncated;
                    l.TotalRecords = s.TotalRecords;
                    foreach (var a in s.Alarms) l.Alarms.Add(Alarm(a.DeviceToken, a.Alarm));
                    return l;
                }

                case LocationItem loc:
                {
                    var l = ObservedLine.Of(ObservedKinds.Location);
                    l.Device = loc.DeviceToken;
                    l.SpeedMps = loc.Location.SpeedMps;
                    l.HeadingDegrees = loc.Location.HeadingDegrees;
                    l.ElevationMetres = loc.Location.ElevationMetres;
                    l.OccurredAt = loc.Location.OccurredAt;
                    l.ObservedAt = loc.Location.ObservedAt;
                    return l;
                }

                case CommandItem c:
                {
                    var l = ObservedLine.Of(ObservedKinds.Command);
                    l.Device = c.DeviceToken;
                    l.Token = c.Command.Token;
                    l.Name = c.Command.Name;
                    l.State = c.Command.Status;
                    l.QueuedAt = c.Command.QueuedAt;
                    l.ObservedAt = c.Command.ObservedAt;
                    return l;
                }

                case PresenceItem p:
                {
                    var l = ObservedLine.Of(ObservedKinds.Presence);
                    l.Device = p.DeviceToken;
                    l.PresenceActive = p.Presence.Active;
                    l.LastActivityAt = p.Presence.LastActivityAt;
                    l.ObservedAt = p.Presence.ObservedAt;
                    return l;
                }

                case StatusItem st:
                {
                    var l = ObservedLine.Of(ObservedKinds.Status);
                    l.Name = st.Source;
                    l.State = st.State;
                    l.Reason = st.Reason;
                    l.OccurredAt = st.At;
                    return l;
                }

                default:
                    return null;
            }
        }

        static ObservedLine Alarm(string deviceToken, ObservedAlarm a)
        {
            var l = ObservedLine.Of(ObservedKinds.Alarm);
            l.Device = deviceToken;
            l.Token = a.Token;
            l.Name = a.AlarmKey;
            l.MetricKey = a.MetricKey;
            l.State = a.State;
            l.Severity = a.Severity;
            l.Acknowledged = a.Acknowledged;
            l.OccurredAt = a.OccurredAt;
            l.ObservedAt = a.ObservedAt;
            return l;
        }

        /// <summary>The line for a device-plane event. A command's own line is returned with the task, so its answer can be recorded when it comes.</summary>
        public static DeviceLine Device(DeviceEvent e)
        {
            switch (e.Kind)
            {
                case DeviceEventKind.LinkState:
                {
                    var l = DeviceLine.Of(DeviceKinds.LinkState, e.ExternalId);
                    l.State = e.State.ToString();
                    return l;
                }

                case DeviceEventKind.Started:
                    return DeviceLine.Of(DeviceKinds.Started, e.ExternalId);
                case DeviceEventKind.StartFailed:
                {
                    var l = DeviceLine.Of(DeviceKinds.StartFailed, e.ExternalId);
                    l.Text = e.Text;
                    return l;
                }

                case DeviceEventKind.FirstPublish:
                    return DeviceLine.Of(DeviceKinds.FirstPublish, e.ExternalId);
                case DeviceEventKind.PumpFaulted:
                {
                    var l = DeviceLine.Of(DeviceKinds.LinkState, e.ExternalId);
                    l.State = "PumpFaulted";
                    l.Text = e.Text;
                    return l;
                }

                case DeviceEventKind.Command:
                {
                    var l = DeviceLine.Of(DeviceKinds.Command, e.ExternalId);
                    l.Text = e.Text;
                    return l;
                }

                case DeviceEventKind.CommandRefused:
                {
                    var l = DeviceLine.Of(DeviceKinds.CommandRefused, e.ExternalId);
                    l.Text = e.Text;
                    return l;
                }

                case DeviceEventKind.Task:
                {
                    var l = DeviceLine.Of(DeviceKinds.TaskReceived, e.ExternalId);
                    l.Token = e.Task.Token;
                    l.Key = e.Task.Key;
                    l.Payload = e.Task.Area;
                    l.Sequence = e.Task.Sequence;
                    return l;
                }

                default:
                    return null;
            }
        }

        /// <summary>The line for a command's answer: what the device's task layer finished with (the outcome it hands the SDK to publish).</summary>
        public static DeviceLine Completed(string externalId, string token, Tasks.TaskResult result)
        {
            var l = DeviceLine.Of(DeviceKinds.TaskCompleted, externalId);
            l.Token = token;
            l.Succeeded = result.Succeeded;
            l.Reason = result.Reason;
            return l;
        }

        /// <summary>The line for a machine starting to drive a route, or (a null route) stopping.</summary>
        public static DeviceLine Route(string externalId, Tasks.Route route)
        {
            var l = DeviceLine.Of(DeviceKinds.Route, externalId);
            l.Points.AddRange(Tasks.RoutePolyline.ToFlat(route));
            return l;
        }

        public static DeviceLine Sample(string externalId, Sample sample, DateTimeOffset ackedAt)
        {
            var l = DeviceLine.Of(DeviceKinds.Sample, externalId);
            l.SampleKind = sample.Kind == SampleKind.Measurement ? "measurement" : "location";
            l.OccurredAt = sample.OccurredUtc;
            l.AckedAt = ackedAt;
            if (sample.Kind == SampleKind.Measurement)
            {
                foreach (var kv in sample.Values)
                    if (!double.IsNaN(kv.Value) && !double.IsInfinity(kv.Value)) l.Values[kv.Key] = kv.Value;
            }
            else
            {
                l.Latitude = Finite(sample.Latitude);
                l.Longitude = Finite(sample.Longitude);
                l.Elevation = Finite(sample.Elevation);
                l.SpeedMps = Finite(sample.SpeedMps);
                l.HeadingDegrees = Finite(sample.HeadingDegrees);
            }

            return l;
        }

        static double? Finite(double v) => double.IsNaN(v) || double.IsInfinity(v) ? (double?)null : v;
    }
}
