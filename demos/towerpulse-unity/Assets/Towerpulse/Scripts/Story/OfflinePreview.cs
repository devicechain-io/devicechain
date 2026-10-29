// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

using System;
using System.Collections.Generic;
using DeviceChain.Towerpulse.Model;
using DeviceChain.Towerpulse.Plane;

namespace DeviceChain.Towerpulse.Story
{
    /// <summary>
    /// A scripted stand-in for the platform, used ONLY when no platform is connected (stills, a rehearsal on a laptop). It plays the two rules the story
    /// needs (the zone that stays hot while occupied, and the chiller that falls silent) against the plane, in the platform's own vocabulary, and tags every
    /// fact it produces <see cref="FactSource.Preview"/> so a viewer is never told it came from a platform. The thresholds restate the scenario's rules;
    /// the Go scenario is authoritative. Connected to a real platform this class is simply not created.
    /// </summary>
    public sealed class OfflinePreview
    {
        public const double SampleS = 30.0, HotC = 26.0, AbsenceS = 120.0;
        /// <summary>How long a queued command waits before it is sent: the platform delivers on a sweep, not at once.</summary>
        public const double DeliveryDelayS = 12.0;
        public const double OperatorDelayS = 45.0;
        public const double BoostMinutes = 15;

        readonly TowerPlane plane;
        readonly CommandBoard commands;
        readonly AlarmBoard alarms;
        readonly CommandLedger ledger;
        readonly Queue<double> window = new Queue<double>();
        double lastSampleS = double.NegativeInfinity;
        double silentSinceS = double.NaN;
        int seq;

        sealed class Waiting { public string Token; public PlaneDevice Device; public string Name; public double DueS; public bool Sent; public CommandArgs Args; }
        readonly List<Waiting> waiting = new List<Waiting>();
        readonly List<string> awaitingPlatformDone = new List<string>();
        // the human beat: what an operator does some time after seeing an alarm, queued only when that time comes
        readonly List<Waiting> scheduled = new List<Waiting>();

        public OfflinePreview(TowerPlane plane, CommandBoard commands, AlarmBoard alarms, CommandLedger ledger)
        {
            this.plane = plane ?? throw new ArgumentNullException(nameof(plane));
            this.commands = commands ?? throw new ArgumentNullException(nameof(commands));
            this.alarms = alarms ?? throw new ArgumentNullException(nameof(alarms));
            this.ledger = ledger ?? throw new ArgumentNullException(nameof(ledger));
        }

        /// <summary>The device the model calls <paramref name="layoutId"/> (the plane finds devices by their external id).</summary>
        public static PlaneDevice ByLayout(TowerPlane plane, string layoutId)
        {
            foreach (var d in plane.Devices) if (d.LayoutId == layoutId) return d;
            throw new ArgumentException("no device has the layout id " + layoutId, nameof(layoutId));
        }

        public static string TokenFor(int n) { return "preview-cmd-" + n.ToString("0000"); }

        /// <summary>Call after the plane has advanced. Samples at the report cadence and moves queued commands along.</summary>
        public void Step()
        {
            double now = plane.Model.TimeS;
            if (now >= lastSampleS + SampleS || now < lastSampleS) { lastSampleS = now; Sample(now); }
            Release(now);
            Deliver(now);
            Finalise(now);
        }

        void Sample(double now)
        {
            var hot = ByLayout(plane, BuildingLayout.HotZone);
            var z = plane.Model.Zones[plane.Model.Layout.ZoneIndex(BuildingLayout.HotZone)];
            window.Enqueue(z.TempC);
            while (window.Count > 10) window.Dequeue(); // five minutes of 30 s samples
            double sum = 0;
            foreach (var t in window) sum += t;
            double avg = sum / window.Count;
            if (window.Count == 10 && avg > HotC && z.Occupancy > 0)
            {
                if (alarms.Find(hot.ExternalId, "zone-hot") == null || !alarms.Find(hot.ExternalId, "zone-hot").Active)
                {
                    alarms.Raise(hot.ExternalId, "zone-hot", "MAJOR", FactSource.Preview, now);
                    Enqueue(hot, Wire.BoostCooling, now, ArgsNumber(Wire.MinutesParam, BoostMinutes));
                }
            }
            else if (avg <= HotC && alarms.Find(hot.ExternalId, "zone-hot") != null) alarms.Clear(hot.ExternalId, "zone-hot", now);

            var chiller = ByLayout(plane, BuildingLayout.AnnexChiller);
            bool silent = Readings.NotReporting(plane.Model, chiller);
            if (silent)
            {
                if (double.IsNaN(silentSinceS)) silentSinceS = now;
                if (now - silentSinceS >= AbsenceS && (alarms.Find(chiller.ExternalId, "chiller-silent") == null || !alarms.Find(chiller.ExternalId, "chiller-silent").Active))
                {
                    alarms.Raise(chiller.ExternalId, "chiller-silent", "CRITICAL", FactSource.Preview, now);
                    // the human beat: an operator sees the absence alarm and presses restart
                    scheduled.Add(new Waiting { Device = chiller, Name = Wire.Restart, DueS = now + OperatorDelayS, Args = new CommandArgs() });
                }
            }
            else
            {
                silentSinceS = double.NaN;
                if (alarms.Find(chiller.ExternalId, "chiller-silent") != null) alarms.Clear(chiller.ExternalId, "chiller-silent", now);
            }
        }

        static CommandArgs ArgsNumber(string name, double v)
        {
            var a = new CommandArgs();
            a.Numbers[name] = v;
            return a;
        }

        void Enqueue(PlaneDevice device, string name, double atS, CommandArgs args)
        {
            string token = TokenFor(++seq);
            commands.ApplyPlatform(token, device.ExternalId, name, "QUEUED", atS, FactSource.Preview);
            waiting.Add(new Waiting { Token = token, Device = device, Name = name, DueS = atS + DeliveryDelayS, Args = args });
        }

        void Release(double now)
        {
            for (int i = scheduled.Count - 1; i >= 0; i--)
            {
                var w = scheduled[i];
                if (now < w.DueS) continue;
                scheduled.RemoveAt(i);
                Enqueue(w.Device, w.Name, now, w.Args);
            }
        }

        void Deliver(double now)
        {
            foreach (var w in waiting)
            {
                if (w.Sent || now < w.DueS) continue;
                w.Sent = true;
                commands.ApplyPlatform(w.Token, w.Device.ExternalId, w.Name, "SENT", now, FactSource.Preview);
                ledger.Received(w.Token, w.Device.ExternalId, w.Name, now);
                var token = w.Token;
                plane.Commands.Submit(w.Device, token, w.Name, w.Args, r => ledger.Answered(token, r.Success, r.Reason, plane.Model.TimeS));
                awaitingPlatformDone.Add(token);
            }
        }

        void Finalise(double now)
        {
            for (int i = awaitingPlatformDone.Count - 1; i >= 0; i--)
            {
                if (!ledger.TryGet(awaitingPlatformDone[i], out var rec) || !rec.Answered) continue;
                commands.ApplyPlatform(rec.Token, rec.DeviceExternalId, rec.Name, rec.Success ? "SUCCESSFUL" : "FAILED", rec.AnsweredSimS, FactSource.Preview);
                awaitingPlatformDone.RemoveAt(i);
            }
        }
    }
}
