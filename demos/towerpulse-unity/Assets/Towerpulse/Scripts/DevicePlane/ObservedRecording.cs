// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Demos.Platform;
using DeviceChain.Demos.Platform.Recording;

namespace DeviceChain.Towerpulse.DevicePlane
{
    /// <summary>Turns what the platform's observer reported into the shared recording's observed lines, which name a device by its platform TOKEN (the run header maps token to external id). Only the kinds Towerpulse tells its story with are kept.</summary>
    public static class ObservedRecording
    {
        public static IEnumerable<ObservedLine> Lines(ObserverItem item)
        {
            switch (item)
            {
                case MeasurementItem m:
                {
                    var l = ObservedLine.Of(ObservedKinds.Measurement);
                    l.Device = m.DeviceToken; l.Name = m.Name; l.Value = m.Value; l.OccurredAt = m.OccurredAt; l.ObservedAt = m.ObservedAt; l.FromSnapshot = m.FromSnapshot;
                    yield return l;
                    break;
                }
                case AlarmItem a when a.Alarm != null:
                    yield return Alarm(a);
                    break;
                case AlarmSnapshotItem s:
                {
                    var l = ObservedLine.Of(ObservedKinds.AlarmSnapshot);
                    l.RequestedAt = s.RequestedAt; l.ObservedAt = s.ObservedAt; l.Truncated = s.Truncated; l.TotalRecords = s.TotalRecords;
                    foreach (var a in s.Alarms) if (a.Alarm != null) l.Alarms.Add(Alarm(a));
                    yield return l;
                    break;
                }
                case CommandItem c when c.Command != null:
                {
                    var l = ObservedLine.Of(ObservedKinds.Command);
                    l.Device = c.DeviceToken; l.Token = c.Command.Token; l.Name = c.Command.Name; l.State = c.Command.Status;
                    l.QueuedAt = c.Command.QueuedAt; l.ObservedAt = c.Command.ObservedAt;
                    yield return l;
                    break;
                }
            }
        }

        static ObservedLine Alarm(AlarmItem a)
        {
            var l = ObservedLine.Of(ObservedKinds.Alarm);
            l.Device = a.DeviceToken; l.Token = a.Alarm.Token; l.Name = a.Alarm.AlarmKey; l.MetricKey = a.Alarm.MetricKey; l.State = a.Alarm.State;
            l.Severity = a.Alarm.Severity; l.Acknowledged = a.Alarm.Acknowledged; l.OccurredAt = a.Alarm.OccurredAt; l.ObservedAt = a.Alarm.ObservedAt;
            return l;
        }
    }
}
